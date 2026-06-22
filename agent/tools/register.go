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
	r.Register(VimListBuffers())
	r.Register(VimReadBuffer())
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
