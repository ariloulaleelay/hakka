package tools

import "github.com/ariloulaleelay/hakka/agent"

// RegisterAll registers the full builtin toolset on the given registry.
func RegisterAll(r *agent.ToolRegistry) {
	r.Register(ReadFile())
	r.Register(ListDir())
	r.Register(WriteFile())
	r.Register(EditFile())
	r.Register(Search())
	r.Register(Shell())
	r.Register(HTTPGet())
	r.Register(Random())
	r.Register(FeedbackTool())
	r.Register(VimListBuffers())
	r.Register(VimReadBuffer())
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
// (set by the gateway per-request) to enforce isolation — e.g. a TCP
// client can only see "tcp" sessions, a Telegram chat only sees its own
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
