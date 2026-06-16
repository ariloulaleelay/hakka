package agent

import "testing"

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
	if err := r.Bind(sess, "b"); err != nil {
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
	if err := r.Bind(sess, "nope"); err == nil {
		t.Fatal("expected error binding unknown model")
	}
	// Model must be untouched after a rejected bind.
	if r.Current(sess) != "a" {
		t.Fatalf("rejected bind mutated model: %q", r.Current(sess))
	}
}

func TestRouterFallsBackWhenBoundModelDisappears(t *testing.T) {
	// A session may have been bound to a model that the current process
	// no longer registers (config edit, rename, etc). Adapter must fall
	// back to the registry default rather than returning nil.
	r, a, _ := newTestRouter()
	sess := NewSession("testns", "sys")
	sess.SetModel("ghost")
	if r.Adapter(sess) != a {
		t.Fatal("expected fallback to default when bound model is unknown")
	}
}

func TestRouterUsesModelField(t *testing.T) {
	r, _, b := newTestRouter()
	sess := NewSession("testns", "sys")
	sess.SetModel("b")
	if r.Adapter(sess) != b {
		t.Fatal("router did not read the Model field")
	}
}