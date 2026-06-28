package gateways

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/net/proxy"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/event"
	"github.com/ariloulaleelay/hakka/agent/format"
)

// TelegramGateway maps a Telegram chat to one or more Hakka sessions
// within the "tg:<chat_id>" namespace. /start resets the current session.
// The gateway maintains an in-memory map of which session is active per chat.
//
// Namespace isolation is achieved via the Go context: every incoming
// message enriches the context with the per-chat namespace BEFORE any
// operation (command processing, conversation execution), so there is
// no need to mutate shared fields on the Conversation or CommandProcessor.
// This ensures thread safety when multiple chats are handled concurrently.
type TelegramGateway struct {
	Conv  *agent.Conversation
	Cmd   *commands.CommandProcessor
	Token string

	// Whitelist restricts which chat IDs can interact with the bot.
	// When nil or empty, all chats are allowed. When non-empty, only
	// chats with an entry in this map may send messages.
	Whitelist map[int64]struct{}

	// SOCKS5 optionally specifies a SOCKS5 proxy address (e.g. "127.0.0.1:1080")
	// to connect through when reaching the Telegram Bot API. When non-empty,
	// the gateway creates an HTTP client tunnelled through the proxy.
	SOCKS5 string

	bot    *tgbotapi.BotAPI
	cancel context.CancelFunc
	done   chan struct{}

	// activeSessions maps chatID → active session ID within the chat's
	// namespace. On cache miss (e.g. after restart), the store is queried
	// for existing sessions — see getOrCreateSession.
	mu              sync.Mutex
	activeSessions  map[int64]string
}

// NewTelegramGateway creates a Telegram gateway from explicit dependencies.
func NewTelegramGateway(conv *agent.Conversation, cmd *commands.CommandProcessor, token string) *TelegramGateway {
	return &TelegramGateway{
		Conv:  conv,
		Cmd:   cmd,
		Token: token,
		activeSessions: make(map[int64]string),
	}
}

func (gw *TelegramGateway) Start(ctx context.Context) error {
	var bot *tgbotapi.BotAPI
	var err error

	if gw.SOCKS5 != "" {
		slog.Info("telegram: using SOCKS5 proxy",
			"socks5_addr", gw.SOCKS5,
		)
		var dialer proxy.Dialer
		dialer, err = proxy.SOCKS5("tcp", gw.SOCKS5, nil, proxy.Direct)
		if err != nil {
			return fmt.Errorf("telegram: SOCKS5 dialer: %w", err)
		}
		httpClient := &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Dial: dialer.Dial,
			},
		}
		bot, err = tgbotapi.NewBotAPIWithClient(gw.Token, tgbotapi.APIEndpoint, httpClient)
	} else {
		bot, err = tgbotapi.NewBotAPI(gw.Token)
	}
	if err != nil {
		return err
	}
	gw.bot = bot

	slog.Info("telegram: bot started",
		"bot_username", bot.Self.UserName,
		"bot_id", bot.Self.ID,
	)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	ctx, cancel := context.WithCancel(ctx)
	gw.cancel = cancel
	gw.done = make(chan struct{})

	go func() {
		defer close(gw.done)
		for {
			select {
			case <-ctx.Done():
				bot.StopReceivingUpdates()
				return
			case upd, ok := <-updates:
				if !ok {
					return
				}
				if upd.Message == nil {
					slog.Debug("telegram: update with nil message, skipping",
						"update_id", upd.UpdateID,
					)
					continue
				}
				gw.dispatch(ctx, upd)
			}
		}
	}()
	return nil
}

func (gw *TelegramGateway) Stop(_ context.Context) error {
	if gw.cancel != nil {
		gw.cancel()
	}
	if gw.done != nil {
		<-gw.done
	}
	return nil
}

// isAuthorized checks whether the given chat ID is allowed to use the bot.
// If the whitelist is nil or empty, all chats are authorised (backward
// compatible). Otherwise the chat ID must be present in the whitelist.
func (gw *TelegramGateway) isAuthorized(chatID int64) bool {
	if len(gw.Whitelist) == 0 {
		return true
	}
	_, ok := gw.Whitelist[chatID]
	return ok
}

// isBotMentioned checks whether the message mentions the bot's username.
// It first checks message entities for "mention" type, then falls back
// to a simple substring check on the text.
func (gw *TelegramGateway) isBotMentioned(msg *tgbotapi.Message) bool {
	if gw.bot == nil {
		slog.Warn("telegram: isBotMentioned called but bot is nil")
		return false
	}
	username := gw.bot.Self.UserName
	if username == "" {
		slog.Warn("telegram: isBotMentioned called but bot username is empty")
		return false
	}

	// Check message entities for "mention" type
	for i, ent := range msg.Entities {
		slog.Debug("telegram: checking entity",
			"entity_index", i,
			"entity_type", ent.Type,
			"entity_offset", ent.Offset,
			"entity_length", ent.Length,
		)
		if ent.Type == "mention" && ent.Offset+ent.Length <= len(msg.Text) {
			mention := msg.Text[ent.Offset : ent.Offset+ent.Length]
			slog.Debug("telegram: comparing mention",
				"mention", mention,
				"expected", "@"+username,
			)
			if strings.EqualFold(mention, "@"+username) {
				return true
			}
		}
	}

	// Check message entities for "bot_command" type — Telegram group
	// commands like /session@bot_username use bot_command entities.
	for _, ent := range msg.Entities {
		if ent.Type == "bot_command" && ent.Offset+ent.Length <= len(msg.Text) {
			cmdText := msg.Text[ent.Offset : ent.Offset+ent.Length]
			slog.Debug("telegram: checking bot_command entity",
				"cmd_text", cmdText,
				"expected_suffix", "@"+username,
			)
			if strings.HasSuffix(cmdText, "@"+username) {
				return true
			}
		}
	}

	// Fallback: check text for @username
	lowerText := strings.ToLower(msg.Text)
	lowerUsername := strings.ToLower("@" + username)
	contains := strings.Contains(lowerText, lowerUsername)
	slog.Debug("telegram: fallback mention check",
		"lower_text", lowerText,
		"looking_for", lowerUsername,
		"contains", contains,
	)
	return contains
}

// formatAuthorInfo builds the author heading for group chat messages.
// Format: @username (FirstName LastName):\n
func (gw *TelegramGateway) formatAuthorInfo(user *tgbotapi.User) string {
	login := user.UserName
	name := user.FirstName
	if user.LastName != "" {
		if name != "" {
			name += " " + user.LastName
		} else {
			name = user.LastName
		}
	}

	var buf strings.Builder
	if login != "" {
		buf.WriteString("@")
		buf.WriteString(login)
		if name != "" {
			buf.WriteString(" (")
			buf.WriteString(name)
			buf.WriteString(")")
		}
	} else if name != "" {
		buf.WriteString("(")
		buf.WriteString(name)
		buf.WriteString(")")
	}

	if buf.Len() > 0 {
		buf.WriteString(":\n")
	}
	return buf.String()
}

func (gw *TelegramGateway) dispatch(ctx context.Context, upd tgbotapi.Update) {
	if upd.Message == nil || upd.Message.Text == "" {
		slog.Debug("telegram: ignoring update with nil/empty message",
			"update_id", upd.UpdateID,
		)
		return
	}

	chatID := upd.Message.Chat.ID
	chatType := upd.Message.Chat.Type

	gw.logIncomingMessage(upd)

	if !gw.isAuthorized(chatID) {
		gw.replyUnauthorized(chatID)
		return
	}

	namespace := fmt.Sprintf("tg:%d", chatID)
	ctx = event.ContextWithNamespace(ctx, namespace)

	isGroup := chatType == "group" || chatType == "supergroup"
	inputText := gw.buildInputText(upd, isGroup)

	sessionID := gw.getOrCreateSession(ctx, namespace, chatID)
	session, err := gw.Conv.Sessions.GetOrCreate(ctx, namespace, sessionID)
	if err != nil {
		slog.Warn("telegram: failed to get session",
			"chat_id", chatID, "session_id", sessionID, "error", err)
		gw.reply(chatID, fmt.Sprintf("error: %v", err))
		return
	}

	if isGroup && !gw.isBotMentioned(upd.Message) {
		gw.appendUnmentioned(ctx, session, namespace, chatID, inputText)
		return
	}

	if upd.Message.IsCommand() && upd.Message.Command() == "start" {
		gw.resetSession(chatID)
		return
	}

	if cmdRes := gw.Cmd.Execute(ctx, sessionID, inputText); cmdRes.Handled {
		gw.handleCommandResult(chatID, sessionID, cmdRes)
		return
	}

	gw.runConversation(ctx, namespace, sessionID, chatID, inputText)
}

// logIncomingMessage logs metadata about an incoming Telegram message.
func (gw *TelegramGateway) logIncomingMessage(upd tgbotapi.Update) {
	from := upd.Message.From
	fromID := int64(0)
	fromUsername := ""
	fromFirstName := ""
	fromLastName := ""
	if from != nil {
		fromID = from.ID
		fromUsername = from.UserName
		fromFirstName = from.FirstName
		fromLastName = from.LastName
	}
	slog.Debug("telegram: incoming message",
		"chat_id", upd.Message.Chat.ID,
		"chat_type", upd.Message.Chat.Type,
		"from_id", fromID,
		"from_username", fromUsername,
		"from_first_name", fromFirstName,
		"from_last_name", fromLastName,
		"text", upd.Message.Text,
		"entities_count", len(upd.Message.Entities),
	)
}

// replyUnauthorized sends a restricted-bot message to an unauthorised chat.
func (gw *TelegramGateway) replyUnauthorized(chatID int64) {
	slog.Info("telegram: unauthorized chat blocked",
		"chat_id", chatID, "whitelist_len", len(gw.Whitelist))
	gw.reply(chatID, fmt.Sprintf(
		"⛔ This bot is restricted. Your chat ID is %d. Please ask the administrator to add it to the whitelist.",
		chatID,
	))
}

// buildInputText produces the final input text for the LLM / command
// processor. In group chats it prepends author info (e.g. "@user:"),
// except for bot commands which need the raw command text.
func (gw *TelegramGateway) buildInputText(upd tgbotapi.Update, isGroup bool) string {
	text := upd.Message.Text
	if !isGroup || upd.Message.From == nil || upd.Message.IsCommand() {
		return text
	}
	heading := gw.formatAuthorInfo(upd.Message.From)
	return heading + text
}

// appendUnmentioned records an unmentioned group-chat message in the
// session history without replying to the chat.
func (gw *TelegramGateway) appendUnmentioned(
	ctx context.Context,
	session agent.SessionView,
	namespace string,
	chatID int64,
	inputText string,
) {
	slog.Debug("telegram: appending unmentioned message to history",
		"chat_id", chatID, "session_id", session.SessionID())
	session.Append(agent.Message{Role: agent.RoleUser, Content: inputText})
	_ = gw.Conv.Sessions.Save(ctx, namespace, session)
}

// resetSession clears the active session for a chat (used by /start).
func (gw *TelegramGateway) resetSession(chatID int64) {
	slog.Debug("telegram: /start command received", "chat_id", chatID)
	gw.mu.Lock()
	if gw.activeSessions != nil {
		delete(gw.activeSessions, chatID)
	}
	gw.mu.Unlock()
	gw.reply(chatID, "session reset")
}

// handleCommandResult sends the command reply and optionally updates
// the active session mapping (e.g. when /session switch changes it).
func (gw *TelegramGateway) handleCommandResult(chatID int64, sessionID string, cmdRes commands.CommandResult) {
	slog.Debug("telegram: command handled",
		"chat_id", chatID, "session_id", sessionID,
		"reply_len", len(cmdRes.Reply))
	if cmdRes.Error != nil {
		gw.reply(chatID, fmt.Sprintf("error: %v", cmdRes.Error))
	} else if cmdRes.Reply != "" {
		gw.reply(chatID, cmdRes.Reply)
	}
	if cmdRes.Session != nil && cmdRes.Session.SessionID() != sessionID {
		gw.mu.Lock()
		gw.activeSessions[chatID] = cmdRes.Session.SessionID()
		gw.mu.Unlock()
	}
}

// runConversation executes the LLM turn for the given input and sends
// the reply. It manages the typing indicator lifecycle around the call.
func (gw *TelegramGateway) runConversation(
	ctx context.Context,
	namespace string,
	sessionID string,
	chatID int64,
	inputText string,
) {
	slog.Debug("telegram: sending to conversation",
		"chat_id", chatID, "session_id", sessionID, "input_len", len(inputText))

	typingDone := make(chan struct{})
	go gw.keepTyping(ctx, chatID, typingDone)

	reply, err := gw.convExecute(ctx, namespace, sessionID, inputText)
	close(typingDone)

	if err != nil {
		slog.Warn("telegram: conversation error",
			"chat_id", chatID, "error", err)
		gw.reply(chatID, fmt.Sprintf("error: %v", err))
		return
	}
	if reply == "" {
		reply = "..."
	}
	slog.Debug("telegram: sending reply",
		"chat_id", chatID, "reply_len", len(reply))
	gw.reply(chatID, reply)
}

// getOrCreateSession returns the active session ID for the given chat.
// On first call (or after a restart), it looks up existing sessions in
// the store for this namespace. If one is found, the most recent session
// is reused — this preserves conversation history across server restarts.
// Only when no sessions exist at all does it create a brand new one.
func (gw *TelegramGateway) getOrCreateSession(ctx context.Context, namespace string, chatID int64) string {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	if gw.activeSessions == nil {
		gw.activeSessions = make(map[int64]string)
	}

	if sid, ok := gw.activeSessions[chatID]; ok {
		return sid
	}

	// Cache miss: look up existing sessions in the store before creating
	// a new one. This preserves session continuity across server restarts.
	sessions, err := gw.Conv.Sessions.List(ctx, namespace)
	if err != nil {
		slog.Warn("telegram: failed to list sessions for namespace, creating new",
			"namespace", namespace,
			"error", err,
		)
	} else if len(sessions) > 0 {
		// Reuse the most recent session (List returns oldest-first).
		latest := sessions[len(sessions)-1]
		slog.Debug("telegram: reusing existing session after restart",
			"chat_id", chatID,
			"session_id", latest.SessionID(),
			"sessions_found", len(sessions),
		)
		gw.activeSessions[chatID] = latest.SessionID()
		return latest.SessionID()
	}

	// No existing sessions found — create a new one in the chat's namespace.
	session, err := gw.Conv.Sessions.GetOrCreate(ctx, namespace, "")
	if err != nil {
		// Fallback: use chat ID as session ID
		sid := strconv.FormatInt(chatID, 10)
		gw.activeSessions[chatID] = sid
		return sid
	}
	// Telegram sessions have no meaningful working directory, so
	// ClientCWD remains empty and BuildContext won't inject it.
	gw.activeSessions[chatID] = session.SessionID()
	return session.SessionID()
}

// convExecute is a convenience wrapper that consumes the event channel
// from Conversation.Execute and returns only the final reply/error.
// Namespace is resolved from context by Conversation's resolveNamespace.
func (gw *TelegramGateway) convExecute(ctx context.Context, namespace, sessionID, input string) (string, error) {
	eventCh, err := gw.Conv.Execute(ctx, sessionID, input)
	if err != nil {
		return "", err
	}
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				return "", te.Err
			}
			return te.Reply, nil
		}
	}
	return "", nil
}

func (gw *TelegramGateway) reply(chatID int64, text string) {
	const maxLen = 4000
	html := format.MarkdownToTelegramHTML(text)

	if len(html) <= maxLen {
		// Short enough — send as a single formatted message.
		msg := tgbotapi.NewMessage(chatID, html)
		msg.ParseMode = tgbotapi.ModeHTML
		if _, err := gw.bot.Send(msg); err != nil {
			slog.Warn("telegram: failed to send message", "chat_id", chatID, "error", err)
		}
		return
	}

	// Long response — split into multiple messages.
	slog.Debug("telegram: response too long, splitting into multiple messages",
		"chat_id", chatID,
		"len", len(html),
	)

	for len(html) > 0 {
		chunk := html
		if len(chunk) > maxLen {
			chunk = chunk[:maxLen]
			// Try to break at the last newline within the chunk for cleaner splits.
			if idx := strings.LastIndex(chunk, "\n"); idx > 0 {
				chunk = chunk[:idx]
			}
			html = html[len(chunk):]
		} else {
			html = ""
		}
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = tgbotapi.ModeHTML
		if _, err := gw.bot.Send(msg); err != nil {
			slog.Warn("telegram: failed to send chunk",
				"chat_id", chatID,
				"error", err,
				"chunk_len", len(chunk),
			)
			return
		}
	}
}

// sendChatAction sends a chat action (e.g. "typing") to a specific chat.
// The Telegram Bot API returns {"ok": true, "result": true} for this endpoint,
// so we use MakeRequest directly instead of bot.Send (which expects a Message
// struct). Errors are logged at debug level only — transient issues should
// not interrupt the conversation flow.
func (gw *TelegramGateway) sendChatAction(chatID int64, action string) {
	params := tgbotapi.Params{
		"chat_id": strconv.FormatInt(chatID, 10),
		"action":  action,
	}
	resp, err := gw.bot.MakeRequest("sendChatAction", params)
	if err != nil {
		slog.Debug("telegram: sendChatAction failed",
			"chat_id", chatID,
			"action", action,
			"error", err,
		)
		return
	}
	if !resp.Ok {
		slog.Debug("telegram: sendChatAction not ok",
			"chat_id", chatID,
			"action", action,
			"description", resp.Description,
		)
	}
}

// keepTyping periodically sends the "typing" chat action to the given chat
// until done is closed or the context is cancelled. The first action is sent
// immediately, then every 5 seconds as recommended by Telegram's Bot API docs.
// This gives the user visual feedback that the bot is processing their request.
func (gw *TelegramGateway) keepTyping(ctx context.Context, chatID int64, done <-chan struct{}) {
	gw.sendChatAction(chatID, tgbotapi.ChatTyping)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			gw.sendChatAction(chatID, tgbotapi.ChatTyping)
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

var _ Gateway = (*TelegramGateway)(nil)
