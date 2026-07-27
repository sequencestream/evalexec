package evalspec

// SpecVersion is the protocol version carried by every top-level object.
const SpecVersion = "evalexec/v1alpha1"

// RunStatus is the outcome of the run as a whole: whether EvalExec managed to
// process the dataset, never whether the graded agent was any good. A run in
// which every single evaluation failed is still RunCompleted.
type RunStatus string

const (
	// RunCompleted means every sample was dispatched and processed. It
	// requires counts.skipped == 0.
	RunCompleted RunStatus = "completed"
	// RunCancelled means dispatch stopped early (fail-fast or interrupt) but
	// the backfill and summary finished, so the result is trustworthy though
	// incomplete. It requires counts.skipped > 0 and a stop reason.
	RunCancelled RunStatus = "cancelled"
	// RunFailed means a run-level fault prevented a trustworthy EvalResult.
	// The top-level error field is then mandatory.
	RunFailed RunStatus = "failed"
)

// IsValid reports whether s is one of the three defined run statuses.
func (s RunStatus) IsValid() bool {
	switch s {
	case RunCompleted, RunCancelled, RunFailed:
		return true
	default:
		return false
	}
}

func (s RunStatus) String() string { return string(s) }

// RecordStatus says only whether a sample was executed. Because EvalExec does
// not run the agent under evaluation, there is no "partial" state.
type RecordStatus string

const (
	// RecordCompleted means the sample reached the Grader and an evaluation
	// was written — whether that evaluation succeeded or failed.
	RecordCompleted RecordStatus = "completed"
	// RecordSkipped means fail-fast or an interrupt stopped the run before
	// this sample produced an evaluation. It covers both never-dispatched and
	// dispatched-then-cancelled samples.
	RecordSkipped RecordStatus = "skipped"
)

// IsValid reports whether s is one of the two defined record statuses.
func (s RecordStatus) IsValid() bool {
	switch s {
	case RecordCompleted, RecordSkipped:
		return true
	default:
		return false
	}
}

func (s RecordStatus) String() string { return string(s) }

// EvaluationStatus says whether the evaluation itself succeeded — not whether
// the agent under evaluation performed well. Quality lives in Score and Label,
// which EvalExec never interprets.
type EvaluationStatus string

const (
	// EvaluationSuccess means the Grader completed and produced a score
	// and/or a label.
	EvaluationSuccess EvaluationStatus = "success"
	// EvaluationFail means the Grader reached no usable conclusion. It covers
	// insufficient evidence, Grader faults, Judge failures, timeouts and
	// protocol errors alike, distinguished by the error code. A failure is
	// never recorded as a zero score.
	EvaluationFail EvaluationStatus = "fail"
)

// IsValid reports whether s is one of the two defined evaluation statuses.
func (s EvaluationStatus) IsValid() bool {
	switch s {
	case EvaluationSuccess, EvaluationFail:
		return true
	default:
		return false
	}
}

func (s EvaluationStatus) String() string { return string(s) }

// StopReason explains why a run stopped early. It is non-empty exactly when
// RunStatus is not RunCompleted.
type StopReason string

const (
	// StopFailFast means the caller asked to stop on the first failed
	// evaluation. It is a normal, requested wind-down: the exit code stays 0.
	StopFailFast StopReason = "fail_fast"
	// StopInterrupt means SIGINT or SIGTERM arrived. The exit code is 130.
	StopInterrupt StopReason = "interrupt"
	// StopError means a run-level fault, paired with RunFailed.
	StopError StopReason = "error"
)

// IsValid reports whether r is one of the three defined stop reasons.
func (r StopReason) IsValid() bool {
	switch r {
	case StopFailFast, StopInterrupt, StopError:
		return true
	default:
		return false
	}
}

func (r StopReason) String() string { return string(r) }

// ErrorCode classifies why something did not produce a result. The first five
// values classify a failed evaluation; CodeSkipped appears only on a record
// backfilled after the run stopped, never inside an evaluation.
type ErrorCode string

const (
	// CodeInsufficientEvidence means the Grader declined to conclude.
	CodeInsufficientEvidence ErrorCode = "insufficient_evidence"
	// CodeJudgeError means the Judge call failed or returned unparseable
	// content. Rate limits and 5xx responses land here: EvalExec does not
	// retry.
	CodeJudgeError ErrorCode = "judge_error"
	// CodeTimeout means grader.timeout_ms or judge_model.timeout_ms elapsed.
	// A cancelled sample is skipped, not timed out.
	CodeTimeout ErrorCode = "timeout"
	// CodeProtocolError means the Grader response did not match the agreed
	// shape.
	CodeProtocolError ErrorCode = "protocol_error"
	// CodeInternalError means the Grader raised an internal fault.
	CodeInternalError ErrorCode = "internal_error"
	// CodeSkipped marks a backfilled record. It belongs on RecordError, never
	// on EvalError, and never appears in fail_by_code.
	CodeSkipped ErrorCode = "skipped"
)

// IsValid reports whether c is any defined error code, including CodeSkipped.
func (c ErrorCode) IsValid() bool {
	switch c {
	case CodeInsufficientEvidence, CodeJudgeError, CodeTimeout,
		CodeProtocolError, CodeInternalError, CodeSkipped:
		return true
	default:
		return false
	}
}

// IsEvaluationFailure reports whether c can classify a failed evaluation.
// CodeSkipped cannot: a skipped sample was never evaluated, so counting it in
// fail_by_code would break the identity fail = sum(fail_by_code).
func (c ErrorCode) IsEvaluationFailure() bool {
	return c.IsValid() && c != CodeSkipped
}

func (c ErrorCode) String() string { return string(c) }

// GraderProtocol is how EvalExec reaches the Grader.
type GraderProtocol string

const (
	// GraderBuiltin runs a Grader compiled into the binary. Downstream Go
	// programs may register their own entries under this protocol.
	GraderBuiltin GraderProtocol = "builtin"
	// GraderHTTPJSON posts a normalized grade call to a remote service.
	GraderHTTPJSON GraderProtocol = "http-json"
	// GraderStdioJSONL exchanges one JSON line per call with a subprocess.
	GraderStdioJSONL GraderProtocol = "stdio-jsonl"
)

// IsValid reports whether p is one of the three defined Grader protocols.
func (p GraderProtocol) IsValid() bool {
	switch p {
	case GraderBuiltin, GraderHTTPJSON, GraderStdioJSONL:
		return true
	default:
		return false
	}
}

func (p GraderProtocol) String() string { return string(p) }

// JudgeProtocol is how EvalExec reaches the LLM Judge.
type JudgeProtocol string

const (
	// JudgeOpenAIChat targets a Chat Completions-compatible HTTP service.
	JudgeOpenAIChat JudgeProtocol = "openai-chat"
	// JudgeAnthropicMessages targets the Anthropic Messages API. It is an
	// optional extension to the three protocols the core specification lists,
	// admissible within v1alpha1 as an added enum value.
	JudgeAnthropicMessages JudgeProtocol = "anthropic-messages"
	// JudgeHTTPJSON posts an EvalExec-defined request body.
	JudgeHTTPJSON JudgeProtocol = "http-json"
	// JudgeStdioJSONL exchanges one JSON line per call with a subprocess.
	JudgeStdioJSONL JudgeProtocol = "stdio-jsonl"
)

// IsValid reports whether p is one of the four defined Judge protocols.
func (p JudgeProtocol) IsValid() bool {
	switch p {
	case JudgeOpenAIChat, JudgeAnthropicMessages, JudgeHTTPJSON, JudgeStdioJSONL:
		return true
	default:
		return false
	}
}

func (p JudgeProtocol) String() string { return string(p) }

// AuthType is how a Judge credential is supplied. A credential is only ever
// referenced by environment variable name; it never appears in a request
// snapshot or a result.
type AuthType string

const (
	// AuthBearerEnv reads a bearer token from the named environment variable.
	AuthBearerEnv AuthType = "bearer_env"
	// AuthNone is for a local Judge endpoint with no authentication.
	AuthNone AuthType = "none"
)

// IsValid reports whether t is one of the two defined auth types.
func (t AuthType) IsValid() bool {
	switch t {
	case AuthBearerEnv, AuthNone:
		return true
	default:
		return false
	}
}

func (t AuthType) String() string { return string(t) }
