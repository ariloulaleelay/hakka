package tools

import "github.com/ariloulaleelay/hakka/agent"

// RegisterAll registers the full builtin toolset on the given registry.
func RegisterAll(r *agent.ToolRegistry, cs *CheckpointStore) {
	r.Register(ReadFile())
	r.Register(ListDir())
	r.Register(WriteFile(cs))
	r.Register(EditFile(cs))
	r.Register(Search())
	r.Register(Shell(cs))
	r.Register(HTTPGet())
	r.Register(Random())
	r.Register(FeedbackTool())
	r.Register(VimListBuffers())
	r.Register(VimReadBuffer())
	r.Register(Rollback(cs))
}

// RegisterMeta registers system meta-tools that are always executable
// but never appear in SchemasForSession — the engine injects them
// into schemas conditionally. For example, context_compactify only
// appears when the soft token limit is exceeded.
func RegisterMeta(r *agent.ToolRegistry) {
	r.Register(Compactify())
}

// RegisterProcessTools registers process interaction tools on the given
// registry. These tools let the LLM spawn, interact with, and kill
// subprocesses (e.g. debuggers, REPLs, long-running commands).
//
// The pm parameter is the ProcessManager that holds the running processes.
func RegisterProcessTools(r *agent.ToolRegistry, pm *ProcessManager) {
	r.Register(SpawnProcess(pm))
	r.Register(InteractProcess(pm))
	r.Register(KillProcess(pm))
	r.Register(ListProcesses(pm))
}

// RegisterSessionTools registers session-control tools on the given
// registry. These tools read the current namespace from the Go context
// (set by the gateway per-request) to enforce isolation — e.g. a WebSocket
// client can only see "default" sessions, a Telegram chat only sees its own
// "tg:<chat_id>" sessions.
//
// The conv parameter is optional; when non-nil, session_summarize can
// use the configured LLM to generate summaries. Pass nil to use
// heuristic summaries only.
func RegisterSessionTools(r *agent.ToolRegistry, sm *agent.SessionManager, conv *agent.Conversation) {
	r.Register(SessionList(sm))
	r.Register(SessionRename(sm))
	r.Register(SessionInfo(sm))
	r.Register(SessionRead(sm))
	r.Register(SessionSearch(sm))
	r.Register(SessionSummarize(sm, conv))
	r.Register(SessionDelete(sm))
	r.Register(SessionCreate(sm))
	r.Register(SessionAskQuestion(sm, conv))
}

// RegisterToolManagementTools registers tool-management tools that let the
// LLM discover and inspect tools at runtime:
//   - show_tool — show detailed information about a specific tool and enable it
//
// show_tool is pre-enabled in every new session so the LLM can always call it
// to bootstrap tool access.
//
// allow_tool and deny_tool are NOT registered as LLM-callable tools — they are
// human-only slash commands (/tool allow, /tool deny) available in command_processor.go.
func RegisterToolManagementTools(r *agent.ToolRegistry) {
	r.Register(ShowTool(r))
}

// RegisterSubagentTools registers the subagent_run tool on the given registry.
// The subagent tool lets the LLM fork its current session and run an autonomous
// subtask in a child LLM session. It needs access to the router, tool registry,
// engine config, and skill registry to set up the child conversation.
//
// The subagent_run tool is automatically blocked on child sessions to prevent
// infinite recursion.
func RegisterSubagentTools(r *agent.ToolRegistry, router *agent.Router, tools *agent.ToolRegistry, cfg agent.EngineConfig, skills *agent.SkillRegistry) {
	r.Register(SubagentRun(router, tools, cfg, skills))
}

// RegisterSkillTools registers skill-management tools on the given registry.
// These tools let the LLM discover, inspect, load, unload, and import skills.
//
// The sr parameter is the SkillRegistry that holds the available skills.
func RegisterSkillTools(r *agent.ToolRegistry, sr *agent.SkillRegistry) {
	r.Register(SearchSkills(sr))
	r.Register(InspectSkill(sr))
	r.Register(LoadSkill(sr))
	r.Register(UnloadSkill(sr))
	r.Register(ImportSkill(sr))
}
