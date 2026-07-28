package agent

import (
	"encoding/json"
	"testing"
)

func TestSessionCost_Tracking(t *testing.T) {
	s := NewSession("testns", "")
	if s.TotalCost() != 0 {
		t.Fatalf("expected initial cost 0, got %f", s.TotalCost())
	}

	s.AddCost(0.0000950625)
	if s.TotalCost() != 0.0000950625 {
		t.Fatalf("expected cost 0.0000950625, got %f", s.TotalCost())
	}

	s.AddCost(0.00015)
	if want := 0.0000950625 + 0.00015; s.TotalCost() != want {
		t.Fatalf("expected cost %f, got %f", want, s.TotalCost())
	}

	s.Update(func(d *SessionData) { d.TotalCost = 0.5 })
	if s.TotalCost() != 0.5 {
		t.Fatalf("expected cost 0.5, got %f", s.TotalCost())
	}
}

func TestSessionCost_DeepCopy(t *testing.T) {
	s := NewSession("testns", "")
	s.AddCost(0.001)
	s.AddCost(0.002)

	data := s.Read()
	if data.TotalCost != 0.003 {
		t.Fatalf("expected TotalCost 0.003 in copy, got %f", data.TotalCost)
	}

	// Modify original
	s.AddCost(0.005)
	// Copy should be unchanged
	if data.TotalCost != 0.003 {
		t.Fatalf("expected copy TotalCost 0.003 after original change, got %f", data.TotalCost)
	}
}

func TestSessionCost_JSONRoundTrip(t *testing.T) {
	s := NewSession("testns", "sys")
	s.AddCost(0.0000950625)
	s.AddCost(0.00015)
	// Also add some tokens
	s.AddTokenUsage(100)
	s.AddTokenUsage(200)

	data := s.Read()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SessionData
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.TotalCost != 0.0002450625 {
		t.Fatalf("expected TotalCost 0.0002450625 after JSON round-trip, got %f", decoded.TotalCost)
	}
	if decoded.TotalTokens != 300 {
		t.Fatalf("expected TotalTokens 300 after JSON round-trip, got %d", decoded.TotalTokens)
	}
}
