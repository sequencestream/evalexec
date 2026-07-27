package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/fixtures"
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
// passes all six checks. What follows is M3's job.
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
