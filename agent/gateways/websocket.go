package gateways

import (
	"context"
	"encoding/json"
	"errors"
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

func NewWebSocketGateway(conv *agent.Conversation, streamer *agent.StreamSession, cmd *commands.CommandProcessor, addr string) *WebSocketGateway {
	if addr == "" {
		addr = ":8765"
	}
	ns := conv.Namespace
	if ns == "" {
		ns = "ws"
	}
	return &WebSocketGateway{
		Handler: NewTurnHandler(conv, streamer, cmd, ns),
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
			if gw.Handler.Conv != nil && gw.Handler.Conv.Config.Logger != nil {
				gw.Handler.Conv.Config.Logger.Error("websocket serve error", "err", err)
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
		InsecureSkipVerify: true, // intended for local dev; harden as needed
	})
	if err != nil {
		return
	}
	defer wsConn.CloseNow()

	ctx := r.Context()

	// Per-connection response reader for vim tool callbacks.
	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()

	for {
		_, data, err := wsConn.Read(readCtx)
		if err != nil {
			return
		}

		frameData := make([]byte, len(data))
		copy(frameData, data)

		// Peek at type field for response routing
		var env struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Result    json.RawMessage `json:"result"`
			ReqError  string          `json:"error"`
		}
		if err := json.Unmarshal(frameData, &env); err != nil {
			writer := wsWriter(readCtx, wsConn)
			if writer.Write(FrameResponse{Error: err.Error()}) != nil {
				return // client disconnected
			}
			continue
		}

		// Route response frames to the waiting vim tool immediately
		if env.Type == "response" && env.RequestID != "" {
			responseReader.Deliver(&event.ClientResponse{
				RequestID: env.RequestID,
				Result:    env.Result,
				Error:     env.ReqError,
			})
			continue
		}

		// Handle cancel frames
		if env.Type == "cancel" {
			var cancelReq struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(frameData, &cancelReq); err != nil {
				writer := wsWriter(readCtx, wsConn)
				if writer.Write(FrameResponse{Error: "invalid cancel frame: " + err.Error()}) != nil {
					return
				}
				continue
			}
			cancelled := gw.Handler.CancelSession(cancelReq.SessionID)
			writer := wsWriter(readCtx, wsConn)
			if writer.Write(FrameResponse{
				Event:     "cancel",
				SessionID: cancelReq.SessionID,
				Data:      map[string]any{"cancelled": cancelled},
			}) != nil {
				return
			}
			continue
		}

		// Normal request — process in a separate goroutine so the read
		// loop stays available for incoming response frames.
		go func(data []byte) {
			writer := wsWriter(readCtx, wsConn)
			var req FrameRequest
			if err := json.Unmarshal(data, &req); err != nil {
				if writer.Write(FrameResponse{Error: err.Error()}) != nil {
					readCancel() // client disconnected, abort everything
				}
				return
			}
			gw.Handler.HandleRequest(readCtx, req, writer, responseReader)
		}(frameData)
	}
}

// wsWriter adapts a websocket.Conn to frameWriter. Returns a
// mutex-protected writer because the main loop and request goroutines
// both write to the same WebSocket connection concurrently.
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

var _ Gateway = (*WebSocketGateway)(nil)
