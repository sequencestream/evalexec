package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Clock supplies the current time. It is injectable so golden-file tests can
// compare results that would otherwise differ on every run.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// IDGenerator produces an eval_id when the caller did not supply one.
type IDGenerator interface {
	NewID() (string, error)
}

// UUIDv7Generator generates UUIDv7 identifiers: a 48-bit millisecond
// timestamp followed by 74 bits of randomness.
//
// Version 7 rather than 4 because the timestamp prefix makes identifiers sort
// chronologically, which matters when a directory holds a run per hour. It is
// implemented here rather than pulled from a library to keep the dependency
// surface at aimodel plus one JSON Schema library — it is twenty lines.
type UUIDv7Generator struct {
	// Clock is the time source; nil means the system clock.
	Clock Clock
	// Rand supplies randomness; nil means crypto/rand.
	Rand func([]byte) (int, error)
}

// NewID returns a new UUIDv7 in the canonical hyphenated form.
func (g UUIDv7Generator) NewID() (string, error) {
	var b [16]byte

	now := time.Now
	if g.Clock != nil {
		now = g.Clock.Now
	}

	ms := now().UTC().UnixMilli()

	// 48-bit big-endian millisecond timestamp.
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	read := rand.Read
	if g.Rand != nil {
		read = g.Rand
	}

	if _, err := read(b[6:]); err != nil {
		return "", fmt.Errorf("cli: generate eval_id: %w", err)
	}

	// Version 7 in the high nibble of byte 6, RFC 4122 variant in byte 8.
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80

	return formatUUID(b), nil
}

// formatUUID renders the canonical 8-4-4-4-12 form.
func formatUUID(b [16]byte) string {
	var out [36]byte

	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])

	return string(out[:])
}

// FixedIDGenerator returns the same identifier every time, for tests.
type FixedIDGenerator struct {
	ID string
}

// NewID returns the fixed identifier.
func (g FixedIDGenerator) NewID() (string, error) { return g.ID, nil }

// FixedClock returns the same instant every time, for tests.
type FixedClock struct {
	T time.Time
}

// Now returns the fixed instant.
func (c FixedClock) Now() time.Time { return c.T }
