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

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/config"
	"github.com/ariloulaleelay/hakka/agent/event"
	"github.com/ariloulaleelay/hakka/agent/gateways"
	"github.com/ariloulaleelay/hakka/agent/mcp"
	postgresstore "github.com/ariloulaleelay/hakka/agent/stores/postgres"
	sqlitestore "github.com/ariloulaleelay/hakka/agent/stores/sqlite"
	hakkatools "github.com/ariloulaleelay/hakka/agent/tools"
	"github.com/ariloulaleelay/hakka/agent/webfront"
	"github.com/ariloulaleelay/hakka/batch"
)

type appConfig struct {
	configPath        string
	wsAddr            string
	webAddr           string
	dbPath            string
	logLevel          string
	llmDebug          string // directory for LLM request/response debug logs; empty = disabled
	telegramToken     string // Telegram bot token; falls back to TELEGRAM_BOT_TOKEN env var
	telegramWhitelist string // comma-separated list of allowed chat IDs
	telegramSOCKS5    string // SOCKS5 proxy address; falls back to TELEGRAM_SOCKS5 env var
	run               string // batch mode: task string
	runFile           string // batch mode: path to file with task
	runEnableTools    string // batch mode: comma-separated tool names or tags to enable
	runCompactLimit   int    // batch mode: compact soft limit (0 = default 200000)
}

func main() {
	var cfg appConfig
	flag.StringVar(&cfg.configPath, "config", "hakka.json", "Path to model config JSON")
	flag.StringVar(&cfg.wsAddr, "ws-addr", ":8765", "WebSocket gateway bind address")
	flag.StringVar(&cfg.webAddr, "web-addr", "", "Web frontend HTTP server address (e.g. :8080). Serves SPA + WebSocket on the same port. Empty = disabled")
	flag.StringVar(&cfg.dbPath, "db", "", "Session DB URL: sqlite:path/to/file, postgres://user:pass@host/db, or plain file path for SQLite (default: in-memory)")
	flag.StringVar(&cfg.logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	flag.StringVar(&cfg.llmDebug, "llm-debug", "", "Directory for LLM request/response debug logs (empty = disabled)")
	flag.StringVar(&cfg.telegramToken, "telegram-token", "", "Telegram bot token (env: TELEGRAM_BOT_TOKEN)")
	flag.StringVar(&cfg.telegramWhitelist, "telegram-whitelist", "", "Comma-separated list of allowed Telegram chat IDs (env: TELEGRAM_WHITELIST)")
	flag.StringVar(&cfg.telegramSOCKS5, "telegram-socks5", "", "SOCKS5 proxy for Telegram (e.g. 127.0.0.1:1080) (env: TELEGRAM_SOCKS5)")
	flag.StringVar(&cfg.run, "run", "", "Run a single autonomous task in batch mode (no servers started)")
	flag.StringVar(&cfg.runFile, "run-file", "", "Read task from file and run in batch mode")
	flag.StringVar(&cfg.runEnableTools, "run-enable-tool", "", "Comma-separated tools/tags to enable (use '#tag' for explicit tag, e.g. '#utility,read_file')")
	flag.IntVar(&cfg.runCompactLimit, "compact-soft-limit", 0, "Compact soft limit in tokens (0 = default 200000, use in batch mode)")
	flag.Parse()

	logger := newLogger(cfg.logLevel)
	slog.SetDefault(logger)

	// Batch mode: run once and exit.
	if cfg.run != "" || cfg.runFile != "" {
		task := cfg.run
		if cfg.runFile != "" {
			data, err := os.ReadFile(cfg.runFile)
			if err != nil {
				logger.Error("cannot read task file", "path", cfg.runFile, "err", err)
				os.Exit(1)
			}
			task = string(data)
		}
		if task == "" {
			logger.Error("empty task: nothing to run")
			os.Exit(1)
		}

		var enableTools []string
		if cfg.runEnableTools != "" {
			enableTools = strings.Split(cfg.runEnableTools, ",")
			for i := range enableTools {
				enableTools[i] = strings.TrimSpace(enableTools[i])
			}
		}

		if err := runBatch(logger, cfg.configPath, task, enableTools, cfg.runCompactLimit); err != nil {
			logger.Error("batch run failed", "err", err)
			os.Exit(1)
		}
		return
	}

	// Server mode: start gateways.
	if err := run(cfg, logger); err != nil {
		logger.Error("fatal error", "err", err)
		os.Exit(1)
	}
}

func runBatch(logger *slog.Logger, configPath, task string, enableTools []string, compactSoftLimit int) error {
	modelCfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config %q: %w", configPath, err)
	}

	registry, err := config.BuildRegistry(modelCfg, "")
	if err != nil {
		return fmt.Errorf("build registry: %w", err)
	}
	logger.Info("batch run", "model", registry.Default(), "task", task[:min(len(task), 80)])

	// Connect MCP servers if configured.
	tools := agent.NewToolRegistry()
	pm := hakkatools.NewProcessManager()
	ck, _ := hakkatools.NewCheckpointStore(hakkatools.DefaultCheckpointDir())
	hakkatools.RegisterAll(tools, ck)
	hakkatools.RegisterMeta(tools)
	hakkatools.RegisterProcessTools(tools, pm)

	// Override feedback URL if configured.
	if url := modelCfg.FeedbackEndpoint(); url != "" {
		hakkatools.SetFeedbackURL(url)
	}

	mcpMgr := mcp.NewManager()
	mcpMgr.Logger = logger
	if servers := modelCfg.MCPServerConfigs(); len(servers) > 0 {
		// We need a temporary session manager for session tools during MCP setup.
		store := agent.NewMemoryStore()
		sessions := agent.NewSessionManager(store, "")
		hakkatools.RegisterSessionTools(tools, sessions, nil)

		for name, srvCfg := range servers {
			mcpMgr.Add(name, mcp.ServerConfig{
				Command: srvCfg.Command,
				Args:    srvCfg.Args,
				Env:     srvCfg.Env,
				URL:     srvCfg.URL,
				Headers: srvCfg.Headers,
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
	defer mcpMgr.CloseAll()

	// Run the task.
	skillRegistry := agent.NewSkillRegistry()
	hakkatools.RegisterSkillTools(tools, skillRegistry)

	_, err = batch.RunBatch(context.Background(), batch.RunBatchParams{
		Registry:         registry,
		Task:             task,
		Tools:            tools,
		EnableTools:      enableTools,
		CompactSoftLimit: compactSoftLimit,
		Logger:           logger,
		Skills:           skillRegistry,
	})
	return err
}

// ---------------------------------------------------------------------------
// Server mode
// ---------------------------------------------------------------------------

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

	store, idGen, closeStore, err := openStore(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore()

	if idGen != nil {
		agent.SetDefaultIDGenerator(idGen)
	}

	// Override feedback URL if configured.
	if url := modelCfg.FeedbackEndpoint(); url != "" {
		hakkatools.SetFeedbackURL(url)
	}

	// ── Platform: once-per-process engine components ──────────────────
	engineCfg := agent.DefaultEngineConfig()
	engineCfg.Logger = logger
	engineCfg.Hooks = agent.Hooks{
		OnToolCall: func(sessionID string, call agent.ToolCall) {
			logger.Info("tool call", "session", sessionID, "name", call.Name, "args", call.Arguments)
		},
		OnToolResult: func(sessionID string, call agent.ToolCall, result event.ToolResult) {
			logger.Info("tool result", "session", sessionID, "name", call.Name, "result", result.ForLLM())
		},
		OnError: func(sessionID string, err error) {
			logger.Error("engine error", "session", sessionID, "err", err)
		},
	}

	skillRegistry := agent.NewSkillRegistry()

	platform := agent.NewPlatform(agent.PlatformConfig{
		Store:         store,
		Registry:      registry,
		SystemPrompt:  "You are Hakka, a helpful assistant.",
		EngineCfg:     engineCfg,
		SkillRegistry: skillRegistry,
	})

	// ── Shared tool registry (WebSocket / webfront) ───────────────────
	pm := hakkatools.NewProcessManager()
	tools := agent.NewToolRegistry()
	ck, _ := hakkatools.NewCheckpointStore(hakkatools.DefaultCheckpointDir())
	hakkatools.RegisterAll(tools, ck)
	hakkatools.RegisterMeta(tools)
	hakkatools.RegisterProcessTools(tools, pm)
	hakkatools.RegisterToolManagementTools(tools)
	hakkatools.RegisterSkillTools(tools, platform.Skills())
	hakkatools.RegisterSessionTools(tools, platform.Sessions(), nil)
	hakkatools.RegisterSubagentTools(tools, platform.Sessions(), platform.Router(), tools, engineCfg, platform.Skills())

	// ── MCP servers ───────────────────────────────────────────────────
	mcpMgr := mcp.NewManager()
	mcpMgr.Logger = logger
	if servers := modelCfg.MCPServerConfigs(); len(servers) > 0 {
		for name, srvCfg := range servers {
			mcpMgr.Add(name, mcp.ServerConfig{
				Command: srvCfg.Command,
				Args:    srvCfg.Args,
				Env:     srvCfg.Env,
				URL:     srvCfg.URL,
				Headers: srvCfg.Headers,
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

	// ── Gateways ──────────────────────────────────────────────────────
	clientDecorator := agent.EngineChannelClientDecorator()

	hub := gateways.NewNamespaceHub("ws")

	wsGw := buildWebSocketGateway(platform, tools, clientDecorator, cfg.wsAddr, hub)

	tgGw, err := buildTelegramGateway(platform, cfg.telegramToken, cfg.telegramWhitelist, cfg.telegramSOCKS5)
	if err != nil {
		return fmt.Errorf("build telegram gateway: %w", err)
	}

	gws := []gateways.Gateway{wsGw}
	if tgGw != nil {
		gws = append(gws, tgGw)
	}
	if cfg.webAddr != "" {
		wfGw := buildWebFrontGateway(platform, tools, cfg.webAddr, hub)
		gws = append(gws, wfGw)
	}

	if err := startGateways(ctx, gws); err != nil {
		return fmt.Errorf("gateway start failed: %w", err)
	}
	logger.Info("hakka up", "ws", cfg.wsAddr, "web", cfg.webAddr)

	<-ctx.Done()
	logger.Info("shutting down")

	mcpMgr.CloseAll()

	return shutdownGateways(gws)
}

// buildWebSocketGateway wires the WebSocket gateway using the platform
// and the shared namespace hub.
func buildWebSocketGateway(platform *agent.Platform, tools *agent.ToolRegistry, decorator agent.ToolContextDecorator, addr string, hub *gateways.NamespaceHub) *gateways.WebSocketGateway {
	ns := platform.ForNamespace("ws", tools, decorator)
	cmd := commands.New(platform.Sessions(), ns.Conversation, platform.SystemPrompt(), "ws")
	cmd.SetTools(tools)
	gw := gateways.NewWebSocketGateway(ns.Conversation, cmd, addr)
	gw.Handler.SetHub(hub)
	return gw
}

// buildWebFrontGateway creates a webfront Gateway that serves the embedded
// SPA and a WebSocket endpoint on the same HTTP port. It shares the same
// namespace ("ws") and hub as the standalone WebSocket gateway so sessions
// and turn events are consistent across all transports.
func buildWebFrontGateway(platform *agent.Platform, tools *agent.ToolRegistry, addr string, hub *gateways.NamespaceHub) *webfront.Gateway {
	decorator := agent.EngineChannelClientDecorator()
	ns := platform.ForNamespace("ws", tools, decorator)
	cmd := commands.New(platform.Sessions(), ns.Conversation, platform.SystemPrompt(), "ws")
	cmd.SetTools(tools)
	wfGw := webfront.New(addr, ns.Conversation, cmd)
	wfGw.SetHub(hub)
	return wfGw
}

// buildTelegramGateway wires the Telegram gateway with a restricted
// toolset (no filesystem, shell, or Neovim tools). Returns (nil, nil)
// when no Telegram token is configured. Token, whitelist, and SOCKS5
// proxy fall back to environment variables when the flags are empty.
func buildTelegramGateway(platform *agent.Platform, telegramToken, telegramWhitelist, telegramSOCKS5 string) (*gateways.TelegramGateway, error) {
	if telegramToken == "" {
		telegramToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if telegramWhitelist == "" {
		telegramWhitelist = os.Getenv("TELEGRAM_WHITELIST")
	}
	if telegramSOCKS5 == "" {
		telegramSOCKS5 = os.Getenv("TELEGRAM_SOCKS5")
	}
	if telegramToken == "" {
		return nil, nil
	}

	// Telegram gets a restricted toolset — no file, shell, or Neovim tools.
	// Session tools are safe (no filesystem/shell access) and are scoped
	// to the per-chat namespace, so we include them for Telegram users too.
	tgTools := agent.NewToolRegistry()
	hakkatools.RegisterTelegramTools(tgTools)
	hakkatools.RegisterToolManagementTools(tgTools)
	hakkatools.RegisterSessionTools(tgTools, platform.Sessions(), nil)
	// NOTE: subagent_run is intentionally NOT registered for Telegram.
	// External users should not be able to fork autonomous subagents.

	ns := platform.ForNamespace("tg", tgTools, nil)
	cmd := commands.New(platform.Sessions(), ns.Conversation, platform.SystemPrompt(), "tg")
	cmd.SetTools(tgTools)

	gw := gateways.NewTelegramGateway(ns.Conversation, cmd, telegramToken)
	gw.SOCKS5 = telegramSOCKS5

	whitelist, err := parseWhitelist(telegramWhitelist)
	if err != nil {
		return nil, fmt.Errorf("telegram whitelist: %w", err)
	}
	gw.Whitelist = whitelist
	return gw, nil
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

func openStore(dataSource string) (agent.SessionStore, agent.IDGenerator, func(), error) {
	if dataSource == "" {
		return agent.NewMemoryStore(), nil, func() {}, nil
	}

	// URL-based scheme detection.
	switch {
	case strings.HasPrefix(dataSource, "postgres://"), strings.HasPrefix(dataSource, "postgresql://"):
		store, err := postgresstore.Open(dataSource)
		if err != nil {
			return nil, nil, nil, err
		}
		idGen := store.IDGenerator()
		_ = store.MigrateMessageIDs(context.Background(), idGen)
		return store, idGen, func() { _ = store.Close() }, nil

	case strings.HasPrefix(dataSource, "sqlite:"):
		path := strings.TrimPrefix(dataSource, "sqlite:")
		store, err := sqlitestore.Open(path)
		if err != nil {
			return nil, nil, nil, err
		}
		idGen := store.IDGenerator()
		_ = store.MigrateMessageIDs(context.Background(), idGen)
		return store, idGen, func() { _ = store.Close() }, nil

	default:
		// Plain path — assume SQLite (backward compatibility).
		store, err := sqlitestore.Open(dataSource)
		if err != nil {
			return nil, nil, nil, err
		}
		idGen := store.IDGenerator()
		_ = store.MigrateMessageIDs(context.Background(), idGen)
		return store, idGen, func() { _ = store.Close() }, nil
	}
}
