package agent

import (
	"math/big"
	"strings"
	"testing"
)

func TestBase62Encode(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "A"},
		{35, "Z"},
		{36, "a"},
		{61, "z"},
		{62, "10"},
		{124, "20"},
		{3843, "zz"},
		{3844, "100"},
		{238327, "zzz"},
	}
	for _, tt := range tests {
		got := base62Encode(big.NewInt(tt.n))
		if got != tt.want {
			t.Errorf("base62Encode(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestBytesToBase62RoundTrip(t *testing.T) {
	// bytesToBase62 is deterministic: same bytes → same string.
	input := []byte{0x01, 0x02, 0x03}
	got1 := bytesToBase62(input)
	got2 := bytesToBase62(input)
	if got1 != got2 {
		t.Errorf("bytesToBase62 not deterministic: %q vs %q", got1, got2)
	}
}

func TestMakeUniqueID(t *testing.T) {
	// Default generator produces non-empty strings.
	id := MakeUniqueID()
	if id == "" {
		t.Error("MakeUniqueID() returned empty string")
	}
}

func TestRandomIDGenerator(t *testing.T) {
	gen := NewRandomIDGenerator()

	// Generate 100 IDs, check they're all non-empty and unique.
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := gen.GenerateID()
		if id == "" {
			t.Fatal("GenerateID() returned empty string")
		}
		if seen[id] {
			t.Fatalf("duplicate ID generated: %q", id)
		}
		seen[id] = true
	}

	// Verify all chars are from the base62 alphabet.
	for id := range seen {
		for _, c := range id {
			if !strings.ContainsRune(base62Alphabet, c) {
				t.Errorf("ID %q contains invalid char %q", id, c)
			}
		}
	}
}

func TestSetDefaultIDGenerator(t *testing.T) {
	// Save original and restore after test.
	orig := defaultIDGen
	defer func() { defaultIDGen = orig }()

	// Set a mock generator that returns a known value.
	SetDefaultIDGenerator(&mockIDGen{value: "test-id-42"})
	got := MakeUniqueID()
	if got != "test-id-42" {
		t.Errorf("MakeUniqueID() = %q after SetDefaultIDGenerator, want %q", got, "test-id-42")
	}

	// Setting nil should not change the generator.
	SetDefaultIDGenerator(nil)
	got = MakeUniqueID()
	if got != "test-id-42" {
		t.Errorf("MakeUniqueID() = %q after SetDefaultIDGenerator(nil), want %q", got, "test-id-42")
	}
}

type mockIDGen struct{ value string }

func (m *mockIDGen) GenerateID() string { return m.value }

func TestBase62AlphabetSize(t *testing.T) {
	// Verify the alphabet has exactly 62 unique chars.
	seen := make(map[rune]bool)
	for _, c := range base62Alphabet {
		if seen[c] {
			t.Errorf("duplicate char %q in base62Alphabet", c)
		}
		seen[c] = true
	}
	if len(seen) != 62 {
		t.Errorf("base62Alphabet has %d unique chars, want 62", len(seen))
	}
	// Verify 0-9, a-z, A-Z are all present.
	for c := '0'; c <= '9'; c++ {
		if !seen[c] {
			t.Errorf("base62Alphabet missing %q", c)
		}
	}
	for c := 'a'; c <= 'z'; c++ {
		if !seen[c] {
			t.Errorf("base62Alphabet missing %q", c)
		}
	}
	for c := 'A'; c <= 'Z'; c++ {
		if !seen[c] {
			t.Errorf("base62Alphabet missing %q", c)
		}
	}
}

func TestBase62SortOrder(t *testing.T) {
	// Base62 preserves sort order within the same digit length.
	// Numbers with the same number of base62 digits sort correctly.
	tests := []struct {
		lo, hi int64
		digits int // expected number of base62 digits
	}{
		{0, 61, 1},
		{62, 3843, 2},     // 62 → "10", 3843 → "zz"
		{3844, 238327, 3}, // 3844 → "100", 238327 → "zzz"
	}
	for _, tt := range tests {
		lo := base62Encode(big.NewInt(tt.lo))
		hi := base62Encode(big.NewInt(tt.hi))
		if len(lo) != tt.digits {
			t.Errorf("%d → %q has %d digits, want %d", tt.lo, lo, len(lo), tt.digits)
		}
		if len(hi) != tt.digits {
			t.Errorf("%d → %q has %d digits, want %d", tt.hi, hi, len(hi), tt.digits)
		}
		if lo >= hi {
			t.Errorf("base62 order violation: %d → %q >= %d → %q", tt.lo, lo, tt.hi, hi)
		}
	}
}

func TestBase62EncodeExample(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{10, "A"},
		{35, "Z"},
		{36, "a"},
		{61, "z"},
		{62, "10"},
		{100, "1c"},
	}
	for _, tt := range tests {
		got := base62Encode(big.NewInt(tt.n))
		if got != tt.want {
			t.Errorf("base62Encode(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
