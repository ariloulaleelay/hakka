package agent

import (
	"testing"
)

func TestRegistry_GetProfile(t *testing.T) {
	reg := NewRegistry()
	adapter := &simpleAdapter{}
	reg.Register("test-model", adapter)

	profile, ok := reg.GetProfile("test-model")
	if !ok {
		t.Fatal("expected profile for 'test-model'")
	}
	if profile.Adapter == nil {
		t.Fatal("expected non-nil adapter in profile")
	}
	if profile.CompactSoftLimit != 0 {
		t.Fatalf("expected CompactSoftLimit=0 (default), got %d", profile.CompactSoftLimit)
	}

	// Get should still work (backward compat)
	a, ok := reg.Get("test-model")
	if !ok {
		t.Fatal("expected adapter for 'test-model'")
	}
	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
}

func TestRegistry_GetProfile_NotFound(t *testing.T) {
	reg := NewRegistry()
	_, ok := reg.GetProfile("nonexistent")
	if ok {
		t.Fatal("expected no profile for nonexistent model")
	}
}

func TestRegistry_ModelProfile_CompactSoftLimit(t *testing.T) {
	reg := NewRegistry()

	// Register a model with a profile that has a compact soft limit
	reg.Register("model-with-limit", &simpleAdapter{})
	reg.SetProfileCompactSoftLimit("model-with-limit", 50000)

	profile, ok := reg.GetProfile("model-with-limit")
	if !ok {
		t.Fatal("expected profile for 'model-with-limit'")
	}
	if profile.CompactSoftLimit != 50000 {
		t.Fatalf("expected CompactSoftLimit=50000, got %d", profile.CompactSoftLimit)
	}

	// Another model without limit
	reg.Register("model-without-limit", &simpleAdapter{})
	profile2, ok := reg.GetProfile("model-without-limit")
	if !ok {
		t.Fatal("expected profile for 'model-without-limit'")
	}
	if profile2.CompactSoftLimit != 0 {
		t.Fatalf("expected CompactSoftLimit=0, got %d", profile2.CompactSoftLimit)
	}
}

func TestRegistry_SetProfileCompactSoftLimit_NotFound(t *testing.T) {
	reg := NewRegistry()
	// Should not panic
	reg.SetProfileCompactSoftLimit("nonexistent", 50000)
}

func TestRegistry_Register_PreservesDefault(t *testing.T) {
	reg := NewRegistry()
	reg.Register("model-a", &simpleAdapter{})
	if reg.Default() != "model-a" {
		t.Fatalf("expected default='model-a', got %q", reg.Default())
	}
	reg.Register("model-b", &simpleAdapter{})
	if reg.Default() != "model-a" {
		t.Fatalf("expected default='model-a' (first registered), got %q", reg.Default())
	}
}
