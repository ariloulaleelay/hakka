package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
	"github.com/ariloulaleelay/hakka/agent/gateways"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// vimTestClient implements event.ClientWriter — it records frames the tool
// sends to "Neovim" so tests can inspect them.
type vimTestClient struct {
	mu     sync.Mutex
	frames []event.Frame
}

func newVimTestClient() *vimTestClient {
	return &vimTestClient{}
}

func (c *vimTestClient) WriteFrame(fr event.Frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, fr)
	return nil
}

func (c *vimTestClient) sentFrames() []event.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]event.Frame, len(c.frames))
	copy(out, c.frames)
	return out
}

// vimTestBridge creates a context with a real InProcessResponseReader and a
// fake ClientWriter. Returns the context, the fake client (for inspecting
// sent frames), and the response reader (for delivering responses).
func vimTestBridge(t *testing.T) (context.Context, *vimTestClient, *gateways.InProcessResponseReader) {
	t.Helper()
	client := newVimTestClient()
	rr := gateways.NewInProcessResponseReader()
	ctx := event.ContextWithClient(context.Background(), client, rr)
	return ctx, client, rr
}

// runVim invokes the vim_run_command handler and returns the raw string
// result and any error.
func runVim(t *testing.T, ctx context.Context, args any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	return VimRunCommand().Handler(ctx, raw)
}

// awaitFrameAndRespond blocks until the client has sent a client_request frame,
// then delivers the given result via the response reader. Returns the
// request_id that was used.
func awaitFrameAndRespond(t *testing.T, client *vimTestClient, rr *gateways.InProcessResponseReader, result string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frames := client.sentFrames()
		if len(frames) > 0 {
			fr := frames[len(frames)-1]
			if fr.ClientReq != nil && fr.ClientReq.RequestID != "" {
				rr.Deliver(&event.ClientResponse{
					RequestID: fr.ClientReq.RequestID,
					Result:    json.RawMessage(result),
				})
				return fr.ClientReq.RequestID
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for client_request frame")
	return ""
}

// awaitFrameAndRespondErr is like awaitFrameAndRespond but delivers an error.
func awaitFrameAndRespondErr(t *testing.T, client *vimTestClient, rr *gateways.InProcessResponseReader, errMsg string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frames := client.sentFrames()
		if len(frames) > 0 {
			fr := frames[len(frames)-1]
			if fr.ClientReq != nil && fr.ClientReq.RequestID != "" {
				rr.Deliver(&event.ClientResponse{
					RequestID: fr.ClientReq.RequestID,
					Error:     errMsg,
				})
				return fr.ClientReq.RequestID
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for client_request frame")
	return ""
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestVimRunCommand_ReadBuffer(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	// Start the tool in a goroutine — it will block waiting for the response.
	type toolResult struct {
		result string
	}
	ch := make(chan toolResult, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `return vim.api.nvim_buf_get_lines(0, 0, -1, false)`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- toolResult{result: r}
	}()

	// Wait for the frame, then respond.
	awaitFrameAndRespond(t, client, rr, `["line1","line2","line3"]`)

	r := <-ch
	if !strings.Contains(r.result, `"line1"`) {
		t.Fatalf("expected line1 in result, got: %s", r.result)
	}
	if !strings.Contains(r.result, `"line2"`) {
		t.Fatalf("expected line2 in result, got: %s", r.result)
	}

	// Verify the correct request was sent
	frames := client.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 sent frame, got %d", len(frames))
	}
	if frames[0].Event != "client_request" {
		t.Fatalf("expected event=client_request, got %q", frames[0].Event)
	}
	if frames[0].ClientReq == nil {
		t.Fatal("expected ClientReq to be set")
	}
	if !strings.Contains(frames[0].ClientReq.Command, "nvim_buf_get_lines") {
		t.Fatalf("expected nvim_buf_get_lines in command, got %q", frames[0].ClientReq.Command)
	}
}

func TestVimRunCommand_EditBuffer(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `vim.api.nvim_buf_set_lines(0, 0, -1, false, {'replacement'}); return 'ok'`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `"ok"`)

	r := <-ch
	if !strings.Contains(r, `"ok"`) {
		t.Fatalf("expected ok in result, got: %s", r)
	}

	frames := client.sentFrames()
	if !strings.Contains(frames[0].ClientReq.Command, "nvim_buf_set_lines") {
		t.Fatalf("expected nvim_buf_set_lines, got %q", frames[0].ClientReq.Command)
	}
}

func TestVimRunCommand_GetFilePath(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `return vim.fn.expand('%:p')`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `"/path/to/file.go"`)

	r := <-ch
	if !strings.Contains(r, `/path/to/file.go`) {
		t.Fatalf("expected file path in result, got: %s", r)
	}
}

func TestVimRunCommand_GetFiletype(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `return vim.bo.filetype`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `"go"`)

	r := <-ch
	if !strings.Contains(r, `"go"`) {
		t.Fatalf("expected go in result, got: %s", r)
	}
}

func TestVimRunCommand_LineCount(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `local lines = vim.api.nvim_buf_get_lines(0, 0, -1, false); return #lines`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `142`)

	r := <-ch
	if !strings.Contains(r, `142`) {
		t.Fatalf("expected 142 in result, got: %s", r)
	}
}

func TestVimRunCommand_SaveBuffer(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `vim.cmd('write'); return 'saved'`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `"saved"`)

	r := <-ch
	if !strings.Contains(r, `"saved"`) {
		t.Fatalf("expected saved in result, got: %s", r)
	}
}

func TestVimRunCommand_ErrorResponse(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `return vim.api.non_existent()`,
		})
		if err != nil {
			ch <- "Error: " + err.Error()
		} else {
			ch <- r
		}
	}()

	awaitFrameAndRespondErr(t, client, rr, "E5108: Error executing lua ...")

	r := <-ch
	if !strings.Contains(r, "E5108") {
		t.Fatalf("expected E5108 error in result, got: %s", r)
	}
}

func TestVimRunCommand_NoClientInContext(t *testing.T) {
	ctx := context.Background()
	_, err := runVim(t, ctx, map[string]any{
		"command": `return 42`,
	})

	if err == nil || !strings.Contains(err.Error(), "not connected to a Neovim client") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestVimRunCommand_EmptyCommand(t *testing.T) {
	ctx, _, _ := vimTestBridge(t)
	_, err := runVim(t, ctx, map[string]any{
		"command": "",
	})

	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("expected 'command is required' error, got: %v", err)
	}
}

func TestVimRunCommand_ContextCancelled(t *testing.T) {
	client := newVimTestClient()
	rr := gateways.NewInProcessResponseReader()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = event.ContextWithClient(ctx, client, rr)
	cancel() // cancel before the tool runs

	_, err := runVim(t, ctx, map[string]any{
		"command": `return 42`,
	})

	if err == nil || !strings.Contains(err.Error(), "cancelled or timed out") {
		t.Fatalf("expected cancellation error, got: %v", err)
	}
}

func TestVimRunCommand_Timeout(t *testing.T) {
	// Client that never responds — the tool should time out via context deadline.
	client := newVimTestClient()
	rr := gateways.NewInProcessResponseReader()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	ctx = event.ContextWithClient(ctx, client, rr)

	_, err := runVim(t, ctx, map[string]any{
		"command": `return 42`,
	})

	if err == nil || !strings.Contains(err.Error(), "cancelled or timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestVimRunCommand_ExecSnippet(t *testing.T) {
	s := snippetFor(t, VimRunCommand(), map[string]any{
		"command": `return vim.api.nvim_buf_get_lines(0, 0, -1, false)`,
	})
	if !strings.Contains(s, "nvim_buf_get_lines") {
		t.Fatalf("expected command in snippet, got %q", s)
	}
}

func TestVimRunCommand_ExecSnippet_TruncatesLongCommand(t *testing.T) {
	longCmd := "return " + strings.Repeat("x", 100)
	s := snippetFor(t, VimRunCommand(), map[string]any{
		"command": longCmd,
	})
	// Server no longer truncates — full command is sent to the client
	if !strings.Contains(s, longCmd) {
		t.Fatalf("expected full command in snippet (client truncates), got %q", s)
	}
}

func TestVimRunCommand_ExecSnippet_EmptyCommand(t *testing.T) {
	s := snippetFor(t, VimRunCommand(), map[string]any{})
	if s != "" {
		t.Fatalf("expected empty snippet for empty args, got %q", s)
	}
}

func TestVimRunCommand_ReturnsNullResult(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `return nil`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `null`)

	r := <-ch
	if !strings.Contains(r, "null") {
		t.Fatalf("expected null in result, got: %s", r)
	}
}

func TestVimRunCommand_SendsRequestID(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := runVim(t, ctx, map[string]any{
			"command": `return 'hello'`,
		})
		if err != nil {
			t.Errorf("vim_run_command: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `"hello"`)
	<-ch

	frames := client.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if frames[0].ClientReq.RequestID == "" {
		t.Fatal("expected non-empty request_id")
	}
}

func TestVimRunCommand_NotRegistered(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterAll(reg, nil)
	schemas := reg.Schemas()

	for _, s := range schemas {
		if s.Name == "vim_run_command" {
			t.Fatal("vim_run_command should NOT be registered")
		}
	}
}

// TestVimRunCommand_Registered is removed — vim_run_command is intentionally
// disabled due to bugs. Use vim_list_buffers and vim_read_buffer instead.

// ---------------------------------------------------------------------------
// VimListBuffers tests
// ---------------------------------------------------------------------------

func TestVimListBuffers_Success(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := VimListBuffers().Handler(ctx, json.RawMessage("{}"))
		if err != nil {
			t.Errorf("vim_list_buffers: %v", err)
			return
		}
		ch <- r
	}()

	// Respond with buffer list
	awaitFrameAndRespond(t, client, rr, `[{"nr":1,"name":"/path/to/main.go","ft":"go"},{"nr":2,"name":"/path/to/util.go","ft":"go"}]`)

	r := <-ch
	if !strings.Contains(r, "main.go") {
		t.Fatalf("expected main.go in result, got: %s", r)
	}
	if !strings.Contains(r, "util.go") {
		t.Fatalf("expected util.go in result, got: %s", r)
	}
	if !strings.Contains(r, "buffer 1") {
		t.Fatalf("expected buffer 1 in result, got: %s", r)
	}

	// Verify the correct request was sent
	frames := client.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 sent frame, got %d", len(frames))
	}
	if frames[0].Event != "client_request" {
		t.Fatalf("expected event=client_request, got %q", frames[0].Event)
	}
	if frames[0].ClientReq == nil {
		t.Fatal("expected ClientReq to be set")
	}
	if !strings.Contains(frames[0].ClientReq.Command, "nvim_list_bufs") {
		t.Fatalf("expected nvim_list_bufs in command, got %q", frames[0].ClientReq.Command)
	}
}

func TestVimListBuffers_EmptyList(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := VimListBuffers().Handler(ctx, json.RawMessage("{}"))
		if err != nil {
			t.Errorf("vim_list_buffers: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `[]`)

	r := <-ch
	if !strings.Contains(r, "no open buffers") {
		t.Fatalf("expected 'no open buffers' message, got: %s", r)
	}
}

func TestVimListBuffers_NoClientInContext(t *testing.T) {
	ctx := context.Background()
	_, err := VimListBuffers().Handler(ctx, json.RawMessage("{}"))
	if err == nil || !strings.Contains(err.Error(), "not connected to a Neovim client") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestVimListBuffers_ContextCancelled(t *testing.T) {
	client := newVimTestClient()
	rr := gateways.NewInProcessResponseReader()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = event.ContextWithClient(ctx, client, rr)
	cancel()

	_, err := VimListBuffers().Handler(ctx, json.RawMessage("{}"))
	if err == nil || !strings.Contains(err.Error(), "cancelled or timed out") {
		t.Fatalf("expected cancellation error, got: %v", err)
	}
}

func TestVimListBuffers_ExecSnippet(t *testing.T) {
	s := snippetFor(t, VimListBuffers(), map[string]any{})
	if !strings.Contains(s, "list_buffers") {
		t.Fatalf("expected list_buffers in snippet, got %q", s)
	}
}

// ---------------------------------------------------------------------------
// VimReadBuffer tests
// ---------------------------------------------------------------------------

func TestVimReadBuffer_Success(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := VimReadBuffer().Handler(ctx, json.RawMessage(`{"bufnr": 1}`))
		if err != nil {
			t.Errorf("vim_read_buffer: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `["line one","line two","line three"]`)

	r := <-ch
	if !strings.Contains(r, "line one") {
		t.Fatalf("expected 'line one' in result, got: %s", r)
	}
	if !strings.Contains(r, "line two") {
		t.Fatalf("expected 'line two' in result, got: %s", r)
	}

	// Verify the correct request was sent
	frames := client.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 sent frame, got %d", len(frames))
	}
	if frames[0].Event != "client_request" {
		t.Fatalf("expected event=client_request, got %q", frames[0].Event)
	}
	if frames[0].ClientReq == nil {
		t.Fatal("expected ClientReq to be set")
	}
	if !strings.Contains(frames[0].ClientReq.Command, "nvim_buf_get_lines") {
		t.Fatalf("expected nvim_buf_get_lines in command, got %q", frames[0].ClientReq.Command)
	}
}

func TestVimReadBuffer_WithBufnr(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := VimReadBuffer().Handler(ctx, json.RawMessage(`{"bufnr": 3}`))
		if err != nil {
			t.Errorf("vim_read_buffer: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `["header","body","footer"]`)

	r := <-ch
	if !strings.Contains(r, "header") {
		t.Fatalf("expected 'header' in result, got: %s", r)
	}

	// Verify the bufnr 3 was in the command
	frames := client.sentFrames()
	if !strings.Contains(frames[0].ClientReq.Command, "bufnr=3") && !strings.Contains(frames[0].ClientReq.Command, "3, 0, -1") {
		t.Fatalf("expected command to reference bufnr 3, got: %q", frames[0].ClientReq.Command)
	}
}

func TestVimReadBuffer_EmptyBuffer(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	ch := make(chan string, 1)
	go func() {
		r, err := VimReadBuffer().Handler(ctx, json.RawMessage(`{"bufnr": 1}`))
		if err != nil {
			t.Errorf("vim_read_buffer: %v", err)
			return
		}
		ch <- r
	}()

	awaitFrameAndRespond(t, client, rr, `[]`)

	r := <-ch
	if !strings.Contains(r, "buffer is empty") {
		t.Fatalf("expected 'buffer is empty' message, got: %s", r)
	}
}

func TestVimReadBuffer_MissingBufnr(t *testing.T) {
	ctx, _, _ := vimTestBridge(t)
	_, err := VimReadBuffer().Handler(ctx, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "bufnr") {
		t.Fatalf("expected bufnr error, got: %v", err)
	}
}

func TestVimReadBuffer_NoClientInContext(t *testing.T) {
	ctx := context.Background()
	_, err := VimReadBuffer().Handler(ctx, json.RawMessage(`{"bufnr": 1}`))
	if err == nil || !strings.Contains(err.Error(), "not connected to a Neovim client") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestVimReadBuffer_ContextCancelled(t *testing.T) {
	client := newVimTestClient()
	rr := gateways.NewInProcessResponseReader()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = event.ContextWithClient(ctx, client, rr)
	cancel()

	_, err := VimReadBuffer().Handler(ctx, json.RawMessage(`{"bufnr": 1}`))
	if err == nil || !strings.Contains(err.Error(), "cancelled or timed out") {
		t.Fatalf("expected cancellation error, got: %v", err)
	}
}

func TestVimReadBuffer_ExecSnippet(t *testing.T) {
	s := snippetFor(t, VimReadBuffer(), map[string]any{"bufnr": 5})
	if !strings.Contains(s, "bufnr=5") {
		t.Fatalf("expected bufnr=5 in snippet, got %q", s)
	}
}

func TestVimReadBuffer_ExecSnippet_EmptyArgs(t *testing.T) {
	s := snippetFor(t, VimReadBuffer(), map[string]any{})
	if s != "" {
		t.Fatalf("expected empty snippet for empty args, got %q", s)
	}
}

// ---------------------------------------------------------------------------
// Updated registration test
// ---------------------------------------------------------------------------

func TestVimToolsRegistered(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterAll(reg, nil)
	schemas := reg.Schemas()

	foundList := false
	foundRead := false
	for _, s := range schemas {
		if s.Name == "vim_list_buffers" {
			foundList = true
		}
		if s.Name == "vim_read_buffer" {
			foundRead = true
		}
	}
	if !foundList {
		t.Fatal("vim_list_buffers not found in registered tools")
	}
	if !foundRead {
		t.Fatal("vim_read_buffer not found in registered tools")
	}
}

func TestVimListBuffers_MultipleConcurrentCalls(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	const n = 3
	results := make([]chan string, n)
	for i := 0; i < n; i++ {
		results[i] = make(chan string, 1)
		go func(idx int) {
			r, err := VimListBuffers().Handler(ctx, json.RawMessage("{}"))
			if err != nil {
				t.Errorf("vim_list_buffers: %v", err)
				return
			}
			results[idx] <- r
		}(i)
	}

	// Wait for all n frames to arrive
	deadline := time.Now().Add(5 * time.Second)
	for len(client.sentFrames()) < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if len(client.sentFrames()) < n {
		t.Fatalf("expected %d frames, got %d", n, len(client.sentFrames()))
	}

	// Deliver responses
	for _, fr := range client.sentFrames() {
		if fr.ClientReq != nil {
			rr.Deliver(&event.ClientResponse{
				RequestID: fr.ClientReq.RequestID,
				Result:    json.RawMessage(`[]`),
			})
		}
	}

	// All goroutines should now complete
	for i := 0; i < n; i++ {
		select {
		case r := <-results[i]:
			if !strings.Contains(r, "no open buffers") {
				t.Fatalf("call %d: expected 'no open buffers', got: %s", i, r)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d: timed out waiting for result", i)
		}
	}
}

func TestVimReadBuffer_MultipleConcurrentCalls(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	const n = 3
	results := make([]chan string, n)
	for i := 0; i < n; i++ {
		results[i] = make(chan string, 1)
		go func(idx int) {
			r, err := VimReadBuffer().Handler(ctx, json.RawMessage(`{"bufnr": 1}`))
			if err != nil {
				t.Errorf("vim_read_buffer: %v", err)
				return
			}
			results[idx] <- r
		}(i)
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(client.sentFrames()) < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if len(client.sentFrames()) < n {
		t.Fatalf("expected %d frames, got %d", n, len(client.sentFrames()))
	}

	for _, fr := range client.sentFrames() {
		if fr.ClientReq != nil {
			rr.Deliver(&event.ClientResponse{
				RequestID: fr.ClientReq.RequestID,
				Result:    json.RawMessage(`["line"]`),
			})
		}
	}

	for i := 0; i < n; i++ {
		select {
		case r := <-results[i]:
			if !strings.Contains(r, "line") {
				t.Fatalf("call %d: expected 'line' in result, got: %s", i, r)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d: timed out waiting for result", i)
		}
	}
}

func TestVimRunCommand_MultipleConcurrentCalls(t *testing.T) {
	ctx, client, rr := vimTestBridge(t)

	const n = 5
	results := make([]chan string, n)
	cmds := make([]string, n)
	for i := 0; i < n; i++ {
		results[i] = make(chan string, 1)
		letter := string(rune('A' + i))
		cmds[i] = "return " + letter
		go func(idx int, cmd string) {
			r, err := runVim(t, ctx, map[string]any{
				"command": cmd,
			})
			if err != nil {
				t.Errorf("vim_run_command: %v", err)
				return
			}
			results[idx] <- r
		}(i, cmds[i])
	}

	// Wait for all n frames to arrive, then deliver responses to all
	// pending request IDs.
	deadline := time.Now().Add(5 * time.Second)
	for len(client.sentFrames()) < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if len(client.sentFrames()) < n {
		t.Fatalf("expected %d frames, got %d", n, len(client.sentFrames()))
	}

	// Deliver a response to every request_id we received
	for _, fr := range client.sentFrames() {
		if fr.ClientReq != nil {
			rr.Deliver(&event.ClientResponse{
				RequestID: fr.ClientReq.RequestID,
				Result:    json.RawMessage(`"ok"`),
			})
		}
	}

	// All goroutines should now complete
	for i := 0; i < n; i++ {
		select {
		case r := <-results[i]:
			if !strings.Contains(r, `"ok"`) {
				t.Fatalf("call %d: expected ok in result, got: %s", i, r)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d: timed out waiting for result", i)
		}
	}
}
