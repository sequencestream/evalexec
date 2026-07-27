package evalspec_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
)

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

// TestNewFailEvaluationHasNoScore guards the rule that matters most in this
// package: a failed evaluation is never a zero. The constructor takes no score
// argument at all, so this asserts the resulting shape.
func TestNewFailEvaluationHasNoScore(t *testing.T) {
	e := evalspec.NewFailEvaluation(evalspec.CodeJudgeError, "boom", "judge unreachable", nil, evalspec.Usage{}, 12)

	if e.Score != nil {
		t.Errorf("Score = %v, want nil", *e.Score)
	}

	if e.Label != nil {
		t.Errorf("Label = %v, want nil", *e.Label)
	}

	if e.Status != evalspec.EvaluationFail {
		t.Errorf("Status = %q, want fail", e.Status)
	}

	if e.Error == nil || e.Error.Code != evalspec.CodeJudgeError {
		t.Errorf("Error = %+v, want a judge_error", e.Error)
	}

	// Usage is recorded on failures too, or the tokens a failed evaluation
	// burned would disappear from the run total.
	e2 := evalspec.NewFailEvaluation(evalspec.CodeTimeout, "", "", nil, evalspec.Usage{JudgeInputTokens: 640, JudgeOutputTokens: 32}, 180)
	if e2.Usage.JudgeInputTokens != 640 {
		t.Error("usage must be recorded on a failed evaluation")
	}
}

func TestNewSuccessEvaluationAllowsScorelessSuccess(t *testing.T) {
	// A Grader returning only a label is still a success; it simply does not
	// contribute to the score statistics.
	e := evalspec.NewSuccessEvaluation(nil, str("faithful"), "", nil, evalspec.Usage{}, 5)

	if e.Status != evalspec.EvaluationSuccess {
		t.Errorf("Status = %q, want success", e.Status)
	}

	if e.Score != nil {
		t.Error("Score must stay nil when none was given")
	}

	if e.Error != nil {
		t.Error("Error must be nil on success")
	}
}

func TestNewSkippedRecordShape(t *testing.T) {
	r := evalspec.NewSkippedRecord("task-1", "eval-1", "case-100", 100, evalspec.StopFailFast)

	if r.Status != evalspec.RecordSkipped {
		t.Errorf("Status = %q, want skipped", r.Status)
	}

	if r.Evaluation != nil {
		t.Error("Evaluation must be null on a skipped record")
	}

	if r.StartedAt != nil || r.FinishedAt != nil {
		t.Error("timestamps must be null on a skipped record")
	}

	if r.Error == nil || r.Error.Code != evalspec.CodeSkipped || r.Error.Reason != evalspec.StopFailFast {
		t.Errorf("Error = %+v, want {skipped, fail_fast}", r.Error)
	}

	if err := r.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestRecordValidateRejectsBrokenInvariants drives each invariant negative,
// because a validator that only ever sees valid input proves nothing.
func TestRecordValidateRejectsBrokenInvariants(t *testing.T) {
	ts := evalspec.NewTimestamp(mustTime(t, "2026-07-27T01:00:00Z"))

	tests := []struct {
		name     string
		record   evalspec.Record
		wantPath string
	}{
		{
			name: "failed evaluation carrying a score",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordCompleted,
				StartedAt: &ts, FinishedAt: &ts,
				Evaluation: &evalspec.Evaluation{
					Status: evalspec.EvaluationFail,
					Score:  f64(0), // the classic mistake: recording a failure as a zero
					Error:  &evalspec.EvalError{Code: evalspec.CodeJudgeError},
				},
			},
			wantPath: "evaluation.score",
		},
		{
			name: "failed evaluation without an error",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordCompleted,
				StartedAt: &ts, FinishedAt: &ts,
				Evaluation: &evalspec.Evaluation{Status: evalspec.EvaluationFail},
			},
			wantPath: "evaluation.error",
		},
		{
			name: "failed evaluation coded as skipped",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordCompleted,
				StartedAt: &ts, FinishedAt: &ts,
				Evaluation: &evalspec.Evaluation{
					Status: evalspec.EvaluationFail,
					Error:  &evalspec.EvalError{Code: evalspec.CodeSkipped},
				},
			},
			wantPath: "evaluation.error.code",
		},
		{
			name: "successful evaluation carrying an error",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordCompleted,
				StartedAt: &ts, FinishedAt: &ts,
				Evaluation: &evalspec.Evaluation{
					Status: evalspec.EvaluationSuccess,
					Error:  &evalspec.EvalError{Code: evalspec.CodeTimeout},
				},
			},
			wantPath: "evaluation.error",
		},
		{
			name: "skipped record carrying an evaluation",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordSkipped,
				Evaluation: &evalspec.Evaluation{Status: evalspec.EvaluationSuccess},
				Error:      &evalspec.RecordError{Code: evalspec.CodeSkipped, Reason: evalspec.StopInterrupt},
			},
			wantPath: "evaluation",
		},
		{
			name: "completed record without an evaluation",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordCompleted,
				StartedAt: &ts, FinishedAt: &ts,
			},
			wantPath: "evaluation",
		},
		{
			name: "skipped record without an error",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordSkipped,
			},
			wantPath: "error",
		},
		{
			name: "skipped record with a stop reason of error",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 1, Status: evalspec.RecordSkipped,
				Error: &evalspec.RecordError{Code: evalspec.CodeSkipped, Reason: evalspec.StopError},
			},
			wantPath: "error.reason",
		},
		{
			name: "sequence is not 1-based",
			record: evalspec.Record{
				EvalID: "e", CaseID: "c", Sequence: 0, Status: evalspec.RecordSkipped,
				Error: &evalspec.RecordError{Code: evalspec.CodeSkipped, Reason: evalspec.StopFailFast},
			},
			wantPath: "sequence",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.record.Validate()
			if err == nil {
				t.Fatal("Validate returned nil, want an error")
			}

			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("Validate = %q, want a complaint about %q", err, tt.wantPath)
			}
		})
	}
}

// validResult is a minimal EvalResult satisfying every identity, used as the
// base for negative cases.
func validResult() evalspec.EvalResult {
	return evalspec.EvalResult{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      "eval-1",
		TaskID:      "task-1",
		Status:      evalspec.RunCompleted,
		Counts:      evalspec.Counts{Total: 10, Completed: 10, Skipped: 0},
		Evaluation: evalspec.EvaluationSummary{
			GraderID: "g", GraderVersion: "v1",
			Evaluated: 10, Success: 8, Fail: 2,
			FailByCode: map[evalspec.ErrorCode]int{
				evalspec.CodeInsufficientEvidence: 1,
				evalspec.CodeJudgeError:           1,
			},
			Score: evalspec.ScoreStats{Count: 8, Mean: f64(0.75), Min: f64(0), Max: f64(1)},
		},
	}
}

func TestEvalResultValidateAcceptsValid(t *testing.T) {
	r := validResult()
	if err := r.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestEvalResultCountIdentities breaks each identity in turn. These are the
// identities that make every denominator in a result explicit; the result
// writer refuses to write a result that fails them.
func TestEvalResultCountIdentities(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*evalspec.EvalResult)
		wantPath string
	}{
		{
			name:     "total != completed + skipped",
			mutate:   func(r *evalspec.EvalResult) { r.Counts.Total = 11 },
			wantPath: "counts.total",
		},
		{
			name:     "evaluated != completed",
			mutate:   func(r *evalspec.EvalResult) { r.Evaluation.Evaluated = 9 },
			wantPath: "evaluation.evaluated",
		},
		{
			name:     "evaluated != success + fail",
			mutate:   func(r *evalspec.EvalResult) { r.Evaluation.Success = 7 },
			wantPath: "evaluation.evaluated",
		},
		{
			name: "fail != sum(fail_by_code)",
			mutate: func(r *evalspec.EvalResult) {
				r.Evaluation.FailByCode = map[evalspec.ErrorCode]int{evalspec.CodeTimeout: 1}
			},
			wantPath: "evaluation.fail",
		},
		{
			name:     "score.count > success",
			mutate:   func(r *evalspec.EvalResult) { r.Evaluation.Score.Count = 9 },
			wantPath: "evaluation.score.count",
		},
		{
			name: "fail_by_code keyed by skipped",
			mutate: func(r *evalspec.EvalResult) {
				r.Evaluation.FailByCode = map[evalspec.ErrorCode]int{evalspec.CodeSkipped: 2}
			},
			wantPath: "evaluation.fail_by_code",
		},
		{
			name:     "negative count",
			mutate:   func(r *evalspec.EvalResult) { r.Counts.Skipped = -1; r.Counts.Completed = 11 },
			wantPath: "counts.skipped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validResult()
			tt.mutate(&r)

			err := r.Validate()
			if err == nil {
				t.Fatal("Validate returned nil, want an error")
			}

			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("Validate = %q, want a complaint about %q", err, tt.wantPath)
			}
		})
	}
}

// TestEvalResultStatusBinding covers the coupling of status, stop reason and
// skipped count: completed implies no skips, cancelled implies some.
func TestEvalResultStatusBinding(t *testing.T) {
	failFast := evalspec.StopFailFast

	tests := []struct {
		name     string
		mutate   func(*evalspec.EvalResult)
		wantPath string
		wantOK   bool
	}{
		{
			name:     "completed with a stop reason",
			mutate:   func(r *evalspec.EvalResult) { r.StopReason = &failFast },
			wantPath: "stop_reason",
		},
		{
			name: "completed with skipped samples",
			mutate: func(r *evalspec.EvalResult) {
				r.Counts.Skipped = 2
				r.Counts.Completed = 8
				r.Evaluation.Evaluated = 8
				r.Evaluation.Success = 6
			},
			wantPath: "counts.skipped",
		},
		{
			name: "cancelled without any skipped samples",
			mutate: func(r *evalspec.EvalResult) {
				r.Status = evalspec.RunCancelled
				r.StopReason = &failFast
			},
			wantPath: "counts.skipped",
		},
		{
			name:     "cancelled without a stop reason",
			mutate:   func(r *evalspec.EvalResult) { r.Status = evalspec.RunCancelled; r.Counts.Skipped = 0 },
			wantPath: "stop_reason",
		},
		{
			name:     "failed without an error",
			mutate:   func(r *evalspec.EvalResult) { r.Status = evalspec.RunFailed },
			wantPath: "error",
		},
		{
			name: "cancelled, properly formed",
			mutate: func(r *evalspec.EvalResult) {
				r.Status = evalspec.RunCancelled
				r.StopReason = &failFast
				r.Counts.Completed, r.Counts.Skipped = 6, 4
				r.Evaluation.Evaluated, r.Evaluation.Success = 6, 4
				r.Evaluation.Score.Count = 4
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validResult()
			tt.mutate(&r)

			err := r.Validate()

			if tt.wantOK {
				if err != nil {
					t.Errorf("Validate: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatal("Validate returned nil, want an error")
			}

			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("Validate = %q, want a complaint about %q", err, tt.wantPath)
			}
		})
	}
}

func TestEvalResultScoreStatsAllOrNothing(t *testing.T) {
	tests := []struct {
		name     string
		score    evalspec.ScoreStats
		success  int
		wantPath string
		wantOK   bool
	}{
		{
			name:   "no scored samples means no statistics",
			score:  evalspec.ScoreStats{Count: 0},
			wantOK: true,
		},
		{
			name:     "no scored samples but a mean",
			score:    evalspec.ScoreStats{Count: 0, Mean: f64(0.5)},
			wantPath: "evaluation.score.mean",
		},
		{
			name:     "scored samples without a mean",
			score:    evalspec.ScoreStats{Count: 4, Min: f64(0), Max: f64(1)},
			wantPath: "evaluation.score.mean",
		},
		{
			name:     "min above max",
			score:    evalspec.ScoreStats{Count: 4, Mean: f64(0.5), Min: f64(1), Max: f64(0)},
			wantPath: "evaluation.score.min",
		},
		{
			name:     "mean outside the range",
			score:    evalspec.ScoreStats{Count: 4, Mean: f64(2), Min: f64(0), Max: f64(1)},
			wantPath: "evaluation.score.mean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validResult()
			r.Evaluation.Score = tt.score

			err := r.Validate()

			if tt.wantOK {
				if err != nil {
					t.Errorf("Validate: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatal("Validate returned nil, want an error")
			}

			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("Validate = %q, want a complaint about %q", err, tt.wantPath)
			}
		})
	}
}

// TestScoreStatsMarshalsNullsNotOmissions guards the omitempty trap. The
// specification says mean, min and max are null when there are no scores —
// the keys must exist with a null value, not vanish.
func TestScoreStatsMarshalsNullsNotOmissions(t *testing.T) {
	data, err := json.Marshal(evalspec.ScoreStats{Count: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{"count":0,"mean":null,"min":null,"max":null}`
	if string(data) != want {
		t.Errorf("ScoreStats{} = %s\nwant %s\n(omitempty would delete the keys entirely)", data, want)
	}
}

// TestEvaluationMarshalsNullScore covers the same trap on the record side.
func TestEvaluationMarshalsNullScore(t *testing.T) {
	e := evalspec.NewFailEvaluation(evalspec.CodeTimeout, "deadline exceeded", "", nil, evalspec.Usage{}, 60000)

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, want := range []string{`"score":null`, `"label":null`, `"status":"fail"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("marshalled evaluation lacks %s: %s", want, data)
		}
	}

	// The optional usage counters must be absent, not zero, when unused.
	if strings.Contains(string(data), "judge_reasoning_tokens") {
		t.Errorf("unused optional usage counters must be omitted: %s", data)
	}
}

func TestEvalRequestValidate(t *testing.T) {
	valid := func() evalspec.EvalRequest {
		return evalspec.EvalRequest{
			SpecVersion: evalspec.SpecVersion,
			TaskID:      "task-1",
			Dataset:     evalspec.Dataset{Path: "/tmp/sessions.jsonl"},
			Grader: evalspec.GraderSpec{
				ID: "g", Version: "v1",
				Protocol: evalspec.GraderBuiltin, Entry: "exact_match",
				Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference},
				RequiresJudge: false,
			},
			OutputDir: "/tmp/out",
		}
	}

	base := valid()
	if err := base.Validate(); err != nil {
		t.Fatalf("a valid request must pass: %v", err)
	}

	tests := []struct {
		name     string
		mutate   func(*evalspec.EvalRequest)
		wantPath string
	}{
		{"wrong spec version", func(r *evalspec.EvalRequest) { r.SpecVersion = "evalexec/v2" }, "spec_version"},
		{"empty task id", func(r *evalspec.EvalRequest) { r.TaskID = "" }, "task_id"},
		{"empty dataset path", func(r *evalspec.EvalRequest) { r.Dataset.Path = "" }, "dataset.path"},
		{"empty output dir", func(r *evalspec.EvalRequest) { r.OutputDir = "" }, "output_dir"},
		{"empty grader id", func(r *evalspec.EvalRequest) { r.Grader.ID = "" }, "grader.id"},
		{"unknown grader protocol", func(r *evalspec.EvalRequest) { r.Grader.Protocol = "grpc" }, "grader.protocol"},
		{
			name:     "requires not declared",
			mutate:   func(r *evalspec.EvalRequest) { r.Grader.Requires = nil },
			wantPath: "grader.requires",
		},
		{
			name: "requires holds an invalid element",
			mutate: func(r *evalspec.EvalRequest) {
				r.Grader.Requires = []evalspec.SessionField{"case_id"}
			},
			wantPath: "grader.requires[0]",
		},
		{
			name: "requires repeats an element",
			mutate: func(r *evalspec.EvalRequest) {
				r.Grader.Requires = []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldInput}
			},
			wantPath: "grader.requires[1]",
		},
		{
			name:     "requires_judge without a judge model",
			mutate:   func(r *evalspec.EvalRequest) { r.Grader.RequiresJudge = true },
			wantPath: "judge_model",
		},
		{
			name: "bearer_env without an env name",
			mutate: func(r *evalspec.EvalRequest) {
				r.Grader.RequiresJudge = true
				r.JudgeModel = &evalspec.JudgeModelSpec{
					Protocol: evalspec.JudgeOpenAIChat,
					Auth:     evalspec.Auth{Type: evalspec.AuthBearerEnv},
				}
			},
			wantPath: "judge_model.auth.env",
		},
		{
			name: "auth none with an env name",
			mutate: func(r *evalspec.EvalRequest) {
				r.Grader.RequiresJudge = true
				r.JudgeModel = &evalspec.JudgeModelSpec{
					Protocol: evalspec.JudgeOpenAIChat,
					Auth:     evalspec.Auth{Type: evalspec.AuthNone, Env: "KEY"},
				}
			},
			wantPath: "judge_model.auth.env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := valid()
			tt.mutate(&r)

			err := r.Validate()
			if err == nil {
				t.Fatal("Validate returned nil, want an error")
			}

			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("Validate = %q, want a complaint about %q", err, tt.wantPath)
			}
		})
	}
}

// TestValidateReportsEveryProblem checks that validation aggregates rather
// than stopping at the first failure: someone fixing a request wants the
// whole list.
func TestValidateReportsEveryProblem(t *testing.T) {
	r := evalspec.EvalRequest{SpecVersion: "wrong"}

	err := r.Validate()
	if err == nil {
		t.Fatal("Validate returned nil")
	}

	var errs evalspec.ValidationErrors
	if !errorsAs(err, &errs) {
		t.Fatalf("Validate returned %T, want ValidationErrors", err)
	}

	if len(errs) < 5 {
		t.Errorf("got %d problems, want at least 5 reported in one pass: %v", len(errs), errs)
	}
}

func TestValidationErrorsOrNil(t *testing.T) {
	var empty evalspec.ValidationErrors

	// A typed nil slice returned as an error would produce a non-nil error
	// interface — the classic Go trap this method exists to avoid.
	if err := empty.OrNil(); err != nil {
		t.Errorf("OrNil on an empty slice = %v, want nil", err)
	}
}
