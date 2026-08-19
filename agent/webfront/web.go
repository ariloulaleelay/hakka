// Package webfront provides an embedded HTTP server that serves the
// hakka-webfront SPA (built from ../hakka-webfront) alongside a WebSocket
// endpoint (/ws) on the same port. This makes hakka a self-contained
// server+client: a single binary, a single port, no external dependencies.
//
// # The webfront-dist directory is copied from externa hakka-webfront
//
// This is done automatically by the Makefile before `go build`.
package webfront

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/gateways"
)

//go:embed webfront-dist/*
var embeddedFiles embed.FS

// webfrontFileSystem adapts embed.FS to http.FileSystem, rewriting paths
// to strip the "webfront-dist" prefix added by //go:embed.
type webfrontFileSystem struct {
	inner embed.FS
}

func (w webfrontFileSystem) Open(name string) (fs.File, error) {
	name = strings.TrimPrefix(name, "/")
	name = path.Join("webfront-dist", name)
	return w.inner.Open(name)
}

// Gateway serves the hakka-webfront SPA and a WebSocket endpoint on a
// single HTTP port. It implements gateways.Gateway.
type Gateway struct {
	addr      string
	handler   *gateways.TurnHandler
	server    *http.Server
	fileStore *FileStore
}

// New creates a Gateway. If addr is empty, Start is a no-op (disabled).
// The turnHandler is shared with the standalone WebSocket gateway so
// sessions and turn tracking are consistent across all transports.
func New(addr string, conv *agent.Conversation, cmd *commands.CommandProcessor) *Gateway {
	ns := conv.Namespace()
	if ns == "" {
		ns = "default"
	}
	return &Gateway{
		addr:    addr,
		handler: gateways.NewTurnHandler(conv, cmd, ns),
	}
}

// SetHub replaces the turn handler's namespace hub with a shared
// instance. This must be called before Start. It enables all transports
// for the same namespace (e.g. standalone WS + webfront) to share
// event broadcasts, turn tracking, and in_flight status.
func (gw *Gateway) SetHub(hub *gateways.NamespaceHub) {
	gw.handler.SetHub(hub)
}

// SetFileStore enables the per-session web file endpoints
// (GET/POST/DELETE /session/{session_id}/file/{path...}). Must be called
// before Start. When Sessions/Namespace are zero, they default to the
// gateway conversation's session manager and namespace.
func (gw *Gateway) SetFileStore(cfg FileStoreConfig) {
	if cfg.Sessions == nil && gw.handler != nil && gw.handler.Conv != nil {
		cfg.Sessions = gw.handler.Conv.Sessions()
	}
	if cfg.Namespace == "" && gw.handler != nil {
		cfg.Namespace = gw.handler.Namespace
	}
	gw.fileStore = NewFileStore(cfg)
}

// buildMux assembles the HTTP routes: file endpoints (when configured),
// the WebSocket endpoint, and the static SPA catch-all.
func (gw *Gateway) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Per-session sandboxed web file sharing (upload/download/delete).
	if gw.fileStore != nil {
		mux.HandleFunc("GET /session/{session_id}/file/{path...}", gw.fileStore.HandleDownload)
		mux.HandleFunc("POST /session/{session_id}/file/{path...}", gw.fileStore.HandleUpload)
		mux.HandleFunc("DELETE /session/{session_id}/file/{path...}", gw.fileStore.HandleDelete)
	}

	// WebSocket endpoint — reuse the same handler logic as the
	// standalone WebSocketGateway.
	wsGw := &gateways.WebSocketGateway{
		Handler: gw.handler,
		Addr:    gw.addr,
	}
	mux.HandleFunc("/ws", wsGw.HandleWSConnection)

	// Static SPA — serve embedded webfront files with ETag + gzip.
	fsys := webfrontFileSystem{inner: embeddedFiles}
	fileServer := http.FileServer(http.FS(fsys))
	mux.Handle("/", etagHandler(fsys, gzipHandler(fileServer)))
	return mux
}

func (gw *Gateway) Start(ctx context.Context) error {
	if gw.addr == "" {
		return nil
	}

	mux := gw.buildMux()

	gw.server = &http.Server{
		Addr:              gw.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", gw.addr)
	if err != nil {
		return fmt.Errorf("webfront listen %s: %w", gw.addr, err)
	}

	go func() {
		if err := gw.server.Serve(listener); err != nil {
			slog.Error("webfront server error", "addr", gw.addr, "err", err)
		}
	}()

	slog.Info("webfront serving SPA + WebSocket", "addr", gw.addr)
	return nil
}

func (gw *Gateway) Stop(ctx context.Context) error {
	if gw.server == nil {
		return nil
	}
	return gw.server.Shutdown(ctx)
}

// Ensure Gateway implements gateways.Gateway.
var _ gateways.Gateway = (*Gateway)(nil)

func acceptsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}
