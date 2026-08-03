package gateways

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// WebSocketGateway exposes the engine over /ws.
type WebSocketGateway struct {
	Handler *TurnHandler
	Addr    string

	server *http.Server
}

func NewWebSocketGateway(conv *agent.Conversation, cmd *commands.CommandProcessor, addr string) *WebSocketGateway {
	if addr == "" {
		addr = ":8765"
	}
	ns := conv.Namespace()
	if ns == "" {
		ns = "default"
	}
	return &WebSocketGateway{
		Handler: NewTurnHandler(conv, cmd, ns),
		Addr:    addr,
	}
}

func (gw *WebSocketGateway) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", gw.handle)

	gw.server = &http.Server{
		Addr:              gw.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", gw.Addr)
	if err != nil {
		return err
	}
	go func() {
		if err := gw.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if gw.Handler.Conv != nil && gw.Handler.Conv.Config().Logger != nil {
				gw.Handler.Conv.Config().Logger.Error("websocket serve error", "err", err)
			}
		}
	}()
	return nil
}

func (gw *WebSocketGateway) Stop(ctx context.Context) error {
	if gw.server == nil {
		return nil
	}
	return gw.server.Shutdown(ctx)
}

func (gw *WebSocketGateway) handle(w http.ResponseWriter, r *http.Request) {
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer wsConn.CloseNow()

	ctx := r.Context()

	// Per-connection response reader for tool callbacks.
	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()

	writer := wsWriter(readCtx, wsConn)

	// Register with the namespace hub so this connection receives
	// all broadcast events. Remove on disconnect.
	gw.Handler.Hub().Subscribe(writer)
	defer gw.Handler.Hub().Unsubscribe(writer)

	// Send welcome frame immediately on connect.
	gw.sendWelcome(readCtx, writer, responseReader)

	for {
		_, data, err := wsConn.Read(readCtx)
		if err != nil {
			return
		}

		frameData := make([]byte, len(data))
		copy(frameData, data)

		// Parse based on type field.
		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(frameData, &base); err != nil {
			if writer.Write(FrameResponse{Type: "error", Error: err.Error()}) != nil {
				return
			}
			continue
		}

		switch base.Type {
		case "resp":
			// Client response to a server request (e.g. Neovim Lua eval).
			var resp struct {
				RequestID string          `json:"request_id"`
				Result    json.RawMessage `json:"result"`
				ReqError  string          `json:"error"`
			}
			if err := json.Unmarshal(frameData, &resp); err != nil {
				if writer.Write(FrameResponse{Type: "error", Error: "invalid resp frame: " + err.Error()}) != nil {
					return
				}
				continue
			}
			responseReader.Deliver(&event.ClientResponse{
				RequestID: resp.RequestID,
				Result:    resp.Result,
				Error:     resp.ReqError,
			})
			continue

		case "cancel":
			var cancelReq struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(frameData, &cancelReq); err != nil {
				if writer.Write(FrameResponse{Type: "error", Error: "invalid cancel frame: " + err.Error()}) != nil {
					return
				}
				continue
			}
			if !gw.Handler.CancelSession(cancelReq.SessionID) {
				if writer.Write(FrameResponse{Type: "error", SessionID: cancelReq.SessionID, Error: "no active turn to cancel"}) != nil {
					return
				}
				continue
			}
			// Acknowledge immediately. The turn's actual done frame
			// (with stats) arrives later via the hub broadcast.
			if writer.Write(FrameResponse{
				Type:      "done",
				SessionID: cancelReq.SessionID,
				Cancelled: true,
			}) != nil {
				return
			}
			continue

		case "chat", "cmd", "":
			// "chat" = normal chat turn, "cmd" = JSON command, "" = legacy (treat as chat).
			go func(data []byte) {
				var req FrameRequest
				if err := json.Unmarshal(data, &req); err != nil {
					if writer.Write(FrameResponse{Type: "error", Error: err.Error()}) != nil {
						readCancel()
					}
					return
				}
				gw.Handler.HandleRequest(readCtx, req, writer, responseReader)
			}(frameData)

		default:
			if writer.Write(FrameResponse{Type: "error", Error: fmt.Sprintf("unknown frame type: %q", base.Type)}) != nil {
				return
			}
		}
	}
}

// sendWelcome sends a "welcome" frame on connect with the sessions list
// at the top level, and auto-subscribes to the best session.
func (gw *WebSocketGateway) sendWelcome(ctx context.Context, w frameWriter, responseReader *InProcessResponseReader) {
	nsCtx := event.ContextWithNamespace(ctx, gw.Handler.Namespace)

	// Get all sessions in this namespace.
	sessions := []*agent.Session{}
	if gw.Handler.Conv != nil {
		var err error
		sessions, err = gw.Handler.Conv.Sessions().List(nsCtx, gw.Handler.Namespace)
		if err != nil {
			// Non-fatal — send empty session list.
			sessions = nil
		}
	}

	// Build session maps for top-level sessions field.
	sessionMaps := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		m := s.Metadata()
		m["in_flight"] = gw.Handler.Turns().Get(s.SessionID()) != nil
		sessionMaps = append(sessionMaps, m)
	}

	// Send welcome with sessions at top level (always present, even if empty).
	w.Write(FrameResponse{
		Type:     "welcome",
		Sessions: sessionMaps,
	})

	// Auto-subscribe to the best session:
	// 1. In-flight session (most recent)
	// 2. Latest session
	// 3. Nothing
	var bestSession *agent.Session
	for _, s := range sessions {
		if gw.Handler.Turns().Get(s.SessionID()) != nil {
			bestSession = s
			break
		}
	}
	if bestSession == nil && len(sessions) > 0 {
		bestSession = sessions[len(sessions)-1]
	}

	if bestSession != nil {
		// Issue a get_session to auto-subscribe.
		nsCtx := event.ContextWithNamespace(ctx, gw.Handler.Namespace)
		params, _ := json.Marshal(map[string]string{"id": bestSession.SessionID()})
		cmdRes := gw.Handler.Cmd.ExecuteJSON(nsCtx, bestSession.SessionID(), "get_session", json.RawMessage(params))

		// Check in_flight status for the session before any subscription.
		isInFlight := gw.Handler.Turns().Get(bestSession.SessionID()) != nil
		cmdRes.InFlight = isInFlight

		// Send the session data as a "session" event with top-level fields.
		if cmdRes.Session != nil {
			sessionMap := cmdRes.Session.Metadata()
			fr := FrameResponse{
				Type:      "session",
				SessionID: bestSession.SessionID(),
				Event:     "get_session",
				Session:   sessionMap,
			}
			if msgs := cmdRes.Session.Messages(); len(msgs) > 0 {
				fr.Events = messagesToEvents(msgs)
				// Append "done" terminal event only if the session is
				// NOT in-flight. If it IS in-flight, live events will
				// arrive via hub broadcast.
				if !isInFlight {
					fr.Events = append(fr.Events, map[string]any{"type": "done"})
				}
			}
			w.Write(fr)
		}

		// No need to subscribe to the active turn — all turn events
		// are broadcast to all hub subscribers. The connection was
		// already registered with the hub at the top of handle().
	}
}

// wsWriter adapts a websocket.Conn to frameWriter.
func wsWriter(ctx context.Context, wsConn *websocket.Conn) frameWriter {
	return &wsSyncWriter{ctx: ctx, conn: wsConn}
}

// wsSyncWriter wraps a WebSocket connection with a mutex for safe
// concurrent writes from the main loop and request goroutines.
type wsSyncWriter struct {
	mu   sync.Mutex
	ctx  context.Context
	conn *websocket.Conn
}

func (w *wsSyncWriter) Write(v FrameResponse) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.conn.Write(w.ctx, websocket.MessageText, b)
}

func (w *wsSyncWriter) ConnKey() string {
	return fmt.Sprintf("ws:%p", w.conn)
}

// HandleWSConnection exposes the WebSocket handling logic for reuse by
// other components (e.g. the embedded webfront server on a shared port).
func (gw *WebSocketGateway) HandleWSConnection(w http.ResponseWriter, r *http.Request) {
	gw.handle(w, r)
}

var _ Gateway = (*WebSocketGateway)(nil)
