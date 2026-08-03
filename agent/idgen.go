package agent

import "math/big"

// IDGenerator produces short, unique identifiers.
//
// Implementations may use crypto/rand (standalone) or a DB sequence
// (when a database is available). DB-backed generators produce
// monotonic, compact IDs; random generators produce fixed-length
// base62 strings.
type IDGenerator interface {
	GenerateID() string
}

// defaultIDGen is the package-level generator used by MakeUniqueID.
// It defaults to a RandomIDGenerator and can be replaced with a
// DB-backed generator via SetDefaultIDGenerator.
var defaultIDGen IDGenerator = NewRandomIDGenerator()

// MakeUniqueID generates a short unique identifier using the
// package-level default generator.
func MakeUniqueID() string {
	return defaultIDGen.GenerateID()
}

// SetDefaultIDGenerator replaces the package-level ID generator.
// Call once at startup, before any concurrent calls to MakeUniqueID.
func SetDefaultIDGenerator(g IDGenerator) {
	if g != nil {
		defaultIDGen = g
	}
}

// ---------------------------------------------------------------------------
// Base62 encoding (0-9a-zA-Z)
// ---------------------------------------------------------------------------

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var base62Base = big.NewInt(62)

// base62Encode encodes a non-negative big.Int to a base62 string.
func base62Encode(n *big.Int) string {
	if n.Sign() == 0 {
		return "0"
	}
	zero := big.NewInt(0)
	mod := new(big.Int)
	var buf []byte
	for n.Cmp(zero) > 0 {
		n.DivMod(n, base62Base, mod)
		buf = append(buf, base62Alphabet[mod.Int64()])
	}
	// Reverse in-place.
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// FormatIntBase62 encodes a non-negative int64 to a base62 string.
func FormatIntBase62(n int64) string {
	if n <= 0 {
		return "0"
	}
	return base62Encode(big.NewInt(n))
}

// bytesToBase62 encodes arbitrary bytes as a base62 string by
// interpreting them as a big-endian big.Int.
func bytesToBase62(b []byte) string {
	n := new(big.Int).SetBytes(b)
	return base62Encode(n)
}
