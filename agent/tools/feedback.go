package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ariloulaleelay/hakka/agent"
)

// DefaultFeedbackURL is the built-in endpoint for feedback submissions.
// Users can override it via SetFeedbackURL or config.
const DefaultFeedbackURL = "https://feedback.hakka.su/"

var (
	feedbackMu   sync.RWMutex
	feedbackURL  = DefaultFeedbackURL
	feedbackOnce sync.Once
	feedbackHTTP *http.Client
)

func feedbackClient() *http.Client {
	feedbackOnce.Do(func() {
		feedbackHTTP = &http.Client{Timeout: 10 * time.Second}
	})
	return feedbackHTTP
}

// SetFeedbackURL overrides the default feedback endpoint URL.
// This is called from main.go when config provides a custom URL.
func SetFeedbackURL(url string) {
	feedbackMu.Lock()
	defer feedbackMu.Unlock()
	if url != "" {
		feedbackURL = url
	}
}

// getFeedbackURL returns the current feedback endpoint URL.
func getFeedbackURL() string {
	feedbackMu.RLock()
	defer feedbackMu.RUnlock()
	return feedbackURL
}

type feedbackArgs struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Contact     string `json:"contact,omitempty"`
}

// FeedbackTool returns the feedback submission tool that sends feature
// requests and bug reports to a configurable HTTP endpoint.
//
// The tool generates a unique anonymous ID (UUID v4) for each submission,
// so the user can reference it later without revealing their identity.
func FeedbackTool() agent.Tool {
	return NewTool("feedback",
		"Submit anonymous feedback — a feature request or a bug report. "+
			"A unique anonymous ID is generated automatically and included in the submission. "+
			"Before submitting, help the user structure their feedback: clarify what the problem or request is, "+
			"why it's needed, and any relevant context. For bugs, ask about steps to reproduce "+
			"and expected vs actual behavior. "+
			"Be thorough and structured — ask clarifying questions to make the feedback useful. "+
			"The more detail the user provides, the better the report will be.").
		StringParam("title", "Short summary of the feedback (feature request or bug report)", true).
		StringParam("description", "Full details — for bugs include steps to reproduce, expected vs actual behavior; for features describe the use case and why it's needed", true).
		StringParam("contact", "Optional contact information (email, Telegram handle, etc.) so the team can follow up", false).
		Tags("utility", "all").
		ExecSnippetField("title").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args feedbackArgs
			if err := unmarshalToolArgs(raw, "feedback", &args); err != nil {
				return "", err
			}
			if strings.TrimSpace(args.Title) == "" {
				return "", fmt.Errorf("feedback: title is required")
			}
			if strings.TrimSpace(args.Description) == "" {
				return "", fmt.Errorf("feedback: description is required")
			}

			id := uuid.New().String()

			payload := map[string]any{
				"id":          id,
				"title":       args.Title,
				"description": args.Description,
				"source":      "hakka",
			}
			if args.Contact != "" {
				payload["contact"] = args.Contact
			}

			body, err := json.Marshal(payload)
			if err != nil {
				return "", fmt.Errorf("failed to encode feedback: %w", err)
			}

			url := getFeedbackURL()
			resp, err := feedbackClient().Post(url, "application/json", bytes.NewReader(body))
			if err != nil {
				return "", fmt.Errorf("failed to submit feedback: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return "", fmt.Errorf("feedback submission failed (HTTP %d)", resp.StatusCode)
			}

			return fmt.Sprintf("✓ Feedback submitted (ID: %s)", id), nil
		}).
		Build()
}
