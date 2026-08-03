package agent

import (
	"crypto/rand"
)

// RandomIDGenerator produces fixed-length base62 IDs using crypto/rand.
// It generates 6 random bytes and encodes them as ~9 base62 characters,
// providing ~48 bits of entropy.
type RandomIDGenerator struct{}

// NewRandomIDGenerator creates a RandomIDGenerator.
func NewRandomIDGenerator() *RandomIDGenerator {
	return &RandomIDGenerator{}
}

// GenerateID reads 6 random bytes and encodes them as a base62 string.
// Panics only if crypto/rand.Reader fails — which never happens in practice.
func (g *RandomIDGenerator) GenerateID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("idgen: crypto/rand.Read failed: " + err.Error())
	}
	return bytesToBase62(b[:])
}
