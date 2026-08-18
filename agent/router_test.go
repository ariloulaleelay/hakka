package agent

import (
	"context"
	"testing"
)

func newTestRouter() (*Router, *fakeAdapter, *fakeAdapter) {
	reg := NewRegistry()
	a := &fakeAdapter{}
	b := &fakeAdapter{}
	reg.Register("a", a)
	reg.Register("b", b)
	return NewRouter(reg), a, b
}

func TestRouterAdapterDefaults(t *testing.T) {
	r, a, _ := newTestRouter()
	sess := NewSession("testns", "sys")
	if r.Adapter(sess) != a {
		t.Fatal("expected default adapter for unbound session")
	}
	if r.Current(sess) != "a" {
		t.Fatalf("expected default name, got %q", r.Current(sess))
	}
}

func TestRouterBindAndResolve(t *testing.T) {
	r, _, b := newTestRouter()
	sess := NewSession("testns", "sys")
	if err := r.Bind(context.Background(), sess, "b"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if r.Adapter(sess) != b {
		t.Fatal("expected bound adapter b")
	}
	if r.Current(sess) != "b" {
		t.Fatalf("current: %q", r.Current(sess))
	}
}

func TestRouterBindUnknownModelIsRejected(t *testing.T) {
	r, _, _ := newTestRouter()
	sess := NewSession("testns", "sys")
	if err := r.Bind(context.Background(), sess, "nope"); err == nil {
		t.Fatal("expected error binding unknown model")
	}
	// Model must be untouched after a rejected bind.
	if r.Current(sess) != "a" {
		t.Fatalf("rejected bind mutated model: %q", r.Current(sess))
	}
}

func TestRouterReturnsNilWhenBoundModelDisappears(t *testing.T) {
	// When a session's recorded model no longer exists in the registry
	// (config edit, rename, etc), Adapter must return nil so callers
	// surface an error to the user instead of silently falling back.
	r, _, _ := newTestRouter()
	sess := NewSession("testns", "sys")
	sess.SetModel(context.Background(), "ghost")
	if r.Adapter(sess) != nil {
		t.Fatal("expected nil when bound model is unknown")
	}
}

func TestRouterUsesModelField(t *testing.T) {
	r, _, b := newTestRouter()
	sess := NewSession("testns", "sys")
	sess.SetModel(context.Background(), "b")
	if r.Adapter(sess) != b {
		t.Fatal("router did not read the Model field")
	}
}
