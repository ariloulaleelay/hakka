package gateways

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// syncFrameWriter wraps a frameWriter with a mutex to protect against
// concurrent writes from the scanner loop (error/cancel/init frames) and
// the request processing goroutine (event frames). This is needed only
// because the TCP gateway processes requests in a separate goroutine so
// the scanner loop can read vim response frames — creating two goroutines
// that may write to the same connection concurrently.
type syncFrameWriter struct {
	mu sync.Mutex
	w  frameWriter
}

func (s *syncFrameWriter) Write(v FrameResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(v)
}

// TCPGateway speaks newline-delimited JSON. Designed for the Neovim
// client: one request per line, one response per line.
type TCPGateway struct {
	Handler *TurnHandler
	Addr    string

	listener  net.Listener
	waitGroup sync.WaitGroup
	cancelCtx context.CancelFunc
}

func NewTCPGateway(conv *agent.Conversation, streamer *agent.StreamSession, cmd *commands.CommandProcessor, addr string) *TCPGateway {
	if addr == "" {
		addr = "127.0.0.1:9876"
	}
	return &TCPGateway{
		Handler: NewTurnHandler(conv, streamer, cmd, "tcp"),
		Addr:    addr,
	}
}

func (gw *TCPGateway) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", gw.Addr)
	if err != nil {
		return err
	}
	gw.listener = listener

	ctx, cancel := context.WithCancel(ctx)
	gw.cancelCtx = cancel

	gw.waitGroup.Add(1)
	go gw.acceptLoop(ctx, listener)
	return nil
}

func (gw *TCPGateway) Stop(_ context.Context) error {
	if gw.cancelCtx != nil {
		gw.cancelCtx()
	}
	if gw.listener != nil {
		_ = gw.listener.Close()
	}
	gw.waitGroup.Wait()
	return nil
}

func (gw *TCPGateway) acceptLoop(ctx context.Context, listener net.Listener) {
	defer gw.waitGroup.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		gw.waitGroup.Add(1)
		go func() {
			defer gw.waitGroup.Done()
			gw.handle(ctx, conn)
		}()
	}
}

// handle manages one TCP connection.
func (gw *TCPGateway) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	writer := bufio.NewWriter(conn)
	enc := json.NewEncoder(writer)
	rawFlush := writerFunc(func(v FrameResponse) error {
		if err := enc.Encode(v); err != nil {
			return err
		}
		return writer.Flush()
	})
	flush := &syncFrameWriter{w: rawFlush}

	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()

	for scanner.Scan() {
		line := make([]byte, len(scanner.Bytes()))
		copy(line, scanner.Bytes())

		// Peek at type to route frames
		var env struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Result    json.RawMessage `json:"result"`
			ReqError  string          `json:"error"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			if flush.Write(FrameResponse{Error: err.Error()}) != nil {
				return // client disconnected
			}
			continue
		}

		if env.Type == "response" && env.RequestID != "" {
			responseReader.Deliver(&event.ClientResponse{
				RequestID: env.RequestID,
				Result:    env.Result,
				Error:     env.ReqError,
			})
			continue
		}

		if env.Type == "cancel" {
			// Cancel request: look up session and abort its request.
			var cancelReq struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(line, &cancelReq); err != nil {
				if flush.Write(FrameResponse{Error: "invalid cancel frame: " + err.Error()}) != nil {
					return
				}
				continue
			}
			cancelled := gw.Handler.CancelSession(cancelReq.SessionID)
			if flush.Write(FrameResponse{
				Event:     "cancel",
				SessionID: cancelReq.SessionID,
				Data:      map[string]any{"cancelled": cancelled},
			}) != nil {
				return
			}
			continue
		}

		if env.Type == "init" {
			// Init handshake: resolve/create session, store CWD, reply
			// with metadata. The client sends this on UI open and after
			// session creation to establish the working directory and
			// learn the session ID and model name.
			//
			// When start=true, the server creates a fresh session with all
			// tools enabled — this replaces the two-round-trip pattern of
			// init → /start.
			var initReq struct {
				Cwd   string `json:"cwd"`
				Start bool   `json:"start"`
			}
			if err := json.Unmarshal(line, &initReq); err != nil {
				if flush.Write(FrameResponse{Error: "invalid init frame: " + err.Error()}) != nil {
					return
				}
				continue
			}

			session := agent.NewSession(gw.Handler.Namespace, gw.Handler.Conv.Sessions.SystemPrompt)
			if initReq.Cwd != "" {
				session.ClientCWD = initReq.Cwd
			}
			if initReq.Start {
				// Enable all registered tools.
				if gw.Handler.Conv.Tools != nil {
					for _, schema := range gw.Handler.Conv.Tools.Schemas() {
						session.EnableTool(schema.Name)
					}
				}
			}

			// Persist the session with CWD and tool settings.
			if saveErr := gw.Handler.Conv.Sessions.Save(readCtx, gw.Handler.Namespace, session); saveErr != nil {
				if flush.Write(FrameResponse{Error: saveErr.Error()}) != nil {
					return
				}
				continue
			}

			if flush.Write(FrameResponse{
				Event:     "init",
				SessionID: session.ID,
				Done:      true,
				Data: map[string]any{
					"model": gw.Handler.Conv.SessionModel(session),
					"cwd":   session.ClientCWD,
				},
			}) != nil {
				return
			}
			continue
		}

	// Normal request — process in a separate goroutine so the read
	// loop stays available for incoming response frames (vim tools
	// need the scanner loop to read Neovim's response).
	go func(data []byte) {
		var req FrameRequest
		if err := json.Unmarshal(data, &req); err != nil {
			if flush.Write(FrameResponse{Error: err.Error()}) != nil {
				readCancel() // client disconnected, abort everything
			}
			return
		}
		gw.Handler.HandleRequest(readCtx, req, flush, responseReader)
	}(line)
	}
}

var _ Gateway = (*TCPGateway)(nil)
