package main

import (
	"testing"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		level string
	}{
		{"debug"},
		{"info"},
		{"warn"},
		{"error"},
		{"unknown"}, // fallback to info
	}

	for _, tt := range tests {
		logger := newLogger(tt.level)
		if logger == nil {
			t.Errorf("expected logger to not be nil for level %s", tt.level)
		}
	}
}
