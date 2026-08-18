package gateways

import (
	"context"
	"encoding/json"
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
	mu             sync.Mutex
	activeSessions map[int64]string
}

// NewTelegramGateway creates a Telegram gateway from explicit dependencies.
func NewTelegramGateway(conv *agent.Conversation, cmd *commands.CommandProcessor, token string) *TelegramGateway {
	return &TelegramGateway{
		Conv:           conv,
		Cmd:            cmd,
		Token:          token,
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

	lowerText := strings.ToLower(msg.Text)
	lowerUsername := strings.ToLower("@" + username)
	return strings.Contains(lowerText, lowerUsername)
}

// formatAuthorInfo builds the author heading for group chat messages.
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

// jsonCmd represents a parsed slash command → JSON command mapping.
type jsonCmd struct {
	Cmd    string
	Params json.RawMessage
}

// parseSlashCommand maps text-based slash commands to structured JSON
// commands for the CommandProcessor. This is the Telegram gateway's
// local mapping — the server no longer interprets text slash commands.
func parseSlashCommand(input string) *jsonCmd {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return nil
	}

	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return nil
	}

	cmd := parts[0]
	// Strip @bot_username suffix
	if idx := strings.Index(cmd, "@"); idx >= 0 {
		cmd = cmd[:idx]
	}

	switch cmd {
	case "/help":
		return &jsonCmd{Cmd: "help", Params: nil}
	case "/continue":
		return &jsonCmd{Cmd: "continue", Params: nil}
	case "/start":
		return &jsonCmd{Cmd: "start", Params: nil}
	case "/compact":
		if len(parts) >= 2 {
			n, err := strconv.Atoi(parts[1])
			if err == nil && n >= 0 {
				p, _ := json.Marshal(map[string]any{"n": n})
				return &jsonCmd{Cmd: "compact", Params: p}
			}
		}
		// No params = read current value
		return &jsonCmd{Cmd: "compact", Params: json.RawMessage("{}")}
	case "/cwd_set":
		if len(parts) < 2 {
			return nil
		}
		path := strings.Join(parts[1:], " ")
		p, _ := json.Marshal(map[string]any{"cwd": path})
		return &jsonCmd{Cmd: "cwd_set", Params: p}

	// --- Model commands ---
	case "/models":
		return &jsonCmd{Cmd: "model_list", Params: nil}
	case "/model":
		if len(parts) >= 2 {
			sub := parts[1]
			switch sub {
			case "list":
				return &jsonCmd{Cmd: "model_list", Params: nil}
			case "switch":
				if len(parts) >= 3 {
					p, _ := json.Marshal(map[string]any{"name": parts[2]})
					return &jsonCmd{Cmd: "model_switch", Params: p}
				}
				return nil
			case "show":
				return &jsonCmd{Cmd: "session_info", Params: nil}
			default:
				// Short form: /model <name>
				p, _ := json.Marshal(map[string]any{"name": sub})
				return &jsonCmd{Cmd: "model_switch", Params: p}
			}
		}
		return &jsonCmd{Cmd: "session_info", Params: nil}

	// --- Session commands ---
	case "/session":
		if len(parts) < 2 {
			return nil
		}
		sub := parts[1]
		switch sub {
		case "list":
			return &jsonCmd{Cmd: "session_list", Params: nil}
		case "create":
			return &jsonCmd{Cmd: "session_create", Params: nil}
		case "get":
			if len(parts) >= 3 {
				p, _ := json.Marshal(map[string]any{"id": parts[2]})
				return &jsonCmd{Cmd: "get_session", Params: p}
			}
			return nil
		case "switch":
			// Alias for "get" — switches the active session in the chat.
			if len(parts) >= 3 {
				p, _ := json.Marshal(map[string]any{"id": parts[2]})
				return &jsonCmd{Cmd: "get_session", Params: p}
			}
			return nil
		case "info":
			return &jsonCmd{Cmd: "session_info", Params: nil}
		case "delete":
			if len(parts) >= 3 {
				p, _ := json.Marshal(map[string]any{"id": parts[2]})
				return &jsonCmd{Cmd: "session_delete", Params: p}
			}
			return nil
		case "rename":
			if len(parts) >= 3 {
				name := strings.Join(parts[2:], " ")
				p, _ := json.Marshal(map[string]any{"name": name})
				return &jsonCmd{Cmd: "session_rename", Params: p}
			}
			return nil
		case "autorename":
			return &jsonCmd{Cmd: "session_autorename", Params: nil}
		}
		return nil

	// --- Tool commands ---
	case "/tool":
		if len(parts) < 2 {
			return nil
		}
		sub := parts[1]
		switch sub {
		case "list":
			return &jsonCmd{Cmd: "tool_list", Params: nil}
		case "allow":
			if len(parts) >= 3 {
				p, _ := json.Marshal(map[string]any{"name": parts[2]})
				return &jsonCmd{Cmd: "tool_allow", Params: p}
			}
			return nil
		case "deny":
			if len(parts) >= 3 {
				p, _ := json.Marshal(map[string]any{"name": parts[2]})
				return &jsonCmd{Cmd: "tool_deny", Params: p}
			}
			return nil
		}
		return nil
	}

	return nil
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
	session, err := gw.Conv.Sessions().Get(ctx, namespace, sessionID)
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

	// Parse text slash commands locally and execute as JSON commands.
	if jc := parseSlashCommand(inputText); jc != nil {
		cmdRes := gw.Cmd.ExecuteJSON(ctx, sessionID, jc.Cmd, jc.Params)
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
	if err := session.AddMessages(ctx, []agent.Message{{Role: agent.RoleUser, Content: inputText}}, 0, 0); err != nil {
		slog.Error("telegram: failed to append unmentioned message", "chat_id", chatID, "error", err)
	}
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
// the active session mapping (e.g. when get_session fetches a different
// session).
func (gw *TelegramGateway) handleCommandResult(chatID int64, sessionID string, cmdRes commands.CommandResult) {
	slog.Debug("telegram: command handled",
		"chat_id", chatID, "session_id", sessionID,
		"cmd", cmdRes.Cmd)

	if cmdRes.Error != nil {
		gw.reply(chatID, fmt.Sprintf("error: %v", cmdRes.Error))
		return
	}

	// For commands with structured Data, render a human-readable reply.
	if cmdRes.Data != nil {
		text := gw.formatCommandResult(cmdRes)
		if text != "" {
			gw.reply(chatID, text)
		}
	}

	// Update active session mapping if the result references a different
	// session (e.g. get_session, session_create, session_delete this).
	if cmdRes.Session != nil && cmdRes.Session.SessionID() != sessionID {
		gw.mu.Lock()
		gw.activeSessions[chatID] = cmdRes.Session.SessionID()
		gw.mu.Unlock()
	}
}

// formatCommandResult converts structured command data into human-readable
// text for Telegram display.
func (gw *TelegramGateway) formatCommandResult(res commands.CommandResult) string {
	if res.Data == nil {
		return ""
	}

	var data map[string]any
	if err := json.Unmarshal(res.Data, &data); err != nil {
		return ""
	}

	switch res.Cmd {
	case "help":
		if cmds, ok := data["commands"].([]any); ok {
			var lines []string
			for _, c := range cmds {
				if cmd, ok := c.(map[string]any); ok {
					// Use display name when available (space-separated for Telegram),
					// fall back to the JSON command name (underscore-separated).
					name, _ := cmd["display"].(string)
					if name == "" {
						name, _ = cmd["cmd"].(string)
					}
					line := fmt.Sprintf("/%s — %s", name, cmd["desc"])
					lines = append(lines, line)
				}
			}
			return strings.Join(lines, "\n")
		}

	case "session_list":
		if sessions, ok := data["sessions"].([]any); ok {
			var lines []string
			for _, s := range sessions {
				if sess, ok := s.(map[string]any); ok {
					mark := ""
					if current, _ := sess["current"].(bool); current {
						mark = "▶ "
					}
					name, _ := sess["name"].(string)
					id, _ := sess["id"].(string)
					short, _ := sess["short_id"].(string)
					msgCount, _ := sess["message_count"].(float64)
					display := name
					if display == "" {
						display = short
					}
					lines = append(lines, fmt.Sprintf("%s%s [%s] (%d msgs)", mark, display, id, int(msgCount)))
				}
			}
			return strings.Join(lines, "\n")
		}

	case "session_info":
		if sess, ok := data["session"].(map[string]any); ok {
			id, _ := sess["id"].(string)
			name, _ := sess["name"].(string)
			model, _ := sess["model"].(string)
			msgCount, _ := sess["message_count"].(float64)
			tokens, _ := sess["total_tokens"].(float64)
			contextEst, _ := sess["estimated_context"].(float64)
			return fmt.Sprintf("Session: %s\nName: %s\nModel: %s\nMessages: %d\nContext: ~%d tokens\nTotal tokens: %d",
				id, name, model, int(msgCount), int(contextEst), int(tokens))
		}

	case "session_create":
		if sess, ok := data["session"].(map[string]any); ok {
			id, _ := sess["id"].(string)
			return fmt.Sprintf("Created session: %s", id)
		}

	case "get_session":
		if sess, ok := data["session"].(map[string]any); ok {
			id, _ := sess["id"].(string)
			name, _ := sess["name"].(string)
			msgCount, _ := sess["message_count"].(float64)
			model, _ := sess["model"].(string)
			display := name
			if display == "" {
				display = "(unnamed)"
			}
			return fmt.Sprintf("Session: %s (%s)\nModel: %s\nMessages: %d",
				display, id, model, int(msgCount))
		}

	case "session_delete":
		if deleted, ok := data["deleted"].(string); ok {
			return fmt.Sprintf("Deleted session: %s", deleted)
		}

	case "session_rename":
		if sess, ok := data["session"].(map[string]any); ok {
			name, _ := sess["name"].(string)
			return fmt.Sprintf("Session renamed to: %s", name)
		}

	case "session_autorename":
		if sess, ok := data["session"].(map[string]any); ok {
			name, _ := sess["name"].(string)
			return fmt.Sprintf("Session renamed to: %s", name)
		}

	case "model_list":
		if models, ok := data["models"].([]any); ok {
			var lines []string
			for _, m := range models {
				if mod, ok := m.(map[string]any); ok {
					mark := ""
					if current, _ := mod["current"].(bool); current {
						mark = "▶ "
					}
					name, _ := mod["name"].(string)
					lines = append(lines, mark+name)
				}
			}
			return strings.Join(lines, "\n")
		}

	case "model_switch":
		if model, ok := data["model"].(string); ok {
			return fmt.Sprintf("Model set to: %s", model)
		}

	case "tool_list":
		if tools, ok := data["tools"].([]any); ok {
			var lines []string
			for _, t := range tools {
				if tool, ok := t.(map[string]any); ok {
					name, _ := tool["name"].(string)
					enabled, _ := tool["enabled"].(bool)
					status := "🔴"
					if enabled {
						status = "🟢"
					}
					lines = append(lines, fmt.Sprintf("%s %s", status, name))
				}
			}
			return strings.Join(lines, "\n")
		}

	case "tool_allow":
		if allowed, ok := data["allowed"].([]any); ok {
			names := make([]string, len(allowed))
			for i, n := range allowed {
				names[i] = fmt.Sprint(n)
			}
			return fmt.Sprintf("Allowed: %s", strings.Join(names, ", "))
		}

	case "tool_deny":
		if denied, ok := data["denied"].([]any); ok {
			names := make([]string, len(denied))
			for i, n := range denied {
				names[i] = fmt.Sprint(n)
			}
			return fmt.Sprintf("Denied: %s", strings.Join(names, ", "))
		}

	case "compact":
		if limit, ok := data["compact_soft_limit"].(float64); ok {
			if int(limit) == 0 {
				return "Compact limit: off"
			}
			return fmt.Sprintf("Compact limit: %d tokens", int(limit))
		}

	case "cwd_set":
		if cwd, ok := data["cwd"].(string); ok {
			return fmt.Sprintf("CWD set to: %s", cwd)
		}
	}

	// Fallback: use Reply text if available.
	if res.Reply != "" {
		return res.Reply
	}
	return ""
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
	sessions, err := gw.Conv.Sessions().List(ctx, namespace)
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
	session, err := gw.Conv.Sessions().Create(ctx, namespace)
	if err != nil {
		// Fallback: use chat ID as session ID
		sid := strconv.FormatInt(chatID, 10)
		gw.activeSessions[chatID] = sid
		return sid
	}

	// Pre-enable Telegram-safe tools and deny dangerous ones for new sessions.
	gw.initTelegramSession(ctx, namespace, session)

	gw.activeSessions[chatID] = session.SessionID()
	return session.SessionID()
}

// initTelegramSession pre-enables tools that are safe for Telegram users
// (no filesystem, shell, or Neovim access) and denies tools that should
// not be visible to external users (e.g. subagent_run).
func (gw *TelegramGateway) initTelegramSession(ctx context.Context, namespace string, session *agent.Session) {
	telegramTools := []string{
		"http_get", "random", "feedback",
		"session_list", "session_create", "get_session",
		"session_info", "session_rename", "session_delete",
		"session_autorename", "session_read", "session_search",
		"session_summarize", "session_ask_question",
	}
	for _, name := range telegramTools {
		if err := session.AllowAndEnableTool(ctx, name); err != nil {
			slog.Warn("telegram: failed to enable tool", "tool", name, "error", err)
			return
		}
	}

	// Deny subagent_run — it's not safe for external Telegram users.
	session.DenyTool(ctx, "subagent_run")

	if err := gw.Conv.Sessions().Save(ctx, namespace, session); err != nil {
		slog.Warn("telegram: failed to save initialized session",
			"namespace", namespace,
			"session_id", session.SessionID(),
			"error", err,
		)
	}
}

// convExecute is a convenience wrapper that consumes the event channel
// from Conversation.Execute and returns only the final reply/error.
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
		msg := tgbotapi.NewMessage(chatID, html)
		msg.ParseMode = tgbotapi.ModeHTML
		if _, err := gw.bot.Send(msg); err != nil {
			slog.Warn("telegram: failed to send message", "chat_id", chatID, "error", err)
		}
		return
	}

	slog.Debug("telegram: response too long, splitting into multiple messages",
		"chat_id", chatID,
		"len", len(html),
	)

	for len(html) > 0 {
		chunk := html
		if len(chunk) > maxLen {
			chunk = chunk[:maxLen]
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
// until done is closed or the context is cancelled.
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
