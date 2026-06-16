package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/you/hakka/agent"
)

type httpGetArgs struct {
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	MaxBytes int               `json:"max_bytes"`
}

// HTTPGet fetches a URL with GET and returns status, headers and body.
func HTTPGet() agent.Tool {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "http_get",
			Description: "Perform an HTTP GET and return status, headers, and body (truncated).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url":       map[string]any{"type": "string"},
					"headers":   map[string]any{"type": "object", "description": "Optional extra request headers."},
					"max_bytes": map[string]any{"type": "integer", "description": "Max body bytes to include (default 64KB)."},
				},
				"required": []string{"url"},
			},
		},
		Tags: []string{"network", "all"},
		ExecSnippet: func(args json.RawMessage) string {
			var params struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(args, &params); err != nil || params.URL == "" {
				return ""
			}
			return `url="` + params.URL + `"`
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args httpGetArgs
			if err := unmarshalToolArgs(raw, "http_get", &args); err != nil {
				return "", err
			}
			if args.MaxBytes <= 0 {
				args.MaxBytes = 64 * 1024
			}
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
			if err != nil {
				return "", err
			}
			for key, value := range args.Headers {
				httpReq.Header.Set(key, value)
			}
			resp, err := httpClient.Do(httpReq)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(args.MaxBytes)+1))
			truncated := len(body) > args.MaxBytes
			if truncated {
				body = body[:args.MaxBytes]
			}
			headers := map[string]string{}
			for key, values := range resp.Header {
				if len(values) > 0 {
					headers[key] = values[0]
				}
			}
			out, err := json.Marshal(map[string]any{
				"status":    resp.StatusCode,
				"headers":   headers,
				"body":      string(body),
				"truncated": truncated,
			})
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	}
}
