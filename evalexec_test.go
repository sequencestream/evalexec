package evalexec_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/evalspec/evalspectest"
	"github.com/sequencestream/evalexec/fixtures"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/internal/result"
)

// fixedClock keeps timestamps out of the comparison.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func testClock() fixedClock {
	return fixedClock{t: time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)}
}

// stage copies a fixture case onto disk and returns the request pointing at
// it, with the paths rewritten to the temporary location.
func stage(t *testing.T, caseName string) (*evalspec.EvalRequest, string) {
	t.Helper()

	root := t.TempDir()

	datasetData, err := fixtures.Read(caseName, fixtures.FileDataset)
	if err != nil {
		t.Fatalf("%v", err)
	}

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, datasetData, 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	requestData, err := fixtures.Read(caseName, fixtures.FileRequest)
	if err != nil {
		t.Fatalf("%v", err)
	}

	var req evalspec.EvalRequest
	if err := json.Unmarshal(requestData, &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}

	req.Dataset.Path = datasetPath
	req.OutputDir = filepath.Join(root, "out")

	return &req, root
}

// readResult loads the published result and records.
func readResult(t *testing.T, dir string) (map[string]any, []map[string]any) {
	t.Helper()

	resultData, err := os.ReadFile(filepath.Join(dir, result.FileResult))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal(resultData, &res); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	recordData, err := os.ReadFile(filepath.Join(dir, result.FileRecords))
	if err != nil {
		t.Fatalf("read records: %v", err)
	}

	var records []map[string]any

	for i, line := range fixtures.Lines(recordData) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse record %d: %v", i+1, err)
		}

		records = append(records, rec)
	}

	return res, records
}

// TestGoldenCasesEndToEnd is the golden-file check: the rule-Grader fixtures
// must reproduce their golden results exactly, once the values that
// legitimately vary between runs are normalized away.
func TestGoldenCasesEndToEnd(t *testing.T) {
	// f03 needs a Judge and f04 needs fail-fast; both land later.
	for _, caseName := range []string{fixtures.CaseExactMatchAllPass, fixtures.CaseMixedSuccessFail} {
		t.Run(caseName, func(t *testing.T) {
			req, _ := stage(t, caseName)

			res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if res.Status != evalspec.RunCompleted {
				t.Errorf("status = %q, want completed", res.Status)
			}

			gotResult, gotRecords := readResult(t, req.OutputDir)

			// Acceptance criterion 9: every record carries the run's eval_id.
			if _, err := evalspectest.CheckEvalIDConsistency(gotResult, gotRecords); err != nil {
				t.Errorf("%v", err)
			}

			compareGolden(t, caseName, gotResult, gotRecords)
			assertLineCountIdentity(t, req, gotRecords)
		})
	}
}

func compareGolden(t *testing.T, caseName string, gotResult map[string]any, gotRecords []map[string]any) {
	t.Helper()

	wantResultData, err := fixtures.Read(caseName, fixtures.FileExpectedResult)
	if err != nil {
		t.Fatalf("%v", err)
	}

	wantResult, err := evalspectest.NormalizeJSON(wantResultData)
	if err != nil {
		t.Fatalf("%v", err)
	}

	// The request snapshot is not part of the golden comparison: it embeds
	// absolute paths from the temporary directory, which differ every run by
	// construction.
	got := evalspectest.Normalize(gotResult)
	stripRequest(got)
	stripRequest(wantResult)

	// The dataset digest is compared, because it is taken over the raw file
	// bytes and every implementation must agree on it.
	if diffs := evalspectest.Diff(wantResult, got); len(diffs) != 0 {
		t.Errorf("result.json differs from the golden file:\n%s", strings.Join(diffs, "\n"))
	}

	wantRecordData, err := fixtures.Read(caseName, fixtures.FileExpectedRecord)
	if err != nil {
		t.Fatalf("%v", err)
	}

	wantLines := fixtures.Lines(wantRecordData)
	if len(wantLines) != len(gotRecords) {
		t.Fatalf("wrote %d records, golden file has %d", len(gotRecords), len(wantLines))
	}

	for i, line := range wantLines {
		want, err := evalspectest.NormalizeJSON([]byte(line))
		if err != nil {
			t.Fatalf("record %d: %v", i+1, err)
		}

		if diffs := evalspectest.Diff(want, evalspectest.Normalize(gotRecords[i])); len(diffs) != 0 {
			t.Errorf("record %d differs from the golden file:\n%s", i+1, strings.Join(diffs, "\n"))
		}
	}
}

// stripRequest removes the embedded request snapshot from a normalized result.
func stripRequest(v any) {
	if m, ok := v.(map[string]any); ok {
		delete(m, "request")
	}
}

// assertLineCountIdentity is the assertion every end-to-end test shares:
// records.jsonl has exactly one line per dataset row, and the sequences cover
// 1..n once each.
func assertLineCountIdentity(t *testing.T, req *evalspec.EvalRequest, records []map[string]any) {
	t.Helper()

	datasetData, err := os.ReadFile(req.Dataset.Path)
	if err != nil {
		t.Fatalf("read dataset: %v", err)
	}

	rows := len(fixtures.Lines(datasetData))
	if len(records) != rows {
		t.Fatalf("records.jsonl has %d lines, the dataset has %d rows", len(records), rows)
	}

	seen := make(map[int]bool, rows)

	for i, rec := range records {
		seq, ok := rec["sequence"].(float64)
		if !ok {
			t.Fatalf("record %d has no sequence", i+1)
		}

		n := int(seq)
		if n < 1 || n > rows {
			t.Errorf("record %d has sequence %d, outside 1..%d", i+1, n, rows)
		}

		if seen[n] {
			t.Errorf("sequence %d appears more than once", n)
		}

		seen[n] = true
	}

	if len(seen) != rows {
		t.Errorf("sequences cover %d of %d rows", len(seen), rows)
	}
}

// TestEmptyDatasetProducesAnEmptyResult pins the open question: zero rows is a
// legal run, not an error.
func TestEmptyDatasetProducesAnEmptyResult(t *testing.T) {
	root := t.TempDir()

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCompleted {
		t.Errorf("status = %q, want completed", res.Status)
	}

	if res.Counts.Total != 0 {
		t.Errorf("total = %d, want 0", res.Counts.Total)
	}

	if res.Evaluation.Score.Mean != nil || res.Evaluation.Score.Min != nil || res.Evaluation.Score.Max != nil {
		t.Error("score statistics must all be null with nothing scored")
	}
}

// TestTwoRunsAreIndependent checks that two runs over the same inputs share no
// state: neither result may be influenced by the other having happened.
func TestTwoRunsAreIndependent(t *testing.T) {
	root := t.TempDir()

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(oneMatchingRow), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	first := exactMatchRequest(datasetPath, filepath.Join(root, "out-1"))
	first.EvalID = "eval-first"

	second := exactMatchRequest(datasetPath, filepath.Join(root, "out-2"))
	second.EvalID = "eval-second"

	a, err := evalexec.Run(t.Context(), first, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}

	b, err := evalexec.Run(t.Context(), second, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}

	if a.EvalID == b.EvalID {
		t.Error("two runs must have distinct eval_ids")
	}

	for _, dir := range []string{first.OutputDir, second.OutputDir} {
		if _, err := os.Stat(filepath.Join(dir, result.FileResult)); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}

	// The dataset digest is identical: it depends on the file, not the run.
	if a.Provenance.DatasetSHA256 != b.Provenance.DatasetSHA256 {
		t.Error("the same dataset must hash the same in both runs")
	}

	// The request digests differ, because the output directories do.
	if a.Provenance.EvalRequestSHA256 == b.Provenance.EvalRequestSHA256 {
		t.Error("requests differing in output_dir must hash differently")
	}
}

const oneMatchingRow = `{"case_id":"c1","input":{"q":1},"output":{"a":1},"reference":{"expected_output":{"a":1}}}` + "\n"

func exactMatchRequest(datasetPath, outputDir string) *evalspec.EvalRequest {
	return &evalspec.EvalRequest{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      "eval-test",
		TaskID:      "task-test",
		Dataset:     evalspec.Dataset{Path: datasetPath},
		Grader: evalspec.GraderSpec{
			ID: "g", Version: "v1",
			Protocol: evalspec.GraderBuiltin, Entry: "exact_match",
			Requires: []evalspec.SessionField{
				evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
			},
		},
		Execution: &evalspec.Execution{Concurrency: 1},
		OutputDir: outputDir,
	}
}

// panicGrader models a broken downstream extension.
type panicGrader struct{}

func (panicGrader) Declare() grader.Declaration {
	return grader.Declaration{
		Entry: "panics",
		Requires: []evalspec.SessionField{
			evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
		},
	}
}

func (panicGrader) Grade(context.Context, evalspec.GradeCall) (evalspec.Evaluation, error) {
	panic("this grader is broken")
}

// slowGrader models a Grader that outlasts its timeout.
type slowGrader struct{ delay time.Duration }

func (slowGrader) Declare() grader.Declaration {
	return grader.Declaration{
		Entry: "slow",
		Requires: []evalspec.SessionField{
			evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
		},
	}
}

func (g slowGrader) Grade(ctx context.Context, _ evalspec.GradeCall) (evalspec.Evaluation, error) {
	select {
	case <-time.After(g.delay):
		return evalspec.NewSuccessEvaluation(nil, nil, "eventually", nil, evalspec.Usage{}, 0), nil
	case <-ctx.Done():
		return evalspec.Evaluation{}, ctx.Err()
	}
}

// TestCustomGraderRegistration is the downstream story: register your own
// Grader and run it under protocol "builtin" with your own entry name.
func TestCustomGraderRegistration(t *testing.T) {
	root := t.TempDir()

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(oneMatchingRow), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg := grader.NewRegistry()
	reg.Register("my_custom_grader", func(evalspec.GraderSpec, grader.Deps) (grader.Grader, error) {
		return customGrader{}, nil
	})

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Entry = "my_custom_grader"
	req.Grader.Requires = []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput}

	res, err := evalexec.Run(t.Context(), req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Evaluation.Success != 1 {
		t.Errorf("success = %d, want 1", res.Evaluation.Success)
	}
}

type customGrader struct{}

func (customGrader) Declare() grader.Declaration {
	return grader.Declaration{
		Entry:    "my_custom_grader",
		Requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
	}
}

func (customGrader) Grade(context.Context, evalspec.GradeCall) (evalspec.Evaluation, error) {
	score, label := 0.75, "custom"

	return evalspec.NewSuccessEvaluation(&score, &label, "graded by downstream code", nil, evalspec.Usage{}, 0), nil
}

// TestGraderPanicBecomesOneFailedSample covers the rule that a broken
// extension costs one sample, not the run — and certainly not the process.
func TestGraderPanicBecomesOneFailedSample(t *testing.T) {
	root := t.TempDir()

	rows := oneMatchingRow +
		`{"case_id":"c2","input":{"q":2},"output":{"a":2},"reference":{"expected_output":{"a":2}}}` + "\n"

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(rows), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg := grader.NewRegistry()
	reg.Register("panics", func(evalspec.GraderSpec, grader.Deps) (grader.Grader, error) { return panicGrader{}, nil })

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Entry = "panics"

	res, err := evalexec.Run(t.Context(), req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Both samples were processed; both failed with internal_error.
	if res.Counts.Total != 2 || res.Counts.Completed != 2 {
		t.Errorf("counts = %+v, want 2 completed: one broken Grader call must not stop the others", res.Counts)
	}

	if got := res.Evaluation.FailByCode[evalspec.CodeInternalError]; got != 2 {
		t.Errorf("internal_error count = %d, want 2", got)
	}

	// And the run itself is still completed: it processed everything it was
	// given.
	if res.Status != evalspec.RunCompleted {
		t.Errorf("status = %q, want completed", res.Status)
	}
}

// TestGraderTimeoutIsAFailedEvaluation checks the timeout path produces a
// completed sample with a failed evaluation, not a cancelled one.
func TestGraderTimeoutIsAFailedEvaluation(t *testing.T) {
	root := t.TempDir()

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(oneMatchingRow), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg := grader.NewRegistry()
	reg.Register("slow", func(evalspec.GraderSpec, grader.Deps) (grader.Grader, error) {
		return slowGrader{delay: 5 * time.Second}, nil
	})

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Entry = "slow"
	req.Grader.TimeoutMS = 20

	res, err := evalexec.Run(t.Context(), req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := res.Evaluation.FailByCode[evalspec.CodeTimeout]; got != 1 {
		t.Errorf("timeout count = %d, want 1 (fail_by_code = %v)", got, res.Evaluation.FailByCode)
	}

	// A timed-out sample was processed, so it is completed, not skipped.
	if res.Counts.Completed != 1 || res.Counts.Skipped != 0 {
		t.Errorf("counts = %+v, want 1 completed and 0 skipped", res.Counts)
	}
}

// TestPrecheckFailureLeavesNothingBehind is the atomicity guarantee at the
// library level.
func TestPrecheckFailureLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(`{"case_id":"c1"`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))

	if _, err := evalexec.Run(t.Context(), req); err == nil {
		t.Fatal("a malformed dataset must be rejected")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, e := range entries {
		if e.Name() != "dataset.jsonl" {
			t.Errorf("a rejected run left %s behind", e.Name())
		}
	}
}

// TestFailureIsNeverAZero is the rule the whole status model rests on, checked
// end to end.
func TestFailureIsNeverAZero(t *testing.T) {
	root := t.TempDir()

	// A reference with no expected_output: the Grader cannot conclude.
	rows := `{"case_id":"c0","input":{},"output":{"a":1},"reference":{"note":"none"}}` + "\n" +
		oneMatchingRow

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(rows), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Evaluation.Fail != 1 || res.Evaluation.Success != 1 {
		t.Fatalf("want one failure and one success, got %+v", res.Evaluation)
	}

	// The failure contributed nothing to the average: one score of 1, not a
	// mean of 0.5 dragged down by a zero nobody measured.
	if res.Evaluation.Score.Count != 1 {
		t.Errorf("score.count = %d, want 1", res.Evaluation.Score.Count)
	}

	if res.Evaluation.Score.Mean == nil || *res.Evaluation.Score.Mean != 1 {
		t.Errorf("score.mean = %v, want 1", res.Evaluation.Score.Mean)
	}

	_, records := readResult(t, req.OutputDir)

	for _, rec := range records {
		eval, ok := rec["evaluation"].(map[string]any)
		if !ok {
			t.Fatal("record has no evaluation")
		}

		if eval["status"] == "fail" && eval["score"] != nil {
			t.Errorf("a failed evaluation carries score %v, want null", eval["score"])
		}
	}
}

// TestRunIsAtomicOnSummaryFailure checks that a result which fails its own
// invariants is not published.
func TestRunIsAtomicOnSummaryFailure(t *testing.T) {
	root := t.TempDir()

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(oneMatchingRow), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg := grader.NewRegistry()
	reg.Register("bad_shape", func(evalspec.GraderSpec, grader.Deps) (grader.Grader, error) {
		return badShapeGrader{}, nil
	})

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Entry = "bad_shape"

	_, err := evalexec.Run(t.Context(), req, evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock()))
	if err == nil {
		t.Fatal("a Grader producing an invalid evaluation must fail the run")
	}

	if _, statErr := os.Stat(req.OutputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a failed run must not publish a directory (stat err = %v)", statErr)
	}
}

// badShapeGrader returns an evaluation violating the fail/score invariant, as
// downstream code eventually will.
type badShapeGrader struct{}

func (badShapeGrader) Declare() grader.Declaration {
	return grader.Declaration{
		Entry: "bad_shape",
		Requires: []evalspec.SessionField{
			evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
		},
	}
}

func (badShapeGrader) Grade(context.Context, evalspec.GradeCall) (evalspec.Evaluation, error) {
	zero := 0.0

	// A failure carrying a score: exactly what the constructors prevent and
	// hand-built structs do not.
	return evalspec.Evaluation{
		Status: evalspec.EvaluationFail,
		Score:  &zero,
		Error:  &evalspec.EvalError{Code: evalspec.CodeInsufficientEvidence},
	}, nil
}

// TestOutputDirectoryIsNotOverwritten checks the refusal to write over an
// existing result through the library entry point.
func TestOutputDirectoryIsNotOverwritten(t *testing.T) {
	root := t.TempDir()

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(oneMatchingRow), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))

	if _, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock())); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	before, err := os.ReadFile(filepath.Join(req.OutputDir, result.FileResult))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if _, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock())); err == nil {
		t.Fatal("a second run into the same directory must be refused")
	}

	after, err := os.ReadFile(filepath.Join(req.OutputDir, result.FileResult))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(before) != string(after) {
		t.Error("the existing result was modified")
	}
}
