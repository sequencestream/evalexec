package evalspec

import "encoding/json"

// Usage counts the Judge tokens one evaluation consumed. A failed evaluation
// reports its usage exactly like a successful one — otherwise the tokens
// burned by failures would vanish from the run total.
type Usage struct {
	JudgeInputTokens  int `json:"judge_input_tokens"`
	JudgeOutputTokens int `json:"judge_output_tokens"`
	// JudgeCacheReadTokens is prompt tokens served from the provider's cache.
	// Optional: omitted entirely rather than reported as zero, so records
	// from rule-based Graders carry no spurious counters.
	JudgeCacheReadTokens int `json:"judge_cache_read_tokens,omitempty"`
	// JudgeReasoningTokens is tokens spent on a reasoning model's internal
	// thinking. Without it, usage cannot be reconciled with the bill: a
	// reasoning Judge routinely spends more tokens thinking than answering.
	JudgeReasoningTokens int `json:"judge_reasoning_tokens,omitempty"`
}

// Add accumulates other into u.
func (u *Usage) Add(other Usage) {
	u.JudgeInputTokens += other.JudgeInputTokens
	u.JudgeOutputTokens += other.JudgeOutputTokens
	u.JudgeCacheReadTokens += other.JudgeCacheReadTokens
	u.JudgeReasoningTokens += other.JudgeReasoningTokens
}

// Evidence is one piece of support a Grader cites for its conclusion.
type Evidence struct {
	// Source names the session field the evidence came from, e.g. "output".
	Source string `json:"source"`
	// Path locates the value within that field.
	Path string `json:"path"`
	// Value is the cited value.
	Value any `json:"value"`
}

// EvalError explains why an evaluation failed. It is present exactly when
// Evaluation.Status is EvaluationFail.
type EvalError struct {
	// Code classifies the failure. It never holds CodeSkipped: a skipped
	// sample was never evaluated.
	Code ErrorCode `json:"code"`
	// Message is a human-readable detail. It must not carry a raw Judge
	// response body, which can echo the prompt; that belongs in logs/.
	Message string `json:"message,omitempty"`
}

// RecordError explains why a sample was never evaluated. It appears only on a
// record backfilled after the run stopped.
//
// It is a separate type from EvalError, rather than one struct with four
// optional fields, because the two carry different keys: this one pairs a
// fixed code with the run's stop reason, while EvalError pairs a failure code
// with a message. Merging them would turn "which fields apply here" from a
// type-level fact into a runtime convention.
type RecordError struct {
	// Code is always CodeSkipped.
	Code ErrorCode `json:"code"`
	// Reason matches EvalResult.StopReason: fail_fast or interrupt.
	Reason StopReason `json:"reason"`
}

// Evaluation is the outcome of grading one sample. It is always a single
// object, never an array, and is null only on a skipped record.
type Evaluation struct {
	// Status says whether the evaluation succeeded, not whether the agent
	// performed well.
	Status EvaluationStatus `json:"status"`
	// Score is the Grader's own number. It is null on failure — a failed
	// evaluation is not a zero — and may also be null on success when the
	// Grader gives only a label. EvalExec never compares it to a threshold.
	Score *float64 `json:"score"`
	// Label is the Grader's own categorical verdict, passed through unread.
	Label *string `json:"label"`
	// Reason is the Grader's explanation.
	Reason string `json:"reason,omitempty"`
	// Evidence is what the Grader cited.
	Evidence []Evidence `json:"evidence"`
	// Usage counts Judge tokens, recorded on failures too.
	Usage Usage `json:"usage"`
	// LatencyMS is how long this evaluation took.
	LatencyMS int64 `json:"latency_ms"`
	// Error is set exactly when Status is EvaluationFail.
	Error *EvalError `json:"error"`
}

// NewSuccessEvaluation builds a successful evaluation. score may be nil: a
// Grader that returns only a label is still a success, it just does not
// contribute to the score statistics.
func NewSuccessEvaluation(score *float64, label *string, reason string, evidence []Evidence, usage Usage, latencyMS int64) Evaluation {
	if evidence == nil {
		evidence = []Evidence{}
	}

	return Evaluation{
		Status:    EvaluationSuccess,
		Score:     score,
		Label:     label,
		Reason:    reason,
		Evidence:  evidence,
		Usage:     usage,
		LatencyMS: latencyMS,
		Error:     nil,
	}
}

// NewFailEvaluation builds a failed evaluation.
//
// There is deliberately no score parameter. A failed evaluation must never be
// counted as a zero, and the specification requires the score to be null even
// when the Judge did return a number. Taking the score and discarding it
// internally would leave callers wondering what happened to it; refusing it at
// the signature makes the rule impossible to get wrong.
func NewFailEvaluation(code ErrorCode, message, reason string, evidence []Evidence, usage Usage, latencyMS int64) Evaluation {
	if evidence == nil {
		evidence = []Evidence{}
	}

	return Evaluation{
		Status:    EvaluationFail,
		Score:     nil,
		Label:     nil,
		Reason:    reason,
		Evidence:  evidence,
		Usage:     usage,
		LatencyMS: latencyMS,
		Error:     &EvalError{Code: code, Message: message},
	}
}

// Record is one line of records.jsonl: exactly one per dataset row, always
// carrying the run's eval_id.
type Record struct {
	TaskID string `json:"task_id"`
	EvalID string `json:"eval_id"`
	CaseID string `json:"case_id"`
	// Sequence is the 1-based position of this sample in the dataset. Under
	// concurrency records may be written out of order, so this is what lets a
	// consumer restore the input order.
	Sequence int `json:"sequence"`
	// Status says whether the sample was executed.
	Status RecordStatus `json:"status"`
	// Evaluation is null only when Status is RecordSkipped.
	Evaluation *Evaluation `json:"evaluation"`
	// StartedAt and FinishedAt are null on a skipped record.
	StartedAt  *Timestamp `json:"started_at"`
	FinishedAt *Timestamp `json:"finished_at"`
	// Error is set only on a skipped record.
	Error *RecordError `json:"error"`
}

// NewCompletedRecord builds a record for a sample that reached the Grader,
// whether the evaluation succeeded or failed.
func NewCompletedRecord(taskID, evalID, caseID string, sequence int, eval Evaluation, startedAt, finishedAt Timestamp) Record {
	return Record{
		TaskID:     taskID,
		EvalID:     evalID,
		CaseID:     caseID,
		Sequence:   sequence,
		Status:     RecordCompleted,
		Evaluation: &eval,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		Error:      nil,
	}
}

// NewSkippedRecord builds the backfill record for a sample that never
// produced an evaluation, because fail-fast or an interrupt stopped the run.
// Backfilling calls neither the Grader nor the Judge; it exists so that
// records.jsonl always has exactly one line per dataset row.
func NewSkippedRecord(taskID, evalID, caseID string, sequence int, reason StopReason) Record {
	return Record{
		TaskID:     taskID,
		EvalID:     evalID,
		CaseID:     caseID,
		Sequence:   sequence,
		Status:     RecordSkipped,
		Evaluation: nil,
		StartedAt:  nil,
		FinishedAt: nil,
		Error:      &RecordError{Code: CodeSkipped, Reason: reason},
	}
}

// Counts tallies whether samples were executed.
type Counts struct {
	// Total equals the dataset line count and the records.jsonl line count.
	Total int `json:"total"`
	// Completed is the number of samples handed to the Grader.
	Completed int `json:"completed"`
	// Skipped is the number backfilled after an early stop.
	Skipped int `json:"skipped"`
}

// ScoreStats describes the scores of successful evaluations. It is purely
// descriptive: EvalExec draws no pass/fail conclusion from it.
type ScoreStats struct {
	// Count is how many successful evaluations carried a usable number. It
	// can be lower than the success count when a Grader returns only labels.
	Count int `json:"count"`
	// Mean, Min and Max are all null when Count is zero. They carry no
	// omitempty on purpose: the specification requires the keys to be present
	// with a null value, and omitempty would delete them outright.
	Mean *float64 `json:"mean"`
	Min  *float64 `json:"min"`
	Max  *float64 `json:"max"`
}

// EvaluationSummary aggregates the single Grader's results. There is no
// cross-Grader total, because a run has exactly one Grader.
type EvaluationSummary struct {
	// GraderID and GraderVersion come from the Grader's own declaration
	// rather than from the configuration file.
	GraderID      string `json:"grader_id"`
	GraderVersion string `json:"grader_version"`
	// Evaluated equals Counts.Completed.
	Evaluated int `json:"evaluated"`
	Success   int `json:"success"`
	Fail      int `json:"fail"`
	// FailByCode groups failures by error code so a consumer can tell
	// "insufficient evidence" apart from "the Grader broke". Codes that did
	// not occur are omitted.
	FailByCode map[ErrorCode]int `json:"fail_by_code,omitempty"`
	Score      ScoreStats        `json:"score"`
}

// ModelUsage is the run-level Judge token total.
type ModelUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// ResultUsage holds the run's usage totals.
type ResultUsage struct {
	JudgeModel ModelUsage `json:"judge_model"`
}

// Implementation identifies the build that produced a result.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Provenance is what makes a result traceable. Because a Judge service can
// change server-side, EvalExec promises that the inputs and configuration are
// reproducible — not that the scores are.
type Provenance struct {
	// DatasetSHA256 is over the raw dataset file bytes.
	DatasetSHA256 string `json:"dataset_sha256"`
	// EvalRequestSHA256 is over the redacted, canonicalized request JSON.
	EvalRequestSHA256 string         `json:"eval_request_sha256"`
	Implementation    Implementation `json:"implementation"`
}

// RunError describes a run-level fault, present exactly when Status is
// RunFailed.
type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Artifacts names the files written alongside result.json.
type Artifacts struct {
	Records string `json:"records"`
	// Errors and Logs are optional diagnostics; they are excluded from
	// checksums.sha256, which covers only the stable interface files.
	Errors string `json:"errors,omitempty"`
	Logs   string `json:"logs,omitempty"`
}

// EvalResult is the top-level result of one run.
type EvalResult struct {
	SpecVersion string    `json:"spec_version"`
	EvalID      string    `json:"eval_id"`
	TaskID      string    `json:"task_id"`
	Status      RunStatus `json:"status"`
	// StopReason is non-null exactly when Status is not RunCompleted.
	StopReason *StopReason `json:"stop_reason"`
	// Request is the redacted, path-normalized effective request. Storing it
	// as raw JSON keeps the snapshot byte-identical to what was hashed for
	// provenance.
	Request    json.RawMessage   `json:"request"`
	Artifacts  Artifacts         `json:"artifacts"`
	Counts     Counts            `json:"counts"`
	Evaluation EvaluationSummary `json:"evaluation"`
	Usage      ResultUsage       `json:"usage"`
	Provenance Provenance        `json:"provenance"`
	StartedAt  Timestamp         `json:"started_at"`
	FinishedAt Timestamp         `json:"finished_at"`
	DurationMS int64             `json:"duration_ms"`
	Error      *RunError         `json:"error"`
}
