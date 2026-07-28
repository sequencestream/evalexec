package summary_test

import (
	"testing"
	"time"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/internal/summary"
)

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

func completed(seq int, eval evalspec.Evaluation) *evalspec.Record {
	// Timestamps play no part in the tallies; a fixed one keeps the tests
	// focused on the arithmetic.
	ts := evalspec.NewTimestamp(time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC))
	r := evalspec.NewCompletedRecord("t", "e", "c", seq, eval, ts, ts)

	return &r
}

func skipped(seq int) *evalspec.Record {
	r := evalspec.NewSkippedRecord("t", "e", "c", seq, evalspec.StopFailFast)

	return &r
}

// TestIdentitiesHold is the core check: whatever is fed in, the summary
// satisfies the counting identities that bind sample tallies to evaluation
// tallies.
func TestIdentitiesHold(t *testing.T) {
	a := summary.New()

	a.Add(completed(1, evalspec.NewSuccessEvaluation(f64(1), str("match"), "", nil, evalspec.Usage{}, 0)))
	a.Add(completed(2, evalspec.NewSuccessEvaluation(f64(0), str("mismatch"), "", nil, evalspec.Usage{}, 0)))
	a.Add(completed(3, evalspec.NewFailEvaluation(evalspec.CodeInsufficientEvidence, "", "", nil, evalspec.Usage{}, 0)))
	a.Add(completed(4, evalspec.NewFailEvaluation(evalspec.CodeJudgeError, "", "", nil, evalspec.Usage{}, 0)))
	a.Add(skipped(5))

	counts := a.Counts()
	eval := a.Evaluation("g", "v1")

	res := &evalspec.EvalResult{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      "e",
		TaskID:      "t",
		Status:      evalspec.RunCancelled,
		StopReason:  stopFailFast(),
		Counts:      counts,
		Evaluation:  eval,
	}

	if err := res.Validate(); err != nil {
		t.Fatalf("the accumulated summary violates its own identities: %v", err)
	}

	if counts.Total != 5 || counts.Completed != 4 || counts.Skipped != 1 {
		t.Errorf("counts = %+v, want 5/4/1", counts)
	}

	if eval.Success != 2 || eval.Fail != 2 {
		t.Errorf("success/fail = %d/%d, want 2/2", eval.Success, eval.Fail)
	}

	if eval.FailByCode[evalspec.CodeInsufficientEvidence] != 1 || eval.FailByCode[evalspec.CodeJudgeError] != 1 {
		t.Errorf("fail_by_code = %v", eval.FailByCode)
	}
}

func stopFailFast() *evalspec.StopReason {
	r := evalspec.StopFailFast

	return &r
}

// TestFailuresContributeNoScore is the accounting consequence of "a failure is
// not a zero": the mean is over what was measured, not over what was
// attempted.
func TestFailuresContributeNoScore(t *testing.T) {
	a := summary.New()

	a.Add(completed(1, evalspec.NewSuccessEvaluation(f64(1), nil, "", nil, evalspec.Usage{}, 0)))
	a.Add(completed(2, evalspec.NewFailEvaluation(evalspec.CodeJudgeError, "", "", nil, evalspec.Usage{}, 0)))

	got := a.Evaluation("g", "v1").Score

	if got.Count != 1 {
		t.Fatalf("score.count = %d, want 1", got.Count)
	}

	if got.Mean == nil || *got.Mean != 1 {
		t.Errorf("score.mean = %v, want 1 — a failure must not average in as a zero", got.Mean)
	}
}

// TestScorelessSuccessCountsAsSuccess covers a Grader that returns only a
// label: still a success, simply nothing to average.
func TestScorelessSuccessCountsAsSuccess(t *testing.T) {
	a := summary.New()

	a.Add(completed(1, evalspec.NewSuccessEvaluation(nil, str("faithful"), "", nil, evalspec.Usage{}, 0)))
	a.Add(completed(2, evalspec.NewSuccessEvaluation(f64(0.5), nil, "", nil, evalspec.Usage{}, 0)))

	eval := a.Evaluation("g", "v1")

	if eval.Success != 2 {
		t.Errorf("success = %d, want 2", eval.Success)
	}

	if eval.Score.Count != 1 {
		t.Errorf("score.count = %d, want 1: score.count may be lower than success", eval.Score.Count)
	}
}

// TestUsageIsAccumulatedOnFailuresToo pins the rule the specification calls
// out by name: tokens spent on a failed evaluation were still spent.
func TestUsageIsAccumulatedOnFailuresToo(t *testing.T) {
	a := summary.New()

	a.Add(completed(1, evalspec.NewSuccessEvaluation(f64(1), nil, "", nil,
		evalspec.Usage{JudgeInputTokens: 850, JudgeOutputTokens: 80}, 0)))
	a.Add(completed(2, evalspec.NewFailEvaluation(evalspec.CodeInsufficientEvidence, "", "", nil,
		evalspec.Usage{JudgeInputTokens: 640, JudgeOutputTokens: 32}, 0)))
	a.Add(skipped(3))

	usage := a.Usage()

	if usage.JudgeInputTokens != 1490 {
		t.Errorf("input tokens = %d, want 1490 (850 + 640): a failure's tokens must not vanish", usage.JudgeInputTokens)
	}

	if usage.JudgeOutputTokens != 112 {
		t.Errorf("output tokens = %d, want 112", usage.JudgeOutputTokens)
	}
}

// TestEmptyRunHasNullStatistics covers the zero-row case.
func TestEmptyRunHasNullStatistics(t *testing.T) {
	eval := summary.New().Evaluation("g", "v1")

	if eval.Score.Count != 0 {
		t.Errorf("score.count = %d, want 0", eval.Score.Count)
	}

	if eval.Score.Mean != nil || eval.Score.Min != nil || eval.Score.Max != nil {
		t.Error("with nothing scored, mean, min and max must all be null")
	}

	if eval.FailByCode != nil {
		t.Errorf("fail_by_code = %v, want omitted entirely", eval.FailByCode)
	}
}

func TestScoreRange(t *testing.T) {
	a := summary.New()

	for _, s := range []float64{0.5, 0.1, 0.9, 0.3} {
		a.Add(completed(1, evalspec.NewSuccessEvaluation(f64(s), nil, "", nil, evalspec.Usage{}, 0)))
	}

	got := a.Evaluation("g", "v1").Score

	if *got.Min != 0.1 {
		t.Errorf("min = %v, want 0.1", *got.Min)
	}

	if *got.Max != 0.9 {
		t.Errorf("max = %v, want 0.9", *got.Max)
	}

	if want := (0.5 + 0.1 + 0.9 + 0.3) / 4; *got.Mean != want {
		t.Errorf("mean = %v, want %v", *got.Mean, want)
	}
}

// TestStatusBinding pins the coupling between stopping and the run status.
func TestStatusBinding(t *testing.T) {
	tests := []struct {
		name    string
		skipped int
		stopped bool
		reason  evalspec.StopReason
		want    evalspec.RunStatus
	}{
		{name: "nothing skipped", skipped: 0, want: evalspec.RunCompleted},
		{
			name: "fail-fast", skipped: 3, stopped: true,
			reason: evalspec.StopFailFast, want: evalspec.RunCancelled,
		},
		{
			name: "interrupt", skipped: 1, stopped: true,
			reason: evalspec.StopInterrupt, want: evalspec.RunCancelled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason := summary.Status(tt.skipped, tt.stopped, tt.reason)

			if status != tt.want {
				t.Errorf("status = %q, want %q", status, tt.want)
			}

			if tt.want == evalspec.RunCompleted && reason != nil {
				t.Errorf("stop_reason = %v, want null when completed", *reason)
			}

			if tt.want == evalspec.RunCancelled && (reason == nil || *reason != tt.reason) {
				t.Errorf("stop_reason = %v, want %q", reason, tt.reason)
			}
		})
	}
}
