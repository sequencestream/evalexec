// Package evalerr is the error model every other package tags its failures
// with, so that exactly one place — the exitcode package — decides what a
// failure means to the caller.
//
// The classification lives here rather than in exitcode because the dependency
// has to point one way. Every package that produces an error imports this one
// to label it; only exitcode imports it to read the label. Putting Kind in
// exitcode would force validate, cli, dataset and result to all depend on the
// package that knows about exit codes, which is meant to sit at the end of the
// chain, not the middle of it.
//
// # Stability
//
// L2 Go API. Changeable during v0; from v1.0 it follows the Go compatibility
// promise.
package evalerr

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failure by what the caller should conclude from it. It
// deliberately does not name exit codes: the mapping is exitcode's job, and
// two kinds can share a code while still meaning different things.
type Kind int

// The failure kinds. Kind zero is invalid, so a struct literal that forgets to
// set one is caught rather than silently classified as the first case.
const (
	// KindArgument means the command line or the request structure was
	// malformed. Nothing was read, nothing was written.
	KindArgument Kind = iota + 1
	// KindPrecheck means a pre-check rejected the run: a Grader declaration,
	// a Judge configuration, or the dataset. No Grader or Judge was called.
	//
	// It shares an exit code with KindArgument but stays a separate kind,
	// because the diagnostics differ: one says the invocation was wrong, the
	// other says the data was.
	KindPrecheck
	// KindOutput means the output directory conflicted or could not be
	// written.
	KindOutput
	// KindRuntime means a run-level fault left no trustworthy result.
	KindRuntime
	// KindInterrupt means the user interrupted the run.
	KindInterrupt
)

// String renders the kind for diagnostics.
func (k Kind) String() string {
	switch k {
	case KindArgument:
		return "argument"
	case KindPrecheck:
		return "precheck"
	case KindOutput:
		return "output"
	case KindRuntime:
		return "runtime"
	case KindInterrupt:
		return "interrupt"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// IsValid reports whether k is one of the defined kinds.
func (k Kind) IsValid() bool {
	return k >= KindArgument && k <= KindInterrupt
}

// Error is a classified failure.
type Error struct {
	// Kind decides the exit code.
	Kind Kind
	// Step names the stage that failed, e.g. "dataset_parse". It is part of
	// the stable contract: the pre-check fixtures assert it, and it is what
	// tells a user which of the six ordered checks rejected their run.
	Step string
	// Message is the human-readable explanation written to stderr.
	Message string
	// Err is an optional wrapped cause.
	Err error
	// Reported marks a failure whose cause has already been written to the
	// user's terminal by something else — in practice the flag package, which
	// prints its own complaint before returning. A caller that prints this
	// error too would say the same thing twice.
	//
	// It is an explicit flag rather than a guess, because the obvious
	// heuristic ("no message of its own") also matches a wrapped validation
	// error, and suppressing those leaves a rejected run with no diagnostic
	// at all.
	Reported bool
}

// Error renders the failure as "step: message: cause", omitting whichever
// parts are absent. The cause is always included when present: a wrapper that
// swallowed it would report "dataset is not valid JSONL" without ever saying
// which line.
func (e *Error) Error() string {
	parts := make([]string, 0, 3)

	if e.Step != "" {
		parts = append(parts, e.Step)
	}

	if e.Message != "" {
		parts = append(parts, e.Message)
	}

	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}

	if len(parts) == 0 {
		return "evalexec: unspecified error"
	}

	return strings.Join(parts, ": ")
}

func (e *Error) Unwrap() error { return e.Err }

// New builds a classified error.
func New(kind Kind, step, format string, args ...any) *Error {
	return &Error{Kind: kind, Step: step, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds a classified error around a cause.
func Wrap(kind Kind, step string, err error, format string, args ...any) *Error {
	return &Error{Kind: kind, Step: step, Message: fmt.Sprintf(format, args...), Err: err}
}

// Argument reports a malformed command line or request structure.
func Argument(step, format string, args ...any) *Error {
	return New(KindArgument, step, format, args...)
}

// Precheck reports a run rejected before any Grader or Judge was called.
func Precheck(step, format string, args ...any) *Error {
	return New(KindPrecheck, step, format, args...)
}

// Output reports an output directory conflict or write failure.
func Output(step, format string, args ...any) *Error {
	return New(KindOutput, step, format, args...)
}

// Runtime reports a run-level fault.
func Runtime(step, format string, args ...any) *Error {
	return New(KindRuntime, step, format, args...)
}

// KindOf returns the kind of the first *Error in err's chain.
func KindOf(err error) (Kind, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind, true
	}

	return 0, false
}

// StepOf returns the step of the first *Error in err's chain.
func StepOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Step
	}

	return ""
}
