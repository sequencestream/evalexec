package validate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/internal/validate"
)

// fixture builds a valid request against real files, which each test then
// breaks in one specific way.
type fixture struct {
	dir string
	req *evalspec.EvalRequest
}

func newFixture(t *testing.T, rows string) *fixture {
	t.Helper()

	dir := t.TempDir()

	dataset := filepath.Join(dir, "dataset.jsonl")
	if err := os.WriteFile(dataset, []byte(rows), 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	return &fixture{
		dir: dir,
		req: &evalspec.EvalRequest{
			SpecVersion: evalspec.SpecVersion,
			EvalID:      "eval-1",
			TaskID:      "task-1",
			Dataset:     evalspec.Dataset{Path: dataset},
			Grader: evalspec.GraderSpec{
				ID: "g", Version: "v1",
				Protocol: evalspec.GraderBuiltin, Entry: "exact_match",
				Requires: []evalspec.SessionField{
					evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
				},
				RequiresJudge: false,
			},
			Execution: &evalspec.Execution{Concurrency: 1},
			OutputDir: filepath.Join(dir, "out"),
		},
	}
}

const twoValidRows = `{"case_id":"c1","input":{"q":1},"output":{"a":1},"reference":{"expected_output":{"a":1}}}
{"case_id":"c2","input":{"q":2},"output":null,"reference":{"expected_output":null}}
`

func TestAllAcceptsAValidRequest(t *testing.T) {
	f := newFixture(t, twoValidRows)

	report, err := validate.All(f.req, validate.Options{})
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	if report.Rows != 2 {
		t.Errorf("Rows = %d, want 2", report.Rows)
	}

	if report.Declaration == nil || report.Declaration.Entry != "exact_match" {
		t.Errorf("Declaration = %+v, want the exact_match built-in", report.Declaration)
	}
}

// TestEmptyDatasetIsLegal pins the open question: a dataset with no rows is a
// valid run producing an empty result, not an error.
func TestEmptyDatasetIsLegal(t *testing.T) {
	f := newFixture(t, "")

	report, err := validate.All(f.req, validate.Options{})
	if err != nil {
		t.Fatalf("an empty dataset must be accepted: %v", err)
	}

	if report.Rows != 0 {
		t.Errorf("Rows = %d, want 0", report.Rows)
	}
}

// TestTrailingNewlineIsNotARow guards the line-count identity at its source: a
// trailing newline is normal in a text file and must not become a phantom row.
func TestTrailingNewlineIsNotARow(t *testing.T) {
	f := newFixture(t, twoValidRows+"\n\n")

	report, err := validate.All(f.req, validate.Options{})
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	if report.Rows != 2 {
		t.Errorf("Rows = %d, want 2: blank lines are not rows", report.Rows)
	}
}

func TestStepFailures(t *testing.T) {
	tests := []struct {
		name     string
		rows     string
		mutate   func(t *testing.T, f *fixture)
		wantStep string
		wantKind evalerr.Kind
		wantMsg  string
	}{
		{
			name:     "malformed request",
			rows:     twoValidRows,
			mutate:   func(_ *testing.T, f *fixture) { f.req.TaskID = "" },
			wantStep: validate.StepArguments,
			wantKind: evalerr.KindArgument,
			wantMsg:  "task_id",
		},
		{
			name: "output directory not empty",
			rows: twoValidRows,
			mutate: func(t *testing.T, f *fixture) {
				if err := os.MkdirAll(f.req.OutputDir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}

				if err := os.WriteFile(filepath.Join(f.req.OutputDir, "x"), []byte("x"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			},
			wantStep: validate.StepOutputDirConflict,
			wantKind: evalerr.KindOutput,
			wantMsg:  "not empty",
		},
		{
			name:     "unknown builtin entry",
			rows:     twoValidRows,
			mutate:   func(_ *testing.T, f *fixture) { f.req.Grader.Entry = "vibes" },
			wantStep: validate.StepGraderDeclaration,
			wantKind: evalerr.KindPrecheck,
			wantMsg:  "unknown builtin grader entry",
		},
		{
			name: "declared requires disagrees with the built-in",
			rows: twoValidRows,
			mutate: func(_ *testing.T, f *fixture) {
				f.req.Grader.Requires = []evalspec.SessionField{evalspec.FieldInput}
			},
			wantStep: validate.StepGraderDeclaration,
			wantKind: evalerr.KindPrecheck,
			wantMsg:  "requires",
		},
		{
			name: "declared requires_judge disagrees with the built-in",
			rows: twoValidRows,
			mutate: func(_ *testing.T, f *fixture) {
				f.req.Grader.RequiresJudge = true
				f.req.JudgeModel = &evalspec.JudgeModelSpec{
					Protocol: evalspec.JudgeOpenAIChat,
					Endpoint: "https://example.invalid/v1",
					Auth:     evalspec.Auth{Type: evalspec.AuthNone},
				}
			},
			wantStep: validate.StepGraderDeclaration,
			wantKind: evalerr.KindPrecheck,
			wantMsg:  "requires_judge",
		},
		{
			name: "unknown grader parameter",
			rows: twoValidRows,
			mutate: func(_ *testing.T, f *fixture) {
				f.req.Grader.Parameters = map[string]any{"rubrick": "typo"}
			},
			wantStep: validate.StepGraderDeclaration,
			wantKind: evalerr.KindPrecheck,
			wantMsg:  "unknown parameter",
		},
		{
			name: "duplicate case id",
			rows: `{"case_id":"c1","input":{},"output":{},"reference":{}}
{"case_id":"c1","input":{},"output":{},"reference":{}}
`,
			mutate:   func(_ *testing.T, _ *fixture) {},
			wantStep: validate.StepDatasetParse,
			wantKind: evalerr.KindPrecheck,
			wantMsg:  "c1",
		},
		{
			name:     "malformed jsonl",
			rows:     "{\"case_id\":\"c1\",\"input\":{}\n",
			mutate:   func(_ *testing.T, _ *fixture) {},
			wantStep: validate.StepDatasetParse,
			wantKind: evalerr.KindPrecheck,
			wantMsg:  "line 1",
		},
		{
			name: "session missing a required field",
			rows: `{"case_id":"c1","input":{},"output":{},"reference":{}}
{"case_id":"c2","input":{},"reference":{}}
`,
			mutate:   func(_ *testing.T, _ *fixture) {},
			wantStep: validate.StepSessionRequires,
			wantKind: evalerr.KindPrecheck,
			wantMsg:  "output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.rows)
			tt.mutate(t, f)

			_, err := validate.All(f.req, validate.Options{})
			if err == nil {
				t.Fatal("All returned nil, want an error")
			}

			if step := evalerr.StepOf(err); step != tt.wantStep {
				t.Errorf("step = %q, want %q (%v)", step, tt.wantStep, err)
			}

			if kind, _ := evalerr.KindOf(err); kind != tt.wantKind {
				t.Errorf("kind = %v, want %v", kind, tt.wantKind)
			}

			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantMsg)
			}
		})
	}
}

// TestNullFieldSatisfiesRequires is the positive half of the pair whose
// negative half is "session missing a required field" above. A null output
// means the agent produced none, which is a present field.
func TestNullFieldSatisfiesRequires(t *testing.T) {
	f := newFixture(t, `{"case_id":"c1","input":{},"output":null,"reference":{}}`+"\n")

	if _, err := validate.All(f.req, validate.Options{}); err != nil {
		t.Errorf("an explicitly null output must satisfy requires: %v", err)
	}
}

// TestLLMJudgeDynamicRequires covers the parameter-driven requirement list.
// The pre-check cannot decide whether a dataset satisfies llm_judge without
// resolving this first.
func TestLLMJudgeDynamicRequires(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]any
		requires []evalspec.SessionField
		wantErr  bool
	}{
		{
			name:     "neither",
			params:   map[string]any{"rubric": "r"},
			requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
		},
		{
			name:   "reference only",
			params: map[string]any{"rubric": "r", "use_reference": true},
			requires: []evalspec.SessionField{
				evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
			},
		},
		{
			name:   "trajectory only",
			params: map[string]any{"rubric": "r", "use_trajectory": true},
			requires: []evalspec.SessionField{
				evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldTrajectory,
			},
		},
		{
			name:   "both",
			params: map[string]any{"rubric": "r", "use_reference": true, "use_trajectory": true},
			requires: []evalspec.SessionField{
				evalspec.FieldInput, evalspec.FieldOutput,
				evalspec.FieldReference, evalspec.FieldTrajectory,
			},
		},
		{
			name:   "explicit false behaves like absent",
			params: map[string]any{"rubric": "r", "use_reference": false},
			requires: []evalspec.SessionField{
				evalspec.FieldInput, evalspec.FieldOutput,
			},
		},
		{
			name:     "declared list does not match the derived one",
			params:   map[string]any{"rubric": "r", "use_reference": true},
			requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
			wantErr:  true,
		},
		{
			name:     "non-boolean toggle",
			params:   map[string]any{"rubric": "r", "use_reference": "yes"},
			requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
			wantErr:  true,
		},
	}

	rows := `{"case_id":"c1","input":{},"output":{},"reference":{},"trajectory":[]}` + "\n"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, rows)
			f.req.Grader.Entry = "llm_judge"
			f.req.Grader.RequiresJudge = true
			f.req.Grader.Requires = tt.requires
			f.req.Grader.Parameters = tt.params
			f.req.JudgeModel = &evalspec.JudgeModelSpec{
				Protocol: evalspec.JudgeOpenAIChat,
				Endpoint: "https://example.invalid/v1",
				Auth:     evalspec.Auth{Type: evalspec.AuthNone},
			}

			report, err := validate.All(f.req, validate.Options{})

			if tt.wantErr {
				if err == nil {
					t.Fatal("All returned nil, want an error")
				}

				return
			}

			if err != nil {
				t.Fatalf("All: %v", err)
			}

			if len(report.Requires) != len(tt.requires) {
				t.Errorf("Requires = %v, want %v", report.Requires, tt.requires)
			}
		})
	}
}

// TestJudgeAuthEnvMustResolve covers why an empty credential is a hard failure
// rather than a fallback: a run whose provenance names one endpoint but which
// actually called another is worse than no run.
func TestJudgeAuthEnvMustResolve(t *testing.T) {
	f := newFixture(t, twoValidRows)
	f.req.Grader.Entry = "llm_judge"
	f.req.Grader.RequiresJudge = true
	f.req.Grader.Requires = []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput}
	f.req.JudgeModel = &evalspec.JudgeModelSpec{
		Protocol: evalspec.JudgeOpenAIChat,
		Endpoint: "https://example.invalid/v1",
		Auth:     evalspec.Auth{Type: evalspec.AuthBearerEnv, Env: "EVALEXEC_TEST_KEY_UNSET"},
	}

	_, err := validate.All(f.req, validate.Options{})
	if err == nil {
		t.Fatal("an unset credential environment variable must fail the pre-check")
	}

	if evalerr.StepOf(err) != validate.StepJudgeModel {
		t.Errorf("step = %q, want %q", evalerr.StepOf(err), validate.StepJudgeModel)
	}

	// With the variable set, the same request passes.
	t.Setenv("EVALEXEC_TEST_KEY_UNSET", "value")

	if _, err := validate.All(f.req, validate.Options{}); err != nil {
		t.Errorf("with the credential present the request must pass: %v", err)
	}
}

// stubChecker stands in for the Judge client construction that lands later.
type stubChecker struct{ err error }

func (s stubChecker) Check(*evalspec.JudgeModelSpec) error { return s.err }

func TestJudgeCheckerIsConsulted(t *testing.T) {
	f := newFixture(t, twoValidRows)
	f.req.Grader.Entry = "llm_judge"
	f.req.Grader.RequiresJudge = true
	f.req.Grader.Requires = []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput}
	f.req.JudgeModel = &evalspec.JudgeModelSpec{
		Protocol: evalspec.JudgeOpenAIChat,
		Endpoint: "https://example.invalid/v1",
		Auth:     evalspec.Auth{Type: evalspec.AuthNone},
	}

	want := errors.New("provider rejected the configuration")

	_, err := validate.All(f.req, validate.Options{Judge: stubChecker{err: want}})
	if err == nil {
		t.Fatal("a rejecting Judge checker must fail the pre-check")
	}

	if !errors.Is(err, want) {
		t.Errorf("error = %v, want it to wrap the checker's error", err)
	}

	if evalerr.StepOf(err) != validate.StepJudgeModel {
		t.Errorf("step = %q, want %q", evalerr.StepOf(err), validate.StepJudgeModel)
	}
}

// TestExternalGraderDeclarationIsTakenAtItsWord pins that an external Grader
// is still pre-checked, using the declaration from its configuration. Asking
// the external process would defeat the purpose: the point is to validate
// before contacting it.
func TestExternalGraderDeclarationIsTakenAtItsWord(t *testing.T) {
	f := newFixture(t, twoValidRows)
	f.req.Grader.Protocol = evalspec.GraderHTTPJSON
	f.req.Grader.Entry = "https://grader.example.invalid/grade"
	f.req.Grader.Requires = []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput}

	report, err := validate.All(f.req, validate.Options{})
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	if report.Declaration != nil {
		t.Error("an external Grader has no built-in declaration")
	}

	// And a row missing a declared field is still rejected.
	f2 := newFixture(t, `{"case_id":"c1","input":{}}`+"\n")
	f2.req.Grader = f.req.Grader

	if _, err := validate.All(f2.req, validate.Options{}); err == nil {
		t.Error("an external Grader must not lose the requires pre-check")
	}
}

// TestNoDirectoryIsCreated asserts that validation is read-only. A rejected
// run must leave nothing behind, and a passing one must not create the output
// directory either — that happens later, once there is something to write.
func TestNoDirectoryIsCreated(t *testing.T) {
	f := newFixture(t, twoValidRows)

	if _, err := validate.All(f.req, validate.Options{}); err != nil {
		t.Fatalf("All: %v", err)
	}

	if _, err := os.Stat(f.req.OutputDir); !os.IsNotExist(err) {
		t.Errorf("validation must not create the output directory (stat err = %v)", err)
	}

	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("validation left a temporary directory behind: %s", e.Name())
		}
	}
}

// TestUndeclaredParamsAreNotPoliced pins the distinction between a Grader that
// declares no parameters and one that declares an empty list.
//
// A downstream Grader that has not declared its parameter names should not have
// every parameter rejected with a message reading "accepts []" — that failure
// mode is what the nil case exists to avoid. Declaring the list buys
// the misspelling check; leaving it nil gives that up, which is the author's
// choice to make.
func TestUndeclaredParamsAreNotPoliced(t *testing.T) {
	tests := []struct {
		name    string
		params  []string
		wantErr bool
	}{
		{name: "nil means unchecked", params: nil, wantErr: false},
		{name: "empty means none accepted", params: []string{}, wantErr: true},
		{name: "declared and matching", params: []string{"knob"}, wantErr: false},
		{name: "declared and mismatched", params: []string{"other"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decl := grader.Declaration{
				Entry:    "custom",
				Requires: []evalspec.SessionField{evalspec.FieldInput},
				Params:   tt.params,
			}

			_, err := decl.EffectiveRequires(map[string]any{"knob": true})

			if tt.wantErr && err == nil {
				t.Error("EffectiveRequires returned nil, want an error")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("EffectiveRequires: %v", err)
			}
		})
	}
}
