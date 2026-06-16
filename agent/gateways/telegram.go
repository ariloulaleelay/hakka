package gateways

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/you/hakka/agent"
	"github.com/you/hakka/agent/commands"
	"github.com/you/hakka/agent/event"
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

	bot    *tgbotapi.BotAPI
	cancel context.CancelFunc
	done   chan struct{}

	// activeSessions maps chatID → active session ID within the chat's
	// namespace. Persisted only in memory; on server restart the first
	// message from each chat creates a fresh session.
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
	bot, err := tgbotapi.NewBotAPI(gw.Token)
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

	// Fallback: check text for @username
	lowerText := strings.ToLower(msg.Text)
	lowerUsername := strings.ToLower("@" + username)
	contains := strings.Contains(lowerText, lowerUsername)
	slog.Debug("telegram: fallback mention check",
		"lower_text", lowerText,
		"looking_for", lowerUsername,
		"contains", contains,
	)
	if contains {
		return true
	}

	return false
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
		"chat_id", chatID,
		"chat_type", chatType,
		"from_id", fromID,
		"from_username", fromUsername,
		"from_first_name", fromFirstName,
		"from_last_name", fromLastName,
		"text", upd.Message.Text,
		"entities_count", len(upd.Message.Entities),
	)

	// Whitelist check: if whitelist is non-empty, only authorised chats
	// may interact with the bot.
	if !gw.isAuthorized(chatID) {
		slog.Info("telegram: unauthorized chat blocked",
			"chat_id", chatID,
			"whitelist_len", len(gw.Whitelist),
		)
		gw.reply(chatID, fmt.Sprintf(
			"⛔ This bot is restricted. Your chat ID is %d. Please ask the administrator to add it to the whitelist.",
			chatID,
		))
		return
	}

	namespace := fmt.Sprintf("tg:%d", chatID)

	// Set the per-chat namespace in the context early, before any
	// operation (command processing, conversation execution) that reads
	// it. Conversation, CommandProcessor, and all sub-handlers resolve
	// namespace from context first, falling back to their configured
	// field — this eliminates the need to mutate shared state and
	// ensures thread safety across concurrent chat dispatches.
	ctx = event.ContextWithNamespace(ctx, namespace)

	// In group/supergroup chats, check whether the bot is mentioned.
	isGroup := chatType == "group" || chatType == "supergroup"

	// Build the input text with author info for group chats.
	inputText := upd.Message.Text
	if isGroup && from != nil {
		heading := gw.formatAuthorInfo(from)
		inputText = heading + inputText
		slog.Debug("telegram: prepended author heading",
			"chat_id", chatID,
			"heading", heading,
			"full_input", inputText,
		)
	} else {
		slog.Debug("telegram: using raw text as input",
			"chat_id", chatID,
			"isGroup", isGroup,
			"has_from", from != nil,
			"input_text", inputText,
		)
	}

	// Get or create the active session for this chat.
	// We need the session early so we can append unmentioned messages.
	sessionID := gw.getOrCreateSession(ctx, namespace, chatID)
	session, err := gw.Conv.Sessions.GetOrCreate(ctx, namespace, sessionID)
	if err != nil {
		slog.Warn("telegram: failed to get session",
			"chat_id", chatID,
			"session_id", sessionID,
			"error", err,
		)
		gw.reply(chatID, fmt.Sprintf("error: %v", err))
		return
	}

	if isGroup {
		botUsername := ""
		if gw.bot != nil {
			botUsername = gw.bot.Self.UserName
		}
		mentioned := gw.isBotMentioned(upd.Message)
		slog.Debug("telegram: group chat mention check",
			"chat_id", chatID,
			"bot_username", botUsername,
			"mentioned", mentioned,
		)
		if !mentioned {
			// Append to history but don't reply.
			slog.Debug("telegram: appending unmentioned message to history",
				"chat_id", chatID,
				"session_id", sessionID,
				"input_text", inputText,
			)
			session.Append(agent.Message{Role: agent.RoleUser, Content: inputText})
			_ = gw.Conv.Sessions.Save(ctx, namespace, session)
			return
		}
	}

	// Handle /start command: creates a fresh session for this chat.
	if upd.Message.IsCommand() && upd.Message.Command() == "start" {
		slog.Debug("telegram: /start command received",
			"chat_id", chatID,
		)
		gw.mu.Lock()
		if gw.activeSessions != nil {
			delete(gw.activeSessions, chatID)
		}
		gw.mu.Unlock()
		gw.reply(chatID, "session reset")
		return
	}

	// Try slash commands first. The namespace is already in context,
	// so CommandProcessor and its sub-handlers will resolve it via
	// resolveNamespace(ctx) — no need to mutate any shared field.
	cmdRes := gw.Cmd.Execute(ctx, sessionID, inputText)
	if cmdRes.Handled {
		slog.Debug("telegram: command handled",
			"chat_id", chatID,
			"input_text", inputText,
			"reply_len", len(cmdRes.Reply),
		)
		if cmdRes.Error != nil {
			gw.reply(chatID, fmt.Sprintf("error: %v", cmdRes.Error))
		} else if cmdRes.Reply != "" {
			gw.reply(chatID, cmdRes.Reply)
		}
		if cmdRes.Session != nil && cmdRes.Session.ID != sessionID {
			gw.mu.Lock()
			gw.activeSessions[chatID] = cmdRes.Session.ID
			gw.mu.Unlock()
		}
		return
	}

	slog.Debug("telegram: sending to conversation",
		"chat_id", chatID,
		"session_id", sessionID,
		"input_len", len(inputText),
	)

	// Conversation.Execute reads namespace from context via
	// resolveNamespace(ctx), so the per-chat namespace set above is
	// picked up automatically — no field mutation needed.
	reply, err := gw.convExecute(ctx, namespace, sessionID, inputText)
	if err != nil {
		slog.Warn("telegram: conversation error",
			"chat_id", chatID,
			"error", err,
		)
		gw.reply(chatID, fmt.Sprintf("error: %v", err))
		return
	}
	if reply == "" {
		reply = "..."
	}
	slog.Debug("telegram: sending reply",
		"chat_id", chatID,
		"reply_len", len(reply),
	)
	gw.reply(chatID, reply)
}

// getOrCreateSession returns the active session ID for the given chat.
// On first call, it creates a new session and records it as active.
func (gw *TelegramGateway) getOrCreateSession(ctx context.Context, namespace string, chatID int64) string {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	if gw.activeSessions == nil {
		gw.activeSessions = make(map[int64]string)
	}

	if sid, ok := gw.activeSessions[chatID]; ok {
		return sid
	}

	// Create a new session in the chat's namespace.
	session, err := gw.Conv.Sessions.GetOrCreate(ctx, namespace, "")
	if err != nil {
		// Fallback: use chat ID as session ID
		sid := strconv.FormatInt(chatID, 10)
		gw.activeSessions[chatID] = sid
		return sid
	}
	// Telegram sessions have no meaningful working directory, so
	// ClientCWD remains empty and BuildContext won't inject it.
	gw.activeSessions[chatID] = session.ID
	return session.ID
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
	for len(text) > 0 {
		chunk := text
		if len(chunk) > maxLen {
			chunk = chunk[:maxLen]
		}
		text = text[len(chunk):]
		msg := tgbotapi.NewMessage(chatID, chunk)
		_, _ = gw.bot.Send(msg)
	}
}

var _ Gateway = (*TelegramGateway)(nil)
