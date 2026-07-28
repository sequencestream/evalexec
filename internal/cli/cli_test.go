package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/internal/cli"
)

// write drops a file into dir and returns its path.
func write(t *testing.T, dir, name, content string) string {
	t.Helper()

	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}

	return p
}

const validGrader = `{
	"id": "g", "version": "v1", "protocol": "builtin", "entry": "exact_match",
	"requires": ["input", "output", "reference"], "requires_judge": false
}`

// TestGraderFlagRejectsRepetition covers acceptance criteria 2 and 3. The
// standard flag package silently keeps the last value, which for --grader
// would quietly run a different Grader than the caller listed first.
func TestGraderFlagRejectsRepetition(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	var diag strings.Builder

	_, err := cli.Parse([]string{
		"--task-id", "t", "--dataset", "d.jsonl",
		"--grader", g, "--grader", g,
		"--output-dir", "out",
	}, &diag, cli.Options{WorkingDir: dir})
	if err == nil {
		t.Fatal("two --grader flags must be rejected")
	}

	if kind, _ := evalerr.KindOf(err); kind != evalerr.KindArgument {
		t.Errorf("kind = %v, want argument", kind)
	}

	if !strings.Contains(diag.String(), "only be given once") {
		t.Errorf("diagnostic should say the flag may appear once:\n%s", diag.String())
	}
}

// TestSingleValueFlagsRejectRepetition extends the same rule to the other
// single-valued flags: repeating one is always a mistake, and silently taking
// the last value only hides it.
func TestSingleValueFlagsRejectRepetition(t *testing.T) {
	for _, flag := range []string{"--eval-id", "--task-id", "--dataset", "--output-dir", "--judge-model", "--request"} {
		t.Run(flag, func(t *testing.T) {
			_, err := cli.Parse([]string{flag, "a", flag, "b"}, io.Discard, cli.Options{WorkingDir: t.TempDir()})
			if err == nil {
				t.Errorf("%s given twice must be rejected", flag)
			}
		})
	}
}

// TestParamOverrideScalarParsing pins the value grammar: JSON scalars are
// decoded, anything unparseable falls back to a plain string, and structure is
// refused outright.
func TestParamOverrideScalarParsing(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		want    any
		wantErr bool
	}{
		{name: "integer", arg: "max_score=1", want: float64(1)},
		{name: "float", arg: "temperature=0.5", want: 0.5},
		{name: "zero", arg: "temperature=0", want: float64(0)},
		{name: "true", arg: "use_reference=true", want: true},
		{name: "false", arg: "use_trajectory=false", want: false},
		{name: "null", arg: "rubric=null", want: nil},
		{name: "quoted string", arg: `rubric="a, b"`, want: "a, b"},
		// The common case must not need shell quoting.
		{name: "bare string", arg: "model=gpt-4o", want: "gpt-4o"},
		{name: "bare string with dashes", arg: "model=deepseek-v4-flash", want: "deepseek-v4-flash"},
		// Only the first = splits, so a value may contain one.
		{name: "value containing equals", arg: "rubric=a=b", want: "a=b"},
		// Structure belongs in a file, not on a command line.
		{name: "array", arg: "stop=[1,2]", wantErr: true},
		{name: "object", arg: `schema={"type":"object"}`, wantErr: true},
		{name: "no equals sign", arg: "rubric", wantErr: true},
		{name: "empty key", arg: "=value", wantErr: true},
	}

	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, _, _ := strings.Cut(tt.arg, "=")

			req, err := cli.Parse([]string{
				"--task-id", "t", "--dataset", "d.jsonl", "--grader", g,
				"--output-dir", "out", "--grader-param", tt.arg,
			}, io.Discard, cli.Options{WorkingDir: dir})

			if tt.wantErr {
				if err == nil {
					t.Errorf("%q must be rejected", tt.arg)
				}

				return
			}

			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			got, ok := req.Grader.Parameters[key]
			if !ok {
				t.Fatalf("parameter %q not set", key)
			}

			if got != tt.want {
				t.Errorf("%q -> %#v (%T), want %#v (%T)", tt.arg, got, got, tt.want, tt.want)
			}
		})
	}
}

// TestFlagsOverrideRequestFile pins the merge order and checks that an
// override announces itself, so a surprised user can see which value won.
func TestFlagsOverrideRequestFile(t *testing.T) {
	dir := t.TempDir()

	request := write(t, dir, "request.json", `{
		"spec_version": "evalexec/v1alpha1",
		"eval_id": "from-file",
		"task_id": "task-from-file",
		"dataset": {"path": "file.jsonl"},
		"grader": {"id": "g", "version": "v1", "protocol": "builtin", "entry": "exact_match",
		           "requires": ["input", "output", "reference"], "requires_judge": false,
		           "parameters": {"reference_path": "$.expected_output"}},
		"output_dir": "file-out"
	}`)

	var diag strings.Builder

	req, err := cli.Parse([]string{
		"--request", request,
		"--task-id", "task-from-flag",
		"--dataset", "flag.jsonl",
		"--grader-param", "reference_path=$.other",
	}, &diag, cli.Options{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if req.TaskID != "task-from-flag" {
		t.Errorf("task_id = %q, want the flag to win", req.TaskID)
	}

	if filepath.Base(req.Dataset.Path) != "flag.jsonl" {
		t.Errorf("dataset.path = %q, want the flag to win", req.Dataset.Path)
	}

	// Not overridden, so the file value survives.
	if req.EvalID != "from-file" {
		t.Errorf("eval_id = %q, want the file value", req.EvalID)
	}

	// Parameter overrides are applied last, over the file's parameters.
	if got := req.Grader.Parameters["reference_path"]; got != "$.other" {
		t.Errorf("reference_path = %v, want the override to win", got)
	}

	for _, want := range []string{"task_id", "dataset.path"} {
		if !strings.Contains(diag.String(), want) {
			t.Errorf("an override of %s should be announced:\n%s", want, diag.String())
		}
	}
}

// TestPathsResolveAgainstRequestFile pins where a relative path points. A
// request file checked in beside its dataset must keep working regardless of
// which directory the command runs from.
func TestPathsResolveAgainstRequestFile(t *testing.T) {
	dir := t.TempDir()

	sub := filepath.Join(dir, "eval")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	request := write(t, sub, "request.json", `{
		"spec_version": "evalexec/v1alpha1",
		"task_id": "t",
		"dataset": {"path": "sessions.jsonl"},
		"grader": {"id": "g", "version": "v1", "protocol": "builtin", "entry": "exact_match",
		           "requires": ["input", "output", "reference"], "requires_judge": false},
		"output_dir": "out"
	}`)

	req, err := cli.Parse([]string{"--request", request}, io.Discard,
		cli.Options{WorkingDir: filepath.Join(dir, "somewhere-else")})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if want := filepath.Join(sub, "sessions.jsonl"); req.Dataset.Path != want {
		t.Errorf("dataset.path = %q, want %q (relative to the request file, not the working directory)",
			req.Dataset.Path, want)
	}

	if want := filepath.Join(sub, "out"); req.OutputDir != want {
		t.Errorf("output_dir = %q, want %q", req.OutputDir, want)
	}
}

func TestNormalizationDefaults(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	req, err := cli.Parse([]string{
		"--task-id", "t", "--dataset", "d.jsonl", "--grader", g, "--output-dir", "out",
	}, io.Discard, cli.Options{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if req.SpecVersion != evalspec.SpecVersion {
		t.Errorf("spec_version = %q, want %q", req.SpecVersion, evalspec.SpecVersion)
	}

	if req.Execution == nil || req.Execution.Concurrency != 1 {
		t.Errorf("concurrency should default to 1, got %+v", req.Execution)
	}

	if !filepath.IsAbs(req.Dataset.Path) || !filepath.IsAbs(req.OutputDir) {
		t.Errorf("paths must be absolute after normalization: %q %q", req.Dataset.Path, req.OutputDir)
	}
}

var uuidV7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestGeneratedEvalIDIsUUIDv7 covers acceptance criteria 5 and 6, and half of
// 20: two invocations must produce two independent identifiers.
func TestGeneratedEvalIDIsUUIDv7(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	args := []string{"--task-id", "t", "--dataset", "d.jsonl", "--grader", g, "--output-dir", "out"}

	first, err := cli.Parse(args, io.Discard, cli.Options{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	second, err := cli.Parse(args, io.Discard, cli.Options{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, id := range []string{first.EvalID, second.EvalID} {
		if !uuidV7Pattern.MatchString(id) {
			t.Errorf("eval_id %q is not a UUIDv7 (version nibble 7, variant 8-b)", id)
		}
	}

	if first.EvalID == second.EvalID {
		t.Error("two invocations must produce two distinct eval_ids")
	}
}

func TestSuppliedEvalIDIsKept(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	req, err := cli.Parse([]string{
		"--eval-id", "my-own-id", "--task-id", "t", "--dataset", "d.jsonl",
		"--grader", g, "--output-dir", "out",
	}, io.Discard, cli.Options{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Identifiers are opaque strings from the caller; uniqueness is the
	// caller's problem, so no format is imposed.
	if req.EvalID != "my-own-id" {
		t.Errorf("eval_id = %q, want the supplied value untouched", req.EvalID)
	}
}

func TestInjectedIDGenerator(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	req, err := cli.Parse([]string{
		"--task-id", "t", "--dataset", "d.jsonl", "--grader", g, "--output-dir", "out",
	}, io.Discard, cli.Options{
		WorkingDir:  dir,
		IDGenerator: cli.FixedIDGenerator{ID: "fixed-id"},
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if req.EvalID != "fixed-id" {
		t.Errorf("eval_id = %q, want the injected generator's value", req.EvalID)
	}
}

func TestSecretFlagsRejected(t *testing.T) {
	for _, flag := range []string{"--api-key", "--token", "--secret", "--password", "--bearer"} {
		t.Run(flag, func(t *testing.T) {
			// Both the separated and the joined form.
			for _, args := range [][]string{{flag, "value"}, {flag + "=value"}} {
				_, err := cli.Parse(args, io.Discard, cli.Options{WorkingDir: t.TempDir()})
				if err == nil {
					t.Fatalf("%v must be rejected", args)
				}

				if !strings.Contains(err.Error(), "auth") {
					t.Errorf("error should point at judge_model.auth: %v", err)
				}
			}
		})
	}
}

func TestPositionalArgumentsRejected(t *testing.T) {
	_, err := cli.Parse([]string{"run", "--task-id", "t"}, io.Discard, cli.Options{WorkingDir: t.TempDir()})
	if err == nil {
		t.Fatal("a positional argument must be rejected: there are no subcommands")
	}

	if !strings.Contains(err.Error(), "subcommand") {
		t.Errorf("error should explain there are no subcommands: %v", err)
	}
}

func TestJudgeParamWithoutJudgeModel(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	_, err := cli.Parse([]string{
		"--task-id", "t", "--dataset", "d.jsonl", "--grader", g, "--output-dir", "out",
		"--judge-param", "temperature=0",
	}, io.Discard, cli.Options{WorkingDir: dir})
	if err == nil {
		t.Fatal("a Judge parameter with no Judge to apply it to must be rejected")
	}
}

func TestConcurrencyMustBePositive(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	_, err := cli.Parse([]string{
		"--task-id", "t", "--dataset", "d.jsonl", "--grader", g, "--output-dir", "out",
		"--concurrency", "-1",
	}, io.Discard, cli.Options{WorkingDir: dir})
	if err == nil {
		t.Fatal("a negative concurrency must be rejected")
	}
}

func TestSeedIsRecordedButNotForwarded(t *testing.T) {
	dir := t.TempDir()
	g := write(t, dir, "g.json", validGrader)

	req, err := cli.Parse([]string{
		"--task-id", "t", "--dataset", "d.jsonl", "--grader", g, "--output-dir", "out",
		"--seed", "42",
	}, io.Discard, cli.Options{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if req.Execution.Seed == nil || *req.Execution.Seed != 42 {
		t.Errorf("seed = %v, want 42 recorded in the request", req.Execution.Seed)
	}

	// Seed 0 must be distinguishable from "no seed given", which is why the
	// field is a pointer.
	req, err = cli.Parse([]string{
		"--task-id", "t", "--dataset", "d.jsonl", "--grader", g, "--output-dir", "out",
		"--seed", "0",
	}, io.Discard, cli.Options{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if req.Execution.Seed == nil {
		t.Error("--seed 0 must be recorded, not treated as unset")
	}
}
