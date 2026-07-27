package cli

import (
	"time"

	evalexec "github.com/sequencestream/evalexec"
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

// FixedClock returns the same instant every time, for tests.
type FixedClock struct {
	T time.Time
}

// Now returns the fixed instant.
func (c FixedClock) Now() time.Time { return c.T }

// The eval_id generator lives in the root package, because every result needs
// one whether it came from a command line or from a library call. These aliases
// keep the CLI's surface readable without a second implementation of it.
type (
	// IDGenerator produces an eval_id when the caller did not supply one.
	IDGenerator = evalexec.IDGenerator
	// UUIDv7Generator generates timestamp-ordered identifiers.
	UUIDv7Generator = evalexec.UUIDv7Generator
	// FixedIDGenerator returns the same identifier every time, for tests.
	FixedIDGenerator = evalexec.FixedIDGenerator
)
