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
	sqlitestore "github.com/ariloulaleelay/hakka/agent/stores/sqlite"
	hakkatools "github.com/ariloulaleelay/hakka/agent/tools"
	"github.com/ariloulaleelay/hakka/batch"
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
	telegramSOCKS5    string // SOCKS5 proxy address; falls back to TELEGRAM_SOCKS5 env var
	run               string // batch mode: task string
	runFile           string // batch mode: path to file with task
	runEnableTools    string // batch mode: comma-separated tool names or tags to enable
	runCompactLimit   int    // batch mode: compact soft limit (0 = default 200000)
}

func main() {
	var cfg appConfig
	flag.StringVar(&cfg.configPath, "config", "hakka.json", "Path to model config JSON")
	flag.StringVar(&cfg.tcpAddr, "tcp-addr", "127.0.0.1:9876", "TCP gateway bind address")
	flag.StringVar(&cfg.wsAddr, "ws-addr", ":8765", "WebSocket gateway bind address")
	flag.StringVar(&cfg.dbPath, "db", "", "Optional SQLite session DB path (default: in-memory)")
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
	hakkatools.RegisterAll(tools)
	hakkatools.RegisterMeta(tools)

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
	_, err = batch.RunBatch(context.Background(), batch.RunBatchParams{
		Registry:         registry,
		Task:             task,
		Tools:            tools,
		EnableTools:      enableTools,
		CompactSoftLimit: compactSoftLimit,
		Logger:           logger,
	})
	return err
}

// ---------------------------------------------------------------------------
// Server mode (unchanged below)
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

	store, closeStore, err := openStore(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore()

	sessions, router, tools, systemPrompt, engineCfg := setupComponents(store, registry, logger)

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

	gws, err := buildGateways(sessions, router, tools, systemPrompt, engineCfg, cfg.tcpAddr, cfg.wsAddr, cfg.telegramToken, cfg.telegramWhitelist, cfg.telegramSOCKS5)
	if err != nil {
		return fmt.Errorf("build gateways: %w", err)
	}

	if err := startGateways(ctx, gws); err != nil {
		return fmt.Errorf("gateway start failed: %w", err)
	}
	logger.Info("hakka up", "tcp", cfg.tcpAddr, "ws", cfg.wsAddr)

	<-ctx.Done()
	logger.Info("shutting down")

	mcpMgr.CloseAll()

	return shutdownGateways(gws)
}

func setupComponents(store agent.SessionStore, registry *agent.Registry, logger *slog.Logger) (*agent.SessionManager, *agent.Router, *agent.ToolRegistry, string, agent.EngineConfig) {
	const systemPrompt = "You are Hakka, a helpful assistant."

	sessions := agent.NewSessionManager(store, systemPrompt)
	router := agent.NewRouter(registry)

	tools := agent.NewToolRegistry()
	hakkatools.RegisterAll(tools)
	hakkatools.RegisterMeta(tools)

	cfg := agent.DefaultEngineConfig()
	cfg.Logger = logger
	cfg.Hooks = agent.Hooks{
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

	return sessions, router, tools, systemPrompt, cfg
}

func buildGateways(sessions *agent.SessionManager, router *agent.Router, tools *agent.ToolRegistry, systemPrompt string, cfg agent.EngineConfig, tcpAddr, wsAddr, telegramToken, telegramWhitelist, telegramSOCKS5 string) ([]gateways.Gateway, error) {
	if tools == nil {
		tools = agent.NewToolRegistry()
		hakkatools.RegisterAll(tools)
		hakkatools.RegisterMeta(tools)
	}

	// Register session-control tools on the main (TCP/WS) tool registry.
	// These tools let the LLM manage sessions within its own namespace.
	hakkatools.RegisterSessionTools(tools, sessions, nil)

	// TCP and WebSocket gateways serve clients (e.g. Neovim) that can
	// receive client-bound requests from tools. Install a decorator so
	// those requests flow through the engine event loop and stay
	// serialised on a single writer goroutine.
	clientDecorator := agent.EngineChannelClientDecorator()

	tcpConv := agent.NewConversation(sessions, router, tools, "tcp", cfg)
	tcpConv.ToolContext = clientDecorator
	tcpStreamer := agent.NewStreamSession(tcpConv, "tcp")
	tcpCmd := commands.New(sessions, tcpConv, systemPrompt, "tcp")
	tcpCmd.SetTools(tools)

	wsConv := agent.NewConversation(sessions, router, tools, "ws", cfg)
	wsConv.ToolContext = clientDecorator
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
	if telegramSOCKS5 == "" {
		telegramSOCKS5 = os.Getenv("TELEGRAM_SOCKS5")
	}
	if telegramToken != "" {
		tgGw := gateways.NewTelegramGateway(tgConv, tgCmd, telegramToken)
		tgGw.SOCKS5 = telegramSOCKS5
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
