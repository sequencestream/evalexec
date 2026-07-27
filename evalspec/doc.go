// Package evalspec is the wire protocol of EvalExec: the Go form of the
// EvalRequest a caller submits and the EvalResult a run produces, plus the
// Session data rows in between.
//
// The protocol has exactly two top-level abstractions. Everything else here —
// Session, Record, Evaluation — is transport shape, not a domain object with
// its own lifecycle.
//
// Three things in this package repay a careful read, because getting them
// wrong silently violates the specification:
//
//   - Session distinguishes an absent key from a key whose value is null.
//     A Grader declaring requires:["output"] accepts `"output": null` (the
//     agent produced no final output) but rejects a row with no "output" key
//     at all. Use Session.Has, not a nil check on the value.
//
//   - The three status enums are three distinct Go types on purpose.
//     RunStatus, RecordStatus and EvaluationStatus each have a "completed" or
//     similar-looking value with a different meaning; separate types stop them
//     flowing into each other unnoticed.
//
//   - Failed evaluations carry no score. NewFailEvaluation takes no score
//     argument at all, because a failed evaluation must never be counted as a
//     zero — the specification is explicit that a failure is not a low score.
//
// Callers outside this module may construct these types directly, so every
// invariant is enforced twice: by the controlled constructors here, and by
// Validate, which the result writer calls unconditionally before writing
// anything to disk.
//
// # Stability
//
// L1 protocol. This package shares the lifecycle of SpecVersion. Within
// evalexec/v1alpha1 only optional fields are added; removing a field,
// changing a type, or changing the meaning of a status requires a new spec
// version and a new major version.
package evalspec
