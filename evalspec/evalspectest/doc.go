// Package evalspectest compares EvalExec results against golden files.
//
// A result cannot be compared byte for byte: it carries a generated eval_id,
// wall-clock timestamps and measured durations, none of which repeat between
// runs. This package normalizes those away and compares what is left, so a
// golden file asserts the shape and the semantics of a result rather than the
// millisecond it happened to take.
//
// Two things are deliberately not normalized. The checksums in provenance
// must be reproducible — they are the whole point of traceability — so a
// change in either is a real difference. And eval_id, though replaced with a
// placeholder, is first checked for internal consistency: the result and every
// record must agree on it, which is itself one of the acceptance criteria.
//
// It lives in its own package rather than in evalspec because a test helper
// should not enlarge the protocol package's public API, yet it must be usable
// from cross-package tests and by downstream implementations checking
// themselves against the shared fixtures.
//
// # Stability
//
// L2 Go API. Changeable during v0; from v1.0 it follows the Go compatibility
// promise.
package evalspectest
