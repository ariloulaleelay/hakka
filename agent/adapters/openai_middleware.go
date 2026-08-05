package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ctxKey string

const (
	ctxExtraPerReq   ctxKey = "extra_per_req"
	ctxSessionID     ctxKey = "session_id"
	ctxCapturedUsage ctxKey = "captured_usage"
)

func ContextWithCapturedUsage(ctx context.Context, ptr *RawUsage) context.Context {
	return context.WithValue(ctx, ctxCapturedUsage, ptr)
}

// instrumentedTransport wraps RoundTripper to inject extra body fields,
// dump debug payloads, and capture usage from responses.
type instrumentedTransport struct {
	base     http.RoundTripper
	extra    map[string]any
	debugDir string
}

type usageCaptureBody struct {
	io.ReadCloser
	raw       *RawUsage
	remainder []byte
}

func (b *usageCaptureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		data := append(b.remainder, p[:n]...)
		b.remainder = nil
		for {
			idx := bytes.IndexByte(data, '\n')
			if idx < 0 {
				b.remainder = append(b.remainder[:0], data...)
				break
			}
			line := string(data[:idx])
			data = data[idx+1:]
			if strings.HasPrefix(line, "data: ") {
				raw := extractRawUsage([]byte(strings.TrimSpace(line[6:])))
				if raw.totalTokens > 0 || raw.cost > 0 || raw.promptCacheHit > 0 || raw.promptCacheMiss > 0 {
					*b.raw = raw
				}
			}
		}
	}
	return n, err
}

func (t *instrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only inject extra body fields for requests that actually carry JSON
	// bodies (POST/PUT/PATCH). GET/HEAD requests (e.g. quota fetch) pass
	// through unchanged — injecting a body on a GET is malformed and
	// causes proxies like CloudFront to reject them with 403.
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		resp, err := t.base.RoundTrip(req)
		return t.handleResponse(req, resp, err)
	}

	perReqExtra, _ := req.Context().Value(ctxExtraPerReq).(map[string]any)
	sessionID, _ := req.Context().Value(ctxSessionID).(string)
	merged := mergeExtra(t.extra, perReqExtra, sessionID)

	var body []byte
	var err error
	if req.Body != nil {
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
	}

	if len(merged) == 0 && t.debugDir == "" {
		req.Body = io.NopCloser(bytes.NewReader(body))
		resp, err := t.base.RoundTrip(req)
		return t.handleResponse(req, resp, err)
	}

	var newBody []byte
	if len(merged) > 0 {
		var bodyMap map[string]any
		if len(body) > 0 {
			if err := json.Unmarshal(body, &bodyMap); err != nil {
				return nil, err
			}
		} else {
			bodyMap = make(map[string]any)
		}
		for k, v := range merged {
			bodyMap[k] = v
		}
		newBody, err = json.Marshal(bodyMap)
		if err != nil {
			return nil, err
		}
	} else {
		newBody = body
	}

	if t.debugDir != "" {
		n := reqCounter.Add(1)
		ts := time.Now().UnixNano()
		dumpPath := filepath.Join(t.debugDir, fmt.Sprintf("openai_wire_%d_%d_body.json", ts, n))
		_ = os.MkdirAll(t.debugDir, 0755)
		_ = os.WriteFile(dumpPath, newBody, 0644)
	}

	newReq := req.Clone(req.Context())
	newReq.Body = io.NopCloser(bytes.NewReader(newBody))
	newReq.ContentLength = int64(len(newBody))
	resp, err := t.base.RoundTrip(newReq)
	return t.handleResponse(newReq, resp, err)
}

func (t *instrumentedTransport) handleResponse(req *http.Request, resp *http.Response, err error) (*http.Response, error) {
	if err != nil || resp == nil {
		return resp, err
	}
	rawPtr, _ := req.Context().Value(ctxCapturedUsage).(*RawUsage)
	if rawPtr == nil {
		return resp, nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body = &usageCaptureBody{ReadCloser: resp.Body, raw: rawPtr}
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	*rawPtr = extractRawUsage(body)

	newResp := *resp
	newResp.Body = io.NopCloser(bytes.NewReader(body))
	return &newResp, nil
}

func mergeExtra(static, perReq map[string]any, sessionID string) map[string]any {
	result := make(map[string]any, len(static)+len(perReq))
	for k, v := range static {
		result[k] = resolvePlaceholder(v, sessionID)
	}
	for k, v := range perReq {
		result[k] = v
	}
	return result
}

func resolvePlaceholder(v any, sessionID string) any {
	if s, ok := v.(string); ok {
		return strings.ReplaceAll(s, "$session_id", sessionID)
	}
	return v
}

func ContextWithExtras(ctx context.Context, sessionID string, perReqExtra map[string]any) context.Context {
	if sessionID != "" {
		ctx = context.WithValue(ctx, ctxSessionID, sessionID)
	}
	if len(perReqExtra) > 0 {
		ctx = context.WithValue(ctx, ctxExtraPerReq, perReqExtra)
	}
	return ctx
}

func WrapTransport(base http.RoundTripper, extra map[string]any, debugDir string) http.RoundTripper {
	return &instrumentedTransport{base: base, extra: extra, debugDir: debugDir}
}
