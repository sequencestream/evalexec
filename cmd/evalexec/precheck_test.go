package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/fixtures"
	"github.com/sequencestream/evalexec/internal/result"
)

// materialize copies one pre-check subcase out of the embedded fixtures into a
// temporary directory, so the command runs against real files at real paths.
func materialize(t *testing.T, subcase string) string {
	t.Helper()

	root := t.TempDir()
	src := filepath.Join(fixtures.Dir(fixtures.CasePrecheckFailures), subcase)

	err := fs.WalkDir(fixtures.FS, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}

		dst := filepath.Join(root, rel)

		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}

		data, err := fixtures.FS.ReadFile(p)
		if err != nil {
			return err
		}

		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatalf("materialize %s: %v", subcase, err)
	}

	return root
}

// argvFor builds the command line for a subcase: either the explicit argv the
// fixture supplies, or the standard --request form.
func argvFor(t *testing.T, subcase, root string) []string {
	t.Helper()

	if data, err := fixtures.ReadPrecheck(subcase, "args.json"); err == nil {
		var spec struct {
			Argv []string `json:"argv"`
		}

		if err := json.Unmarshal(data, &spec); err != nil {
			t.Fatalf("parse args.json: %v", err)
		}

		return spec.Argv
	}

	return []string{"--request", filepath.Join(root, "request.json")}
}

// TestPrecheckFixtures runs every pre-check subcase end to end and checks the
// exit code, the failing step and the diagnostic.
func TestPrecheckFixtures(t *testing.T) {
	cases, err := fixtures.PrecheckCases()
	if err != nil {
		t.Fatalf("%v", err)
	}

	for _, subcase := range cases {
		t.Run(subcase, func(t *testing.T) {
			data, err := fixtures.ReadPrecheck(subcase, fixtures.FileExpectedFailure)
			if err != nil {
				t.Fatalf("%v", err)
			}

			var want fixtures.ExpectedFailure
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("parse expected failure: %v", err)
			}

			root := materialize(t, subcase)

			var stdout, stderr strings.Builder

			code := run(argvFor(t, subcase, root), &stdout, &stderr)

			if code != want.ExitCode {
				t.Errorf("exit code = %d, want %d\nstderr: %s", code, want.ExitCode, stderr.String())
			}

			if want.StderrContains != "" && !strings.Contains(stderr.String(), want.StderrContains) {
				t.Errorf("stderr does not mention %q:\n%s", want.StderrContains, stderr.String())
			}

			if stdout.Len() != 0 {
				t.Errorf("stdout must stay clean on failure, got: %s", stdout.String())
			}

			assertNoResultDirectory(t, root)
		})
	}
}

// assertNoResultDirectory checks that a rejected run left nothing behind — not
// the output directory, and not a temporary one either.
//
// A stray temporary directory would be worse than a stray result: it survives
// as hidden state that the next run's emptiness check may or may not notice.
func assertNoResultDirectory(t *testing.T, root string) {
	t.Helper()

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}

	for _, e := range entries {
		name := e.Name()

		if strings.Contains(name, ".tmp-") {
			t.Errorf("a rejected run left a temporary directory behind: %s", name)
		}

		// "out" is the output_dir every fixture request names; it must not
		// have been created. "existing-out" is fixture input, not output.
		if name == "out" {
			t.Errorf("a rejected run created its output directory: %s", name)
		}
	}
}

// TestCheckOrderDirectoryConflictWins is the ordering rule on its own, kept
// separate from the fixture sweep so a failure names the rule directly.
//
// The natural implementation validates its inputs before touching its outputs,
// which passes every other pre-check case and gets exactly this one wrong.
func TestCheckOrderDirectoryConflictWins(t *testing.T) {
	root := t.TempDir()

	// Three failures at once, at three different steps.
	outDir := filepath.Join(root, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(outDir, "leftover.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// A malformed dataset (step 5) ...
	dataset := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(dataset, []byte(`{"case_id": "c1"`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// ... and a Grader whose declaration disagrees with the built-in (step 3).
	request := filepath.Join(root, "request.json")
	body := `{
		"spec_version": "evalexec/v1alpha1",
		"task_id": "t",
		"dataset": {"path": "dataset.jsonl"},
		"grader": {"id": "g", "version": "v1", "protocol": "builtin", "entry": "exact_match",
		           "requires": ["input"], "requires_judge": false},
		"output_dir": "out"
	}`

	if err := os.WriteFile(request, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr strings.Builder

	code := run([]string{"--request", request}, &stdout, &stderr)

	if code != 4 {
		t.Errorf("exit code = %d, want 4\n"+
			"the output directory conflict must be reported even though the dataset and the "+
			"Grader declaration are also invalid: the check order is part of the specification\n"+
			"stderr: %s", code, stderr.String())
	}

	if !strings.Contains(stderr.String(), "output") {
		t.Errorf("stderr should name the output directory:\n%s", stderr.String())
	}
}

// TestVersionStillWorksWithoutRequiredFlags checks that --version short
// circuits before the mandatory flags are enforced.
func TestVersionStillWorksWithoutRequiredFlags(t *testing.T) {
	var stdout, stderr strings.Builder

	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if !strings.HasPrefix(stdout.String(), "evalexec ") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// TestSecretFlagsAreRejected covers the rule that a credential never travels
// on a command line, where it lands in shell history and process listings.
func TestSecretFlagsAreRejected(t *testing.T) {
	for _, flag := range []string{"--api-key", "--token", "--secret", "--password"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr strings.Builder

			code := run([]string{flag, "whatever", "--task-id", "t"}, &stdout, &stderr)

			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}

			// The message must point at the supported mechanism, not merely
			// say the flag is unknown — otherwise a user goes looking for the
			// right spelling of a flag that does not and should not exist.
			if !strings.Contains(stderr.String(), "auth") {
				t.Errorf("stderr should point at judge_model.auth:\n%s", stderr.String())
			}
		})
	}
}

// TestValidRunReachesExecution checks the happy path: a well-formed request
// passes all six checks. What follows is execution's job.
func TestValidRunReachesExecution(t *testing.T) {
	root := t.TempDir()

	dataset := filepath.Join(root, "dataset.jsonl")
	rows := `{"case_id":"c1","input":{"q":1},"output":{"a":1},"reference":{"expected_output":{"a":1}}}
{"case_id":"c2","input":{"q":2},"output":null,"reference":{"expected_output":null}}
`

	if err := os.WriteFile(dataset, []byte(rows), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	request := filepath.Join(root, "request.json")
	body := `{
		"spec_version": "evalexec/v1alpha1",
		"task_id": "t",
		"dataset": {"path": "dataset.jsonl"},
		"grader": {"id": "g", "version": "v1", "protocol": "builtin", "entry": "exact_match",
		           "requires": ["input", "output", "reference"], "requires_judge": false},
		"output_dir": "out"
	}`

	if err := os.WriteFile(request, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr strings.Builder

	if code := run([]string{"--request", request}, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
}

// TestSingleInvocationCompletesARun pins the atomic contract: one command
// line, no subcommand, one complete result.
//
// TestValidRunReachesExecution above checks the same path from the other end —
// that nothing rejects it — while this one checks that a result actually lands.
func TestSingleInvocationCompletesARun(t *testing.T) {
	root := t.TempDir()

	dataset := filepath.Join(root, "sessions.jsonl")
	rows := `{"case_id":"c1","input":{"q":1},"output":{"a":1},"reference":{"expected_output":{"a":1}}}
{"case_id":"c2","input":{"q":2},"output":{"a":2},"reference":{"expected_output":{"a":9}}}
`

	if err := os.WriteFile(dataset, []byte(rows), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	graderFile := filepath.Join(root, "grader.json")
	graderBody := `{"id":"g","version":"v1","protocol":"builtin","entry":"exact_match",
		"requires":["input","output","reference"],"requires_judge":false}`

	if err := os.WriteFile(graderFile, []byte(graderBody), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	outDir := filepath.Join(root, "out")

	var stdout, stderr strings.Builder

	code := run([]string{
		"--task-id", "criterion-one",
		"--dataset", dataset,
		"--grader", graderFile,
		"--output-dir", outDir,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	// stdout stays machine-readable: the result is a directory, not a stream.
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}

	res := readPublishedResult(t, outDir)

	if res.Status != evalspec.RunCompleted {
		t.Errorf("status = %q, want completed", res.Status)
	}

	if res.Counts.Total != 2 {
		t.Errorf("counts.total = %d, want 2", res.Counts.Total)
	}
}

// TestTaskIDIsEchoedVerbatim pins the pass-through: task_id is checked for
// non-emptiness and nothing else.
//
// It is a correlation key, not a domain object: no lookup, no normalization, no
// state. So the test feeds values that would break anything trying to interpret
// them and requires each to come back unchanged.
func TestTaskIDIsEchoedVerbatim(t *testing.T) {
	opaque := []string{
		"cs-regression-20260727",
		"team/project#42",
		"含中文的任务名",
		"  leading and trailing spaces  ",
		`{"looks":"like json"}`,
		"../../not/a/path",
	}

	for _, taskID := range opaque {
		t.Run(taskID, func(t *testing.T) {
			root := t.TempDir()

			dataset := filepath.Join(root, "sessions.jsonl")
			row := `{"case_id":"c1","input":{"q":1},"output":{"a":1},"reference":{"expected_output":{"a":1}}}` + "\n"

			if err := os.WriteFile(dataset, []byte(row), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			graderFile := filepath.Join(root, "grader.json")
			graderBody := `{"id":"g","version":"v1","protocol":"builtin","entry":"exact_match",
				"requires":["input","output","reference"],"requires_judge":false}`

			if err := os.WriteFile(graderFile, []byte(graderBody), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			outDir := filepath.Join(root, "out")

			var stdout, stderr strings.Builder

			code := run([]string{
				"--task-id", taskID,
				"--dataset", dataset,
				"--grader", graderFile,
				"--output-dir", outDir,
			}, &stdout, &stderr)

			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
			}

			res := readPublishedResult(t, outDir)

			if res.TaskID != taskID {
				t.Errorf("task_id = %q, want %q verbatim", res.TaskID, taskID)
			}

			// Every record carries it too.
			for i, rec := range readPublishedRecords(t, outDir) {
				if rec.TaskID != taskID {
					t.Errorf("record %d has task_id %q, want %q", i+1, rec.TaskID, taskID)
				}
			}
		})
	}
}

// TestEmptyTaskIDIsRejected is the other half of criterion 4: non-emptiness is
// the one thing that *is* checked.
func TestEmptyTaskIDIsRejected(t *testing.T) {
	root := t.TempDir()

	dataset := filepath.Join(root, "sessions.jsonl")
	if err := os.WriteFile(dataset, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr strings.Builder

	code := run([]string{
		"--task-id", "",
		"--dataset", dataset,
		"--output-dir", filepath.Join(root, "out"),
	}, &stdout, &stderr)

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}

	if !strings.Contains(stderr.String(), "task_id") {
		t.Errorf("stderr should name task_id:\n%s", stderr.String())
	}
}

// readPublishedResult loads result.json from a published directory.
func readPublishedResult(t *testing.T, outDir string) evalspec.EvalResult {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(outDir, result.FileResult))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	var res evalspec.EvalResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	return res
}

// readPublishedRecords loads records.jsonl from a published directory.
func readPublishedRecords(t *testing.T, outDir string) []evalspec.Record {
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
