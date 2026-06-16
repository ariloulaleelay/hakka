package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/you/hakka/agent"
	"github.com/you/hakka/agent/commands"
	"github.com/you/hakka/agent/config"
	"github.com/you/hakka/agent/event"
	"github.com/you/hakka/agent/gateways"
	"github.com/you/hakka/agent/mcp"
	sqlitestore "github.com/you/hakka/agent/stores/sqlite"
	hakkatools "github.com/you/hakka/agent/tools"
)

type appConfig struct {
	configPath        string
	tcpAddr           string
	wsAddr            string
	dbPath            string
	logLevel          string
	llmDebug          string // directory for LLM request/response debug logs; empty = disabled
	telegramToken     string // Telegram bot token; falls back to TELEGRAM_BOT_TOKEN env var
	telegramWhitelist string // comma-separated list of allowed chat IDs
}

func main() {
	var cfg appConfig
	flag.StringVar(&cfg.configPath, "config", "cmd/hakka/hakka.json", "Path to model config JSON")
	flag.StringVar(&cfg.tcpAddr, "tcp-addr", "127.0.0.1:9876", "TCP gateway bind address")
	flag.StringVar(&cfg.wsAddr, "ws-addr", ":8765", "WebSocket gateway bind address")
	flag.StringVar(&cfg.dbPath, "db", "", "Optional SQLite session DB path (default: in-memory)")
	flag.StringVar(&cfg.logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	flag.StringVar(&cfg.llmDebug, "llm-debug", "", "Directory for LLM request/response debug logs (empty = disabled)")
	flag.StringVar(&cfg.telegramToken, "telegram-token", "", "Telegram bot token (env: TELEGRAM_BOT_TOKEN)")
	flag.StringVar(&cfg.telegramWhitelist, "telegram-whitelist", "", "Comma-separated list of allowed Telegram chat IDs (env: TELEGRAM_WHITELIST)")
	flag.Parse()

	logger := newLogger(cfg.logLevel)
	slog.SetDefault(logger)

	if err := run(cfg, logger); err != nil {
		logger.Error("fatal error", "err", err)
		os.Exit(1)
	}
}

func run(cfg appConfig, logger *slog.Logger) error {
	modelCfg, err := config.Load(cfg.configPath)
	if err != nil {
		return fmt.Errorf("load config %q: %w", cfg.configPath, err)
	}

	registry, err := config.BuildRegistry(modelCfg, cfg.llmDebug)
	if err != nil {
		return fmt.Errorf("build registry: %w", err)
	}
	logger.Info("models loaded", "default", registry.Default(), "names", registry.Names())

	store, closeStore, err := openStore(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore()

	sessions, router, tools, systemPrompt, engineCfg := setupComponents(store, registry, logger)

	// Connect to MCP servers (if configured)
	mcpMgr := mcp.NewManager()
	mcpMgr.Logger = logger
	if servers := modelCfg.MCPServerConfigs(); len(servers) > 0 {
		for name, cfg := range servers {
			mcpMgr.Add(name, mcp.ServerConfig{
				Command: cfg.Command,
				Args:    cfg.Args,
				Env:     cfg.Env,
				URL:     cfg.URL,
				Headers: cfg.Headers,
			})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := mcpMgr.ConnectAll(ctx, tools); err != nil {
			cancel()
			mcpMgr.CloseAll()
			return fmt.Errorf("mcp connect: %w", err)
		}
		cancel()
		logger.Info("mcp servers connected", "count", len(servers))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gws, err := buildGateways(sessions, router, tools, systemPrompt, engineCfg, cfg.tcpAddr, cfg.wsAddr, cfg.telegramToken, cfg.telegramWhitelist)
	if err != nil {
		return fmt.Errorf("build gateways: %w", err)
	}

	if err := startGateways(ctx, gws); err != nil {
		return fmt.Errorf("gateway start failed: %w", err)
	}
	logger.Info("hakka up", "tcp", cfg.tcpAddr, "ws", cfg.wsAddr)

	<-ctx.Done()
	logger.Info("shutting down")

	// Disconnect MCP servers
	mcpMgr.CloseAll()

	return shutdownGateways(gws)
}

func setupComponents(store agent.SessionStore, registry *agent.Registry, logger *slog.Logger) (*agent.SessionManager, *agent.Router, *agent.ToolRegistry, string, agent.EngineConfig) {
	const systemPrompt = "You are Hakka, a helpful assistant."

	sessions := agent.NewSessionManager(store, systemPrompt)
	router := agent.NewRouter(registry)

	tools := agent.NewToolRegistry()
	hakkatools.RegisterAll(tools)

	cfg := agent.EngineConfig{
		MaxToolIterations: 256,
		Logger:            logger,
		Hooks: agent.Hooks{
			OnToolCall: func(sessionID string, call agent.ToolCall) {
				logger.Info("tool call", "session", sessionID, "name", call.Name, "args", call.Arguments)
			},
			OnToolResult: func(sessionID string, call agent.ToolCall, result event.ToolResult) {
				logger.Info("tool result", "session", sessionID, "name", call.Name, "result", result.ForLLM())
			},
			OnError: func(sessionID string, err error) {
				logger.Error("engine error", "session", sessionID, "err", err)
			},
		},
	}

	return sessions, router, tools, systemPrompt, cfg
}

func buildGateways(sessions *agent.SessionManager, router *agent.Router, tools *agent.ToolRegistry, systemPrompt string, cfg agent.EngineConfig, tcpAddr, wsAddr, telegramToken, telegramWhitelist string) ([]gateways.Gateway, error) {
	if tools == nil {
		tools = agent.NewToolRegistry()
		hakkatools.RegisterAll(tools)
	}

	// Register session-control tools on the main (TCP/WS) tool registry.
	// These tools let the LLM manage sessions within its own namespace.
	hakkatools.RegisterSessionTools(tools, sessions, nil)

	tcpConv := agent.NewConversation(sessions, router, tools, "tcp", cfg)
	tcpStreamer := agent.NewStreamSession(tcpConv, "tcp")
	tcpCmd := commands.New(sessions, tcpConv, systemPrompt, "tcp")
	tcpCmd.SetTools(tools)

	wsConv := agent.NewConversation(sessions, router, tools, "ws", cfg)
	wsStreamer := agent.NewStreamSession(wsConv, "ws")
	wsCmd := commands.New(sessions, wsConv, systemPrompt, "ws")
	wsCmd.SetTools(tools)

	// Telegram gets a restricted toolset — no file, shell, or Neovim tools.
	// Session tools are safe (no filesystem/shell access) and are scoped
	// to the per-chat namespace, so we include them for Telegram users too.
	tgTools := agent.NewToolRegistry()
	hakkatools.RegisterTelegramTools(tgTools)
	hakkatools.RegisterSessionTools(tgTools, sessions, nil)
	tgConv := agent.NewConversation(sessions, router, tgTools, "tg", cfg)
	tgCmd := commands.New(sessions, tgConv, systemPrompt, "tg")
	tgCmd.SetTools(tgTools)

	gatewaysList := []gateways.Gateway{
		gateways.NewTCPGateway(tcpConv, tcpStreamer, tcpCmd, tcpAddr),
		gateways.NewWebSocketGateway(wsConv, wsStreamer, wsCmd, wsAddr),
	}
	if telegramToken == "" {
		telegramToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if telegramWhitelist == "" {
		telegramWhitelist = os.Getenv("TELEGRAM_WHITELIST")
	}
	if telegramToken != "" {
		tgGw := gateways.NewTelegramGateway(tgConv, tgCmd, telegramToken)
		whitelist, err := parseWhitelist(telegramWhitelist)
		if err != nil {
			return nil, fmt.Errorf("telegram whitelist: %w", err)
		}
		tgGw.Whitelist = whitelist
		gatewaysList = append(gatewaysList, tgGw)
	}
	return gatewaysList, nil
}

// parseWhitelist converts a comma-separated string of chat IDs into a set.
// Returns (nil, nil) on empty input, meaning all chats are allowed.
// Returns an error if any ID is malformed, so misconfiguration is caught
// early rather than silently ignored.
func parseWhitelist(s string) (map[int64]struct{}, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	set := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid chat ID %q in whitelist: %w", part, err)
		}
		set[id] = struct{}{}
	}
	if len(set) == 0 {
		return nil, nil
	}
	return set, nil
}

func startGateways(ctx context.Context, gatewaysList []gateways.Gateway) error {
	for _, gw := range gatewaysList {
		if err := gw.Start(ctx); err != nil {
			return err
		}
	}
	return nil
}

func shutdownGateways(gatewaysList []gateways.Gateway) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var lastErr error
	for _, gw := range gatewaysList {
		if err := gw.Stop(shutdownCtx); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func openStore(path string) (agent.SessionStore, func(), error) {
	if path == "" {
		return agent.NewMemoryStore(), func() {}, nil
	}
	sqlStore, err := sqlitestore.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return sqlStore, func() { _ = sqlStore.Close() }, nil
}
