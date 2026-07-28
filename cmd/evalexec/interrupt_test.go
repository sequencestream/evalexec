package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/fixtures"
	"github.com/sequencestream/evalexec/internal/result"
)

// Interrupt handling can only be tested through a real process: the signal
// handler lives in this package by design, and a library-level test would be
// exercising context cancellation instead — a different thing.

// buildBinary compiles the command once for the interrupt tests.
func buildBinary(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "evalexec")

	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	return bin
}

// slowDataset writes rows that take long enough to grade that an interrupt can
// land mid-run.
//
// The delay comes from a regex Grader over a large output rather than from a
// sleep: there is nothing in the protocol that makes a Grader slow on request,
// and a sleeping fake would have to be compiled into the binary under test.
func slowDataset(t *testing.T, dir string, rows int) string {
	t.Helper()

	// A pattern that backtracks against a long non-matching string costs
	// milliseconds per sample without any artificial delay.
	filler := strings.Repeat("ab", 4000)

	var b strings.Builder

	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&b, `{"case_id":"c%04d","input":{"q":%d},"output":%q}`+"\n", i, i, filler)
	}

	path := filepath.Join(dir, "dataset.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	return path
}

func writeGraderFile(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "grader.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write grader: %v", err)
	}

	return path
}

const slowRegexGrader = `{
	"id": "slow-regex", "version": "v1", "protocol": "builtin", "entry": "regex",
	"requires": ["input", "output"], "requires_judge": false,
	"parameters": {"pattern": "(?:ab)+c"}
}`

// waitForRecords polls the pending result directory until it holds at least n
// records, so the interrupt lands during the run rather than before or after.
//
// Polling rather than sleeping: a fixed sleep is a race that only shows up on a
// loaded machine, and it either flakes or wastes time on every run.
func waitForRecords(t *testing.T, root, evalID string, n int) string {
	t.Helper()

	// The temporary directory name is derived from the eval_id, which the test
	// supplies, so it is predictable.
	pattern := filepath.Join(root, ".*.tmp-"+evalID)

	deadline := time.Now().Add(20 * time.Second)

	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob: %v", err)
		}

		for _, dir := range matches {
			data, err := os.ReadFile(filepath.Join(dir, result.FileRecords))
			if err != nil {
				continue
			}

			if len(fixtures.Lines(data)) >= n {
				return dir
			}
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("no pending directory with %d records appeared under %s", n, root)

	return ""
}

// TestInterruptPublishesACompleteResult is f05: the interrupt case asserts
// invariants rather than a golden file, because where an interrupt lands depends
// on scheduling.
func TestInterruptPublishesACompleteResult(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real subprocess")
	}

	bin := buildBinary(t)
	root := t.TempDir()

	const (
		rows   = 400
		evalID = "interrupt-test-1"
	)

	dataset := slowDataset(t, root, rows)
	graderFile := writeGraderFile(t, root, slowRegexGrader)
	outDir := filepath.Join(root, "out")

	cmd := exec.CommandContext(t.Context(), bin,
		"--eval-id", evalID,
		"--task-id", "interrupt-test",
		"--dataset", dataset,
		"--grader", graderFile,
		"--output-dir", outDir,
	)

	var stderr strings.Builder

	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForRecords(t, root, evalID, 5)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	code := waitForExit(t, cmd)
	t.Logf("exit=%d stderr=%q", code, stderr.String())

	if code != 130 {
		t.Errorf("exit code = %d, want 130\nstderr: %s", code, stderr.String())
	}

	// 130 does not promise the wind-up finished. When it did, the directory is
	// complete; when it did not, there is no directory at all — never a
	// half-written one, because publication is a rename.
	if _, err := os.Stat(outDir); errors.Is(err, os.ErrNotExist) {
		t.Log("the result directory was not published; the wind-up did not complete in time")

		return
	}

	assertInterruptInvariants(t, outDir, rows)
}

// assertInterruptInvariants checks every property f05 declares.
func assertInterruptInvariants(t *testing.T, outDir string, rows int) {
	t.Helper()

	resultData, err := os.ReadFile(filepath.Join(outDir, result.FileResult))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	var res evalspec.EvalResult
	if err := json.Unmarshal(resultData, &res); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if res.Status != evalspec.RunCancelled {
		t.Errorf("status = %q, want cancelled", res.Status)
	}

	if res.StopReason == nil || *res.StopReason != evalspec.StopInterrupt {
		t.Errorf("stop_reason = %v, want interrupt", res.StopReason)
	}

	if res.Counts.Total != rows {
		t.Errorf("counts.total = %d, want %d", res.Counts.Total, rows)
	}

	if res.Counts.Skipped == 0 {
		t.Error("counts.skipped = 0, want at least one sample backfilled")
	}

	// The result must also satisfy its own invariants: an interrupted run is
	// still a valid result or it is no result.
	if err := res.Validate(); err != nil {
		t.Errorf("the published result is not valid: %v", err)
	}

	records := readRecords(t, outDir)

	if len(records) != rows {
		t.Fatalf("records.jsonl has %d lines, the dataset has %d rows: the line-count identity must survive an interrupt",
			len(records), rows)
	}

	seen := make(map[int]bool, rows)
	failures := 0

	for i, rec := range records {
		if rec.Sequence < 1 || rec.Sequence > rows {
			t.Errorf("record %d has sequence %d, outside 1..%d", i+1, rec.Sequence, rows)
		}

		if seen[rec.Sequence] {
			t.Errorf("sequence %d appears more than once", rec.Sequence)
		}

		seen[rec.Sequence] = true

		if rec.Status == evalspec.RecordSkipped {
			if rec.Error == nil || rec.Error.Code != evalspec.CodeSkipped ||
				rec.Error.Reason != evalspec.StopInterrupt {
				t.Errorf("skipped record %s has error %+v, want {skipped, interrupt}", rec.CaseID, rec.Error)
			}

			if rec.Evaluation != nil {
				t.Errorf("skipped record %s carries an evaluation", rec.CaseID)
			}

			continue
		}

		if rec.Evaluation != nil && rec.Evaluation.Status == evalspec.EvaluationFail {
			failures++

			t.Errorf("record %s is a failure after an interrupt: %+v", rec.CaseID, rec.Evaluation.Error)
		}
	}

	if len(seen) != rows {
		t.Errorf("sequences cover %d of %d rows", len(seen), rows)
	}

	// The single most important assertion here. A sample abandoned mid-flight
	// was never finished, so it is skipped — recording it as a timeout or an
	// internal error would present work that never happened as work that
	// happened badly.
	if failures > 0 {
		t.Errorf("%d records are failures; an interrupt must produce skipped records only", failures)
	}
}

func readRecords(t *testing.T, outDir string) []evalspec.Record {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(outDir, result.FileRecords))
	if err != nil {
		t.Fatalf("read records: %v", err)
	}

	lines := fixtures.Lines(data)
	out := make([]evalspec.Record, 0, len(lines))

	for i, line := range lines {
		var rec evalspec.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse record %d: %v", i+1, err)
		}

		out = append(out, rec)
	}

	return out
}

// TestSecondInterruptIsIgnored covers the escalation rule: the backfill is what
// makes a partial result trustworthy, so interrupting it must not abandon the
// run.
func TestSecondInterruptIsIgnored(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real subprocess")
	}

	bin := buildBinary(t)
	root := t.TempDir()

	const (
		rows   = 400
		evalID = "interrupt-test-2"
	)

	dataset := slowDataset(t, root, rows)
	graderFile := writeGraderFile(t, root, slowRegexGrader)
	outDir := filepath.Join(root, "out")

	cmd := exec.CommandContext(t.Context(), bin,
		"--eval-id", evalID,
		"--task-id", "interrupt-test",
		"--dataset", dataset,
		"--grader", graderFile,
		"--output-dir", outDir,
	)

	var stderr strings.Builder

	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForRecords(t, root, evalID, 5)

	// Two signals in quick succession. The second must not stop the wind-up.
	for range 2 {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("signal: %v", err)
		}
	}

	code := waitForExit(t, cmd)
	t.Logf("exit=%d stderr=%q", code, stderr.String())

	if code != 130 {
		t.Errorf("exit code = %d, want 130\nstderr: %s", code, stderr.String())
	}

	if _, err := os.Stat(outDir); errors.Is(err, os.ErrNotExist) {
		t.Skip("the result directory was not published; nothing to check about the second signal")
	}

	// What matters is that the second signal did not abandon the wind-up: the
	// directory is published and complete. The "already winding down" notice is
	// not asserted, because whether the handler observes the second signal
	// before the run finishes is a genuine race — on a fast run the wind-up can
	// complete first, and demanding the message would make the test flaky
	// rather than strict.
	assertInterruptInvariants(t, outDir, rows)
}

// waitForExit waits for the command and returns its exit code.
func waitForExit(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()

	err := cmd.Wait()
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	t.Fatalf("wait: %v", err)

	return -1
}

// TestInterruptBeforeAnyWorkLeavesNoDirectory checks the other end of the
// range: a signal arriving before the run started must not leave a partial
// directory behind either.
func TestInterruptBeforeAnyWorkLeavesNoDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real subprocess")
	}

	bin := buildBinary(t)
	root := t.TempDir()

	dataset := slowDataset(t, root, 200)
	graderFile := writeGraderFile(t, root, slowRegexGrader)
	outDir := filepath.Join(root, "out")

	cmd := exec.CommandContext(t.Context(), bin,
		"--eval-id", "interrupt-test-3",
		"--task-id", "interrupt-test",
		"--dataset", dataset,
		"--grader", graderFile,
		"--output-dir", outDir,
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// No wait: the signal races the start-up on purpose.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	_ = waitForExit(t, cmd)

	// Whatever happened, no temporary directory may survive.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("a temporary directory was left behind: %s", e.Name())
		}
	}

	// And if a directory was published, it is complete.
	if _, err := os.Stat(outDir); err == nil {
		assertInterruptInvariants(t, outDir, 200)
	}
}
