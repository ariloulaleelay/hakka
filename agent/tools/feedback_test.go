package tools

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

func TestFeedbackSubmitsToURL(t *testing.T) {
	var received struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Contact     string `json:"contact"`
	}
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		close(done)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	SetFeedbackURL(srv.URL)
	tool := FeedbackTool()
	res := runPlain(t, tool.Handler, map[string]any{
		"title":       "Add dark mode",
		"description": "Please add dark mode support",
		"contact":     "@user",
	})

	<-done // wait for server to receive

	if received.ID == "" {
		t.Fatal("expected a non-empty ID")
	}
	if !strings.Contains(received.ID, "-") {
		t.Fatal("expected UUID format (with dashes), got:", received.ID)
	}
	if received.Title != "Add dark mode" {
		t.Fatalf("expected title 'Add dark mode', got %q", received.Title)
	}
	if received.Description != "Please add dark mode support" {
		t.Fatalf("expected description, got %q", received.Description)
	}
	if received.Contact != "@user" {
		t.Fatalf("expected contact '@user', got %q", received.Contact)
	}

	if !strings.Contains(res, received.ID) {
		t.Fatalf("expected success message to contain ID %q, got: %q", received.ID, res)
	}
	if !strings.Contains(res, "Feedback submitted") {
		t.Fatalf("expected 'Feedback submitted' in result, got: %q", res)
	}
}

func TestFeedbackWithoutContact(t *testing.T) {
	var received struct {
		Contact string `json:"contact"`
	}
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		close(done)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	SetFeedbackURL(srv.URL)
	tool := FeedbackTool()
	_ = runPlain(t, tool.Handler, map[string]any{
		"title":       "Bug report",
		"description": "Something is broken",
	})

	<-done

	if received.Contact != "" {
		t.Fatalf("expected empty contact, got %q", received.Contact)
	}
}

func TestFeedbackServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	SetFeedbackURL(srv.URL)
	tool := FeedbackTool()
	errMsg := runErr(t, tool.Handler, map[string]any{
		"title":       "test",
		"description": "test",
	})
	if !strings.Contains(errMsg, "500") && !strings.Contains(errMsg, "unexpected status") {
		t.Fatalf("expected error about server failure, got: %q", errMsg)
	}
}

func TestFeedbackMissingTitle(t *testing.T) {
	SetFeedbackURL("http://example.com")
	tool := FeedbackTool()
	errMsg := runErr(t, tool.Handler, map[string]any{
		"description": "test",
	})
	if !strings.Contains(errMsg, "title") {
		t.Fatalf("expected error about missing title, got: %q", errMsg)
	}
}

func TestFeedbackMissingDescription(t *testing.T) {
	SetFeedbackURL("http://example.com")
	tool := FeedbackTool()
	errMsg := runErr(t, tool.Handler, map[string]any{
		"title": "test",
	})
	if !strings.Contains(errMsg, "description") {
		t.Fatalf("expected error about missing description, got: %q", errMsg)
	}
}

func TestFeedbackExecSnippet(t *testing.T) {
	SetFeedbackURL("http://example.com")
	tool := FeedbackTool()
	s := snippetFor(t, tool, map[string]any{
		"title": "Add feature",
	})
	if !strings.Contains(s, "Add feature") {
		t.Fatalf("expected title in snippet, got: %q", s)
	}
}

func TestFeedbackRegisterAllIncludesFeedback(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterAll(reg)
	_, ok := reg.Get("feedback")
	if !ok {
		t.Fatal("expected 'feedback' tool to be registered by RegisterAll")
	}
}

func TestFeedbackHasAllTag(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterAll(reg)
	schemas := reg.SchemasByTags("all")
	found := false
	for _, s := range schemas {
		if s.Name == "feedback" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected feedback tool to have the 'all' tag")
	}
}

func TestFeedbackValidUUID(t *testing.T) {
	var received struct {
		ID string `json:"id"`
	}
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		close(done)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	SetFeedbackURL(srv.URL)
	tool := FeedbackTool()
	_ = runPlain(t, tool.Handler, map[string]any{
		"title":       "test",
		"description": "test",
	})

	<-done

	parts := strings.Split(received.ID, "-")
	if len(parts) != 5 {
		t.Fatalf("expected 5 UUID parts, got %d in %q", len(parts), received.ID)
	}
	if len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("invalid UUID segment lengths in %q", received.ID)
	}
}
