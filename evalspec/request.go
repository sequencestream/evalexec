package evalspec

import (
	"encoding/json"
)

// EvalRequest is everything one evaluation needs. CLI flags and an optional
// request file are normalized into exactly this shape before anything runs.
type EvalRequest struct {
	// SpecVersion is always SpecVersion for requests this implementation writes.
	SpecVersion string `json:"spec_version"`
	// EvalID globally identifies this run. It is optional on input — the CLI
	// generates one when absent — but always present in the normalized
	// request and in every record.
	EvalID string `json:"eval_id,omitempty"`
	// TaskID is a plain correlation key. EvalExec validates only that it is
	// non-empty and echoes it verbatim; it triggers no lookup, no state and
	// no lifecycle.
	TaskID string `json:"task_id"`
	// Dataset locates the session JSONL file.
	Dataset Dataset `json:"dataset"`
	// Grader is the one and only Grader for this run. It is never an array.
	Grader GraderSpec `json:"grader"`
	// JudgeModel is required exactly when Grader.RequiresJudge is true, and
	// unused otherwise even if supplied.
	JudgeModel *JudgeModelSpec `json:"judge_model,omitempty"`
	// Execution carries concurrency, seed and fail-fast.
	Execution *Execution `json:"execution,omitempty"`
	// OutputDir is the result directory. It must not exist, or must be empty.
	OutputDir string `json:"output_dir"`
}

// Dataset locates the agent session rows to grade.
type Dataset struct {
	// Path is the JSONL file path, absolute after normalization.
	Path string `json:"path"`
}

// GraderSpec configures the single Grader, including the self-description
// EvalExec relies on to validate a run before calling anything.
type GraderSpec struct {
	// ID is written verbatim into the result.
	ID string `json:"id"`
	// Version is written verbatim into the result.
	Version string `json:"version"`
	// Protocol selects how the Grader is reached.
	Protocol GraderProtocol `json:"protocol"`
	// Entry is the built-in Grader name for the builtin protocol, or the
	// endpoint / executable for the external ones.
	Entry string `json:"entry,omitempty"`
	// Requires lists the session fields this Grader needs. Together with
	// RequiresJudge it lets EvalExec fully validate a run without
	// understanding the Grader's internals — which is what keeps external
	// Graders subject to the same pre-checks as built-in ones.
	Requires []SessionField `json:"requires"`
	// RequiresJudge declares whether a judge_model must be supplied.
	RequiresJudge bool `json:"requires_judge"`
	// Parameters are the Grader's own settings, including rubric and scale
	// bounds. EvalExec passes them through and never interprets them.
	Parameters map[string]any `json:"parameters,omitempty"`
	// TimeoutMS bounds one sample's evaluation.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// JudgeModelSpec configures the LLM Judge used by the Grader, if any. It never
// describes the agent under evaluation — EvalExec does not call that agent.
type JudgeModelSpec struct {
	// Protocol selects the Judge transport.
	Protocol JudgeProtocol `json:"protocol"`
	// Endpoint is the service base URL, required by openai-chat and
	// http-json; for stdio-jsonl it is the executable.
	Endpoint string `json:"endpoint,omitempty"`
	// Auth references a credential; it never contains one.
	Auth Auth `json:"auth"`
	// Parameters map onto the chat request. Only the documented keys are
	// accepted; an unknown key is an argument error rather than a silent drop.
	Parameters map[string]any `json:"parameters,omitempty"`
	// TimeoutMS bounds one Judge call.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// Auth references a credential by environment variable name. A secret is
// never written into a request snapshot or a result, so this struct
// deliberately has nowhere to put one.
type Auth struct {
	// Type selects the credential scheme.
	Type AuthType `json:"type"`
	// Env is the environment variable holding the token, for AuthBearerEnv.
	Env string `json:"env,omitempty"`
}

// Execution carries the run-level knobs.
type Execution struct {
	// Concurrency is the number of samples evaluated at once; the default is 1.
	Concurrency int `json:"concurrency,omitempty"`
	// Seed is recorded in provenance for traceability. It is not forwarded to
	// the Judge: the canonical chat request has no seed field, so claiming to
	// pass it through would be a lie in the request snapshot.
	Seed *int `json:"seed,omitempty"`
	// FailFast stops dispatching new samples after the first failed
	// evaluation. Only evaluation.status=fail triggers it — a low score never
	// does, because EvalExec does not interpret scores.
	FailFast bool `json:"fail_fast,omitempty"`
}

// GradeCall is the normalized request handed to the Grader for one sample.
// It is the protocol shape shared by the builtin, http-json and stdio-jsonl
// Graders alike.
type GradeCall struct {
	EvalID string `json:"eval_id"`
	TaskID string `json:"task_id"`
	CaseID string `json:"case_id"`

	// The seven session fields, carried as raw JSON so the Grader sees
	// exactly what the dataset held. A field absent from the session is
	// absent here too.
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Trajectory json.RawMessage `json:"trajectory,omitempty"`
	Reference  json.RawMessage `json:"reference,omitempty"`
	Context    json.RawMessage `json:"context,omitempty"`
	Criteria   json.RawMessage `json:"criteria,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`

	// Parameters is grader.parameters after any --grader-param overrides.
	Parameters map[string]any `json:"parameters,omitempty"`
}

// NewGradeCall normalizes one session into a Grader request. Only the fields
// present in the session are carried over, so a Grader can still tell an
// absent field from a null one.
func NewGradeCall(evalID, taskID string, s *Session, params map[string]any) GradeCall {
	c := GradeCall{
		EvalID:     evalID,
		TaskID:     taskID,
		CaseID:     s.CaseID,
		Parameters: params,
	}

	for _, f := range s.Fields() {
		v := s.Field(f)

		switch f {
		case FieldInput:
			c.Input = v
		case FieldOutput:
			c.Output = v
		case FieldTrajectory:
			c.Trajectory = v
		case FieldReference:
			c.Reference = v
		case FieldContext:
			c.Context = v
		case FieldCriteria:
			c.Criteria = v
		case FieldMetadata:
			c.Metadata = v
		}
	}

	return c
}
