package evalexec_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/evalspec/evalspectest"
	"github.com/sequencestream/evalexec/fixtures"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/internal/result"
)

// TestFailFastCaseEndToEnd runs f04 and compares against the golden files.
//
// Fail-fast is what makes a partial result trustworthy rather than truncated:
// the three samples never dispatched are backfilled as skipped, so
// records.jsonl still has one line per dataset row.
func TestFailFastCaseEndToEnd(t *testing.T) {
	req, _ := stage(t, fixtures.CaseFailFastCancelled)

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCancelled {
		t.Errorf("status = %q, want cancelled", res.Status)
	}

	if res.StopReason == nil || *res.StopReason != evalspec.StopFailFast {
		t.Errorf("stop_reason = %v, want fail_fast", res.StopReason)
	}

	gotResult, gotRecords := readResult(t, req.OutputDir)

	if _, err := evalspectest.CheckEvalIDConsistency(gotResult, gotRecords); err != nil {
		t.Errorf("%v", err)
	}

	compareGolden(t, fixtures.CaseFailFastCancelled, gotResult, gotRecords)
	assertLineCountIdentity(t, req, gotRecords)
}

// TestFailFastExitsZero pins the mapping most often written backwards.
// Fail-fast is a stopping policy the caller asked for, so
// the command did what it was told; the incompleteness is reported by status
// and counts, not by the exit code.
func TestFailFastExitsZero(t *testing.T) {
	req, _ := stage(t, fixtures.CaseFailFastCancelled)

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := exitCodeOf(res); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}

// TestFailFastIgnoresLowScores pins the rule that only a failed evaluation
// stops a run. EvalExec does not interpret scores, so it has no basis for
// deciding that zero is bad.
func TestFailFastIgnoresLowScores(t *testing.T) {
	root := t.TempDir()

	// Every sample mismatches, so every score is 0 and every evaluation
	// succeeds.
	var rows strings.Builder

	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&rows,
			`{"case_id":"c%d","input":{"q":%d},"output":{"a":"wrong"},"reference":{"expected_output":{"a":"right"}}}`+"\n",
			i, i)
	}

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(rows.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Execution.FailFast = true

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCompleted {
		t.Errorf("status = %q, want completed: a score of 0 must not trigger fail-fast", res.Status)
	}

	if res.Counts.Completed != 5 || res.Counts.Skipped != 0 {
		t.Errorf("counts = %+v, want all five processed", res.Counts)
	}

	if res.Evaluation.Success != 5 {
		t.Errorf("success = %d, want 5: every comparison reached a conclusion", res.Evaluation.Success)
	}
}

// TestLineCountIdentityAtEveryConcurrency is the invariant that has to hold
// however many workers there are.
func TestLineCountIdentityAtEveryConcurrency(t *testing.T) {
	const rows = 50

	for _, concurrency := range []int{1, 2, 4, 16} {
		t.Run(fmt.Sprintf("concurrency-%d", concurrency), func(t *testing.T) {
			root := t.TempDir()
			datasetPath := writeMixedDataset(t, root, rows)

			req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
			req.Execution.Concurrency = concurrency

			res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if res.Counts.Total != rows {
				t.Errorf("total = %d, want %d", res.Counts.Total, rows)
			}

			_, records := readResult(t, req.OutputDir)
			assertLineCountIdentity(t, req, records)

			// The tallies must agree whatever order the records arrived in.
			if res.Counts.Completed != rows {
				t.Errorf("completed = %d, want %d", res.Counts.Completed, rows)
			}

			if res.Evaluation.Evaluated != res.Evaluation.Success+res.Evaluation.Fail {
				t.Errorf("evaluated %d != success %d + fail %d",
					res.Evaluation.Evaluated, res.Evaluation.Success, res.Evaluation.Fail)
			}
		})
	}
}

// TestResultsAreEquivalentAcrossConcurrency checks that concurrency changes the
// order records are written and nothing else.
//
// Row order is deliberately not guaranteed — the specification says a consumer
// sorts by sequence — so the comparison sorts first. Asserting on order would
// be testing something the protocol does not promise.
func TestResultsAreEquivalentAcrossConcurrency(t *testing.T) {
	const rows = 30

	byConcurrency := make(map[int][]map[string]any)

	for _, concurrency := range []int{1, 8} {
		root := t.TempDir()
		datasetPath := writeMixedDataset(t, root, rows)

		req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
		req.Execution.Concurrency = concurrency

		if _, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock())); err != nil {
			t.Fatalf("Run at concurrency %d: %v", concurrency, err)
		}

		_, records := readResult(t, req.OutputDir)
		sortBySequence(records)
		byConcurrency[concurrency] = records
	}

	serial, parallel := byConcurrency[1], byConcurrency[8]
	if len(serial) != len(parallel) {
		t.Fatalf("serial produced %d records, parallel %d", len(serial), len(parallel))
	}

	for i := range serial {
		want := evalspectest.Normalize(serial[i])
		got := evalspectest.Normalize(parallel[i])

		if diffs := evalspectest.Diff(want, got); len(diffs) != 0 {
			t.Errorf("record %d differs between concurrency 1 and 8:\n%s", i+1, strings.Join(diffs, "\n"))
		}
	}
}

// TestOneFailureDoesNotStopTheOthers covers the default: without fail-fast, a
// sample that cannot be evaluated costs only itself.
func TestOneFailureDoesNotStopTheOthers(t *testing.T) {
	root := t.TempDir()
	datasetPath := writeMixedDataset(t, root, 20)

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Execution.Concurrency = 4

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Counts.Skipped != 0 {
		t.Errorf("skipped = %d, want 0 without fail-fast", res.Counts.Skipped)
	}

	if res.Evaluation.Fail == 0 {
		t.Fatal("the dataset should have produced failures")
	}

	if res.Evaluation.Success == 0 {
		t.Fatal("the dataset should have produced successes")
	}

	if res.Status != evalspec.RunCompleted {
		t.Errorf("status = %q, want completed", res.Status)
	}
}

// countingGrader records how many samples it was asked about, so a test can
// tell dispatch actually stopped rather than merely finishing quickly.
type countingGrader struct {
	graded atomic.Int64
	// failAt makes exactly one sample fail, to trigger fail-fast at a
	// predictable point.
	failAt string
	delay  time.Duration
}

func (g *countingGrader) Declare() grader.Declaration {
	return grader.Declaration{
		Entry:    "counting",
		Requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference},
	}
}

func (g *countingGrader) Grade(ctx context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	if g.delay > 0 {
		select {
		case <-time.After(g.delay):
		case <-ctx.Done():
			return evalspec.Evaluation{}, ctx.Err()
		}
	}

	g.graded.Add(1)

	if call.CaseID == g.failAt {
		return evalspec.NewFailEvaluation(evalspec.CodeInsufficientEvidence,
			"asked to fail", "", nil, evalspec.Usage{}, 0), nil
	}

	score := 1.0

	return evalspec.NewSuccessEvaluation(&score, nil, "", nil, evalspec.Usage{}, 0), nil
}

// TestFailFastStopsDispatch checks that stopping actually stops work, not just
// the bookkeeping: with 200 rows and a failure at row 2, the Grader must not
// see all 200.
func TestFailFastStopsDispatch(t *testing.T) {
	const rows = 200

	root := t.TempDir()
	datasetPath := writeMatchingDataset(t, root, rows)

	g := &countingGrader{failAt: "c2", delay: time.Millisecond}

	reg := grader.NewRegistry()
	reg.Register("counting", func(evalspec.GraderSpec, grader.Deps) (grader.Grader, error) { return g, nil })

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Entry = "counting"
	req.Execution.Concurrency = 2
	req.Execution.FailFast = true

	res, err := evalexec.Run(t.Context(), req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCancelled {
		t.Fatalf("status = %q, want cancelled", res.Status)
	}

	if graded := g.graded.Load(); graded >= rows {
		t.Errorf("the Grader saw %d of %d samples; dispatch did not stop", graded, rows)
	}

	if res.Counts.Skipped == 0 {
		t.Error("skipped = 0, want some samples backfilled")
	}

	// The identity holds regardless of where it stopped.
	_, records := readResult(t, req.OutputDir)
	assertLineCountIdentity(t, req, records)

	// Every backfilled record has the fixed shape, and none of them is a
	// failure: a sample that was never evaluated is skipped, not failed.
	for _, rec := range records {
		if rec["status"] != "skipped" {
			continue
		}

		if rec["evaluation"] != nil {
			t.Errorf("skipped record %v carries an evaluation", rec["case_id"])
		}

		errObj, ok := rec["error"].(map[string]any)
		if !ok {
			t.Errorf("skipped record %v has no error", rec["case_id"])

			continue
		}

		if errObj["code"] != "skipped" || errObj["reason"] != "fail_fast" {
			t.Errorf("skipped record error = %v, want {skipped, fail_fast}", errObj)
		}
	}
}

// TestCancellationProducesSkippedNotFailed is the library-level version of the
// interrupt path, and the single easiest thing to get wrong: a sample abandoned
// mid-flight was never finished, so it is skipped rather than failed.
func TestCancellationProducesSkippedNotFailed(t *testing.T) {
	const rows = 100

	root := t.TempDir()
	datasetPath := writeMatchingDataset(t, root, rows)

	g := &countingGrader{delay: 20 * time.Millisecond}

	reg := grader.NewRegistry()
	reg.Register("counting", func(evalspec.GraderSpec, grader.Deps) (grader.Grader, error) { return g, nil })

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Entry = "counting"
	req.Execution.Concurrency = 4

	ctx, cancel := context.WithCancel(t.Context())

	// Cancel once a few samples have been graded, so there is genuinely work in
	// flight rather than a run that never started.
	go func() {
		for g.graded.Load() < 4 {
			time.Sleep(time.Millisecond)
		}

		cancel()
	}()

	res, err := evalexec.Run(ctx, req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCancelled {
		t.Fatalf("status = %q, want cancelled", res.Status)
	}

	if res.StopReason == nil || *res.StopReason != evalspec.StopInterrupt {
		t.Errorf("stop_reason = %v, want interrupt", res.StopReason)
	}

	if res.Counts.Skipped == 0 {
		t.Error("skipped = 0, want the abandoned samples backfilled")
	}

	_, records := readResult(t, req.OutputDir)
	assertLineCountIdentity(t, req, records)

	// No record may be a failure. A cancelled Grader call returns a context
	// error, and recording that as a timeout or an internal error would report
	// work that never happened as work that happened badly.
	for _, rec := range records {
		eval, ok := rec["evaluation"].(map[string]any)
		if !ok {
			continue
		}

		if eval["status"] == "fail" {
			t.Errorf("record %v is a failure; a cancelled sample must be skipped: %v",
				rec["case_id"], eval["error"])
		}
	}
}

// TestCancelledRunStillPublishes checks that an interrupted run leaves a
// complete, verifiable directory rather than nothing.
func TestCancelledRunStillPublishes(t *testing.T) {
	root := t.TempDir()
	datasetPath := writeMatchingDataset(t, root, 40)

	g := &countingGrader{delay: 10 * time.Millisecond}

	reg := grader.NewRegistry()
	reg.Register("counting", func(evalspec.GraderSpec, grader.Deps) (grader.Grader, error) { return g, nil })

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Entry = "counting"
	req.Execution.Concurrency = 2

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		for g.graded.Load() < 2 {
			time.Sleep(time.Millisecond)
		}

		cancel()
	}()

	if _, err := evalexec.Run(ctx, req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock())); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, name := range []string{result.FileResult, result.FileRecords, result.FileChecksums} {
		if _, err := os.Stat(filepath.Join(req.OutputDir, name)); err != nil {
			t.Errorf("%s missing from an interrupted run: %v", name, err)
		}
	}

	// And no temporary directory survived.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("a temporary directory was left behind: %s", e.Name())
		}
	}
}

// writeMixedDataset produces rows that alternate between matching, mismatching
// and unevaluable, so a run exercises all three outcomes.
func writeMixedDataset(t *testing.T, dir string, rows int) string {
	t.Helper()

	var b strings.Builder

	for i := 1; i <= rows; i++ {
		switch i % 3 {
		case 0:
			// No expected value: the Grader cannot conclude.
			fmt.Fprintf(&b, `{"case_id":"c%d","input":{"q":%d},"output":{"a":%d},"reference":{"note":"none"}}`+"\n", i, i, i)
		case 1:
			fmt.Fprintf(&b, `{"case_id":"c%d","input":{"q":%d},"output":{"a":%d},"reference":{"expected_output":{"a":%d}}}`+"\n", i, i, i, i)
		default:
			fmt.Fprintf(&b, `{"case_id":"c%d","input":{"q":%d},"output":{"a":%d},"reference":{"expected_output":{"a":%d}}}`+"\n", i, i, i, i+1)
		}
	}

	path := filepath.Join(dir, "dataset.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	return path
}

// writeMatchingDataset produces rows that all match.
func writeMatchingDataset(t *testing.T, dir string, rows int) string {
	t.Helper()

	var b strings.Builder

	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&b, `{"case_id":"c%d","input":{"q":%d},"output":{"a":%d},"reference":{"expected_output":{"a":%d}}}`+"\n", i, i, i, i)
	}

	path := filepath.Join(dir, "dataset.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	return path
}

func sortBySequence(records []map[string]any) {
	sort.Slice(records, func(i, j int) bool {
		a, _ := records[i]["sequence"].(float64)
		b, _ := records[j]["sequence"].(float64)

		return a < b
	})
}

// exitCodeOf mirrors what the command does with a result.
func exitCodeOf(res *evalspec.EvalResult) int {
	switch {
	case res.Status == evalspec.RunCompleted:
		return 0
	case res.Status == evalspec.RunCancelled && res.StopReason != nil && *res.StopReason == evalspec.StopInterrupt:
		return 130
	case res.Status == evalspec.RunCancelled:
		return 0
	default:
		return 3
	}
}
