// Package external implements the Graders that live outside this process.
//
// It exists so that a Grader can be written in any language: the contract is a
// JSON request and a JSON response, over HTTP or over a pipe. Until this
// package, "protocol over SDK" was a stated boundary with only Go
// implementations behind it.
//
// One rule shapes the error handling throughout. A response saying
// "status": "fail" is adopted verbatim — an external Grader is entitled to
// report that it could not conclude. A response that is not a valid Evaluation
// at all is a protocol_error, and it is never quietly repaired: silently fixing
// an implementation's output would leave its author permanently unaware they got
// it wrong.
//
// # Stability
//
// Internal package: no compatibility promise. The wire contract itself is
// documented in doc/protocol.md and versioned with the specification.
package external

import (
	"encoding/json"
	"fmt"

	"github.com/sequencestream/evalexec/evalspec"
)

// decodeEvaluation parses an external Grader's response and checks it against
// the protocol's invariants.
//
// Validation is deliberately strict. An external implementation that returns a
// failure carrying a score has misunderstood the rule that a failure is not a
// zero; accepting it would put a number nobody measured into the average, and
// correcting it would hide the misunderstanding.
func decodeEvaluation(data []byte) (evalspec.Evaluation, error) {
	var eval evalspec.Evaluation
	if err := json.Unmarshal(data, &eval); err != nil {
		return evalspec.Evaluation{}, fmt.Errorf("the response is not a valid evaluation object: %w", err)
	}

	if !eval.Status.IsValid() {
		return evalspec.Evaluation{}, fmt.Errorf(
			"the response has status %q, want success or fail", eval.Status)
	}

	rec := evalspec.Record{
		EvalID: "x", CaseID: "x", Sequence: 1,
		Status: evalspec.RecordCompleted, Evaluation: &eval,
		StartedAt: &evalspec.Timestamp{}, FinishedAt: &evalspec.Timestamp{},
	}

	if err := rec.Validate(); err != nil {
		return evalspec.Evaluation{}, fmt.Errorf("the response violates the evaluation invariants: %w", err)
	}

	if eval.Evidence == nil {
		eval.Evidence = []evalspec.Evidence{}
	}

	return eval, nil
}

// protocolFailure builds the evaluation recorded when an external Grader could
// not be understood.
func protocolFailure(reason string, err error) evalspec.Evaluation {
	return evalspec.NewFailEvaluation(evalspec.CodeProtocolError, err.Error(), reason, nil, evalspec.Usage{}, 0)
}

// timeoutFailure builds the evaluation recorded when an external Grader ran out
// of time.
func timeoutFailure(reason string) evalspec.Evaluation {
	return evalspec.NewFailEvaluation(evalspec.CodeTimeout, "the grader exceeded its timeout",
		reason, nil, evalspec.Usage{}, 0)
}

// declarationFrom builds the declaration for an external Grader.
//
// It comes from the configuration rather than from the Grader itself. Asking
// the external process what it requires would make the pre-check depend on
// contacting the very thing it exists to validate before contacting — and would
// mean a Grader that is unreachable produces no pre-check failure but a run
// full of protocol errors.
func declarationFrom(spec evalspec.GraderSpec) evalspec.GraderSpec { return spec }
