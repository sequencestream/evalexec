package evalspec_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sequencestream/evalexec/evalspec"
)

func errorsAs(err error, target any) bool { return errors.As(err, target) }

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}

	return parsed
}

// TestStatusEnumsAreDistinctTypes documents why there are three types rather
// than one string. "completed" is a legal value of both RunStatus and
// RecordStatus with a different meaning in each — the run-level one means
// every sample was dispatched, the record-level one means this sample reached
// the Grader. Distinct types stop one being assigned where the other belongs.
//
// The compile-time guarantee is the point; this test only pins the values.
func TestStatusEnumsAreDistinctTypes(t *testing.T) {
	if string(evalspec.RunCompleted) != string(evalspec.RecordCompleted) {
		t.Fatal("precondition changed: the two statuses no longer share a spelling")
	}

	// Both spell "completed", yet the following does not compile, which is
	// exactly the protection being bought:
	//
	//	var rs evalspec.RecordStatus = evalspec.RunCompleted
	//	                               ^ cannot use RunStatus as RecordStatus
	rs := evalspec.RecordCompleted
	if !rs.IsValid() {
		t.Error("RecordCompleted must be valid")
	}
}

func TestEnumIsValid(t *testing.T) {
	tests := []struct {
		name  string
		valid []interface{ IsValid() bool }
		bad   []interface{ IsValid() bool }
	}{
		{
			name:  "RunStatus",
			valid: []interface{ IsValid() bool }{evalspec.RunCompleted, evalspec.RunCancelled, evalspec.RunFailed},
			bad:   []interface{ IsValid() bool }{evalspec.RunStatus(""), evalspec.RunStatus("skipped"), evalspec.RunStatus("success")},
		},
		{
			name:  "RecordStatus",
			valid: []interface{ IsValid() bool }{evalspec.RecordCompleted, evalspec.RecordSkipped},
			bad: []interface{ IsValid() bool }{
				evalspec.RecordStatus(""),
				evalspec.RecordStatus("cancelled"),
				// 03-cli-and-execution.md once mentioned an "error" sample
				// state; the core spec's two-valued definition wins.
				evalspec.RecordStatus("error"),
			},
		},
		{
			name:  "EvaluationStatus",
			valid: []interface{ IsValid() bool }{evalspec.EvaluationSuccess, evalspec.EvaluationFail},
			bad: []interface{ IsValid() bool }{
				evalspec.EvaluationStatus(""),
				evalspec.EvaluationStatus("completed"),
				// There is no third evaluation state: no "partial", because
				// EvalExec does not run the agent under evaluation.
				evalspec.EvaluationStatus("partial"),
			},
		},
		{
			name:  "StopReason",
			valid: []interface{ IsValid() bool }{evalspec.StopFailFast, evalspec.StopInterrupt, evalspec.StopError},
			bad:   []interface{ IsValid() bool }{evalspec.StopReason(""), evalspec.StopReason("timeout")},
		},
		{
			name: "ErrorCode",
			valid: []interface{ IsValid() bool }{
				evalspec.CodeInsufficientEvidence, evalspec.CodeJudgeError, evalspec.CodeTimeout,
				evalspec.CodeProtocolError, evalspec.CodeInternalError, evalspec.CodeSkipped,
			},
			bad: []interface{ IsValid() bool }{evalspec.ErrorCode(""), evalspec.ErrorCode("failed")},
		},
		{
			name:  "GraderProtocol",
			valid: []interface{ IsValid() bool }{evalspec.GraderBuiltin, evalspec.GraderHTTPJSON, evalspec.GraderStdioJSONL},
			bad:   []interface{ IsValid() bool }{evalspec.GraderProtocol(""), evalspec.GraderProtocol("openai-chat")},
		},
		{
			name: "JudgeProtocol",
			valid: []interface{ IsValid() bool }{
				evalspec.JudgeOpenAIChat, evalspec.JudgeAnthropicMessages,
				evalspec.JudgeHTTPJSON, evalspec.JudgeStdioJSONL,
			},
			bad: []interface{ IsValid() bool }{evalspec.JudgeProtocol(""), evalspec.JudgeProtocol("builtin")},
		},
		{
			name:  "AuthType",
			valid: []interface{ IsValid() bool }{evalspec.AuthBearerEnv, evalspec.AuthNone},
			bad:   []interface{ IsValid() bool }{evalspec.AuthType(""), evalspec.AuthType("basic")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, v := range tt.valid {
				if !v.IsValid() {
					t.Errorf("%v must be valid", v)
				}
			}

			for _, v := range tt.bad {
				if v.IsValid() {
					t.Errorf("%v must not be valid", v)
				}
			}
		})
	}
}

// TestCodeSkippedIsNotAnEvaluationFailure guards the identity
// fail = sum(fail_by_code): a skipped sample was never evaluated, so counting
// it as a failure would break the sum.
func TestCodeSkippedIsNotAnEvaluationFailure(t *testing.T) {
	if evalspec.CodeSkipped.IsEvaluationFailure() {
		t.Error("skipped must not classify a failed evaluation")
	}

	for _, c := range []evalspec.ErrorCode{
		evalspec.CodeInsufficientEvidence, evalspec.CodeJudgeError,
		evalspec.CodeTimeout, evalspec.CodeProtocolError, evalspec.CodeInternalError,
	} {
		if !c.IsEvaluationFailure() {
			t.Errorf("%q must be able to classify a failed evaluation", c)
		}
	}
}

func TestSpecVersion(t *testing.T) {
	if evalspec.SpecVersion != "evalexec/v1alpha1" {
		t.Errorf("SpecVersion = %q; changing it is a protocol version bump", evalspec.SpecVersion)
	}
}
