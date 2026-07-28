// Package exitcode is the single place that turns a failure or a result into
// a process exit code.
//
// It exists as its own package so the mapping cannot drift: no other package
// may decide what a given failure means to a shell script. An exit code says
// only whether the command executed successfully — never whether the agent
// under evaluation performed well. That distinction is the reason a fail-fast
// run exits 0.
//
// # Stability
//
// L3 component. The exit code values themselves are a user-facing contract and
// will not change; the Go API follows the compatibility promise from v1.0.
package exitcode

import (
	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
)

// The exit codes. Any value outside this set is an undefined fault.
const (
	// OK means a trustworthy EvalResult was produced. It may contain failed
	// evaluations, and it may be incomplete after a fail-fast stop.
	OK = 0
	// Argument means a pre-check rejected the run before any Grader or Judge
	// was called. No result was produced.
	Argument = 2
	// Runtime means a run-level fault left no trustworthy result.
	Runtime = 3
	// Output means the output directory conflicted or could not be written.
	Output = 4
	// Interrupt means the user interrupted the run. The result directory may
	// or may not have been published; the caller checks whether it exists.
	Interrupt = 130
)

// FromError maps a failure to an exit code.
//
// An unclassified error maps to Runtime rather than to OK. An unknown failure
// is a fault, and reporting success for something nobody classified would let
// a bug read as a clean run.
func FromError(err error) int {
	if err == nil {
		return OK
	}

	kind, ok := evalerr.KindOf(err)
	if !ok {
		return Runtime
	}

	return FromKind(kind)
}

// FromKind maps a failure kind to an exit code.
func FromKind(kind evalerr.Kind) int {
	switch kind {
	case evalerr.KindArgument, evalerr.KindPrecheck:
		return Argument
	case evalerr.KindOutput:
		return Output
	case evalerr.KindInterrupt:
		return Interrupt
	case evalerr.KindRuntime:
		return Runtime
	default:
		return Runtime
	}
}

// FromResult maps a completed run to an exit code.
//
// A fail-fast stop exits 0. It is a stopping policy the caller explicitly
// requested, so the command did exactly what it was told; that the result is
// incomplete is expressed by status=cancelled and counts.skipped, not by the
// exit code. This is the single easiest mapping to get wrong.
func FromResult(r *evalspec.EvalResult) int {
	if r == nil {
		return Runtime
	}

	switch r.Status {
	case evalspec.RunCompleted:
		return OK
	case evalspec.RunCancelled:
		if r.StopReason != nil && *r.StopReason == evalspec.StopInterrupt {
			return Interrupt
		}

		return OK
	case evalspec.RunFailed:
		return Runtime
	default:
		return Runtime
	}
}
