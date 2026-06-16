package agent

import "testing"

func TestRegistryDefault(t *testing.T) {
	r := NewRegistry()
	a := &fakeAdapter{}
	b := &fakeAdapter{}
	r.Register("a", a)
	r.Register("b", b)
	if r.Default() != "a" {
		t.Fatalf("default: %q", r.Default())
	}
	if err := r.SetDefault("b"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if r.Default() != "b" {
		t.Fatalf("default after set: %q", r.Default())
	}
	if err := r.SetDefault("nope"); err == nil {
		t.Fatal("expected error for unknown adapter")
	}
}

func TestRegistryNames(t *testing.T) {
	r := NewRegistry()
	r.Register("z", &fakeAdapter{})
	r.Register("a", &fakeAdapter{})
	names := r.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "z" {
		t.Fatalf("names: %+v", names)
	}
}
