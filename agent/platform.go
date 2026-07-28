package agent

// Platform bundles once-per-process engine components and provides
// namespace-scoped building blocks for gateways.
//
// Create once at startup, then call ForNamespace for each gateway
// (WebSocket, Telegram, gRPC, etc.) to get namespace-isolated
// Conversation and StreamSession instances.
//
// Usage:
//
//	platform := agent.NewPlatform(agent.PlatformConfig{
//	    Store:        store,
//	    Registry:     registry,
//	    SystemPrompt: "You are a helpful assistant.",
//	})
//	wsNs := platform.ForNamespace("ws", tools, decorator)
//	cmd := commands.New(platform.Sessions(), wsNs.Conversation, platform.SystemPrompt(), "ws")
//	cmd.SetTools(tools)
//	handler := gateways.NewTurnHandler(wsNs.Conversation, wsNs.Streamer, cmd, "ws")
type Platform struct {
	sessions     *SessionManager
	router       *Router
	engineCfg    EngineConfig
	skills       *SkillRegistry
	systemPrompt string
}

// PlatformConfig holds once-per-process configuration for Platform.
// Store, Registry, and SystemPrompt are required. All other fields
// are optional and default to sensible zero values.
type PlatformConfig struct {
	Store         SessionStore
	Registry      *Registry
	SystemPrompt  string
	EngineCfg     EngineConfig     // optional; zero-value fields inherit from DefaultEngineConfig()
	SkillRegistry *SkillRegistry   // optional
}

// NamespaceComponents is a bundle of engine components scoped to a
// single namespace. Use these along with a CommandProcessor to
// construct a TurnHandler for your gateway transport.
type NamespaceComponents struct {
	Conversation *Conversation
	Streamer     *StreamSession
	Tools        *ToolRegistry
}

// NewPlatform assembles once-per-process engine components from the
// given configuration.
func NewPlatform(cfg PlatformConfig) *Platform {
	engineCfg := cfg.EngineCfg
	def := DefaultEngineConfig()
	if engineCfg.MaxToolIterations <= 0 {
		engineCfg.MaxToolIterations = def.MaxToolIterations
	}
	if engineCfg.CompactSoftLimit <= 0 {
		engineCfg.CompactSoftLimit = def.CompactSoftLimit
	}

	return &Platform{
		sessions:     NewSessionManager(cfg.Store, cfg.SystemPrompt),
		router:       NewRouter(cfg.Registry),
		engineCfg:    engineCfg,
		skills:       cfg.SkillRegistry,
		systemPrompt: cfg.SystemPrompt,
	}
}

// Sessions returns the shared SessionManager, which can create and
// manage sessions across all namespaces.
func (p *Platform) Sessions() *SessionManager { return p.sessions }

// Router returns the shared model Router, which resolves which LLM
// adapter to use for a given session.
func (p *Platform) Router() *Router { return p.router }

// Skills returns the shared SkillRegistry, or nil if none was
// configured.
func (p *Platform) Skills() *SkillRegistry { return p.skills }

// SystemPrompt returns the configured system prompt.
func (p *Platform) SystemPrompt() string { return p.systemPrompt }

// EngineConfig returns the engine configuration, with defaults
// already applied.
func (p *Platform) EngineConfig() EngineConfig { return p.engineCfg }

// ForNamespace creates a namespace-scoped bundle of engine components.
//
// tools is the tool registry for this namespace. It should already
// have all desired tools registered (system tools, session tools,
// skill tools, subagent tools, etc.). Callers can pass the same
// registry to multiple ForNamespace calls if they want shared tools.
//
// decorator is an optional ToolContextDecorator. Pass nil if tools
// in this namespace don't need client communication (e.g. Telegram).
// Pass EngineChannelClientDecorator() for WebSocket/Neovim gateways.
func (p *Platform) ForNamespace(ns string, tools *ToolRegistry, decorator ToolContextDecorator) NamespaceComponents {
	conv := NewConversation(p.sessions, p.router, tools, ns, p.engineCfg)

	if decorator != nil {
		conv.SetToolContext(decorator)
	}

	if p.skills != nil {
		conv.SetSkills(p.skills)
	}

	streamer := NewStreamSession(conv, ns)

	return NamespaceComponents{
		Conversation: conv,
		Streamer:     streamer,
		Tools:        tools,
	}
}
