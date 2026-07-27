package builtin_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	_ "github.com/sequencestream/evalexec/grader/builtin"
)

// build constructs a built-in Grader from its entry name and parameters.
func build(t *testing.T, entry string, params map[string]any) grader.Grader {
	t.Helper()

	g, err := grader.Default().Build(evalspec.GraderSpec{
		ID: "g", Version: "v1", Protocol: evalspec.GraderBuiltin,
		Entry: entry, Parameters: params,
	})
	if err != nil {
		t.Fatalf("build %s: %v", entry, err)
	}

	return g
}

// call builds a grade call from raw JSON fragments.
func call(output, reference string) evalspec.GradeCall {
	c := evalspec.GradeCall{EvalID: "e", TaskID: "t", CaseID: "c"}

	if output != "" {
		c.Output = json.RawMessage(output)
	}

	if reference != "" {
		c.Reference = json.RawMessage(reference)
	}

	return c
}

// graded is the shape assertions are written against.
type graded struct {
	status evalspec.EvaluationStatus
	score  float64
	scored bool
	label  string
	code   evalspec.ErrorCode
}

func run(t *testing.T, g grader.Grader, c evalspec.GradeCall) graded {
	t.Helper()

	eval, err := g.Grade(t.Context(), c)
	if err != nil {
		t.Fatalf("Grade returned an error: %v", err)
	}

	out := graded{status: eval.Status}

	if eval.Score != nil {
		out.score, out.scored = *eval.Score, true
	}

	if eval.Label != nil {
		out.label = *eval.Label
	}

	if eval.Error != nil {
		out.code = eval.Error.Code
	}

	return out
}

// TestMismatchIsSuccessNotFailure is the rule most likely to be implemented
// backwards, so it is asserted first and on its own.
//
// A Grader that compared two values and found them different did its job. That
// is a success reporting zero. A failure means no conclusion was reached at
// all, and a failure carries no score rather than a zero — otherwise a number
// nobody measured drags down the average.
func TestMismatchIsSuccessNotFailure(t *testing.T) {
	g := build(t, "exact_match", nil)

	got := run(t, g, call(`{"a":1}`, `{"expected_output":{"a":2}}`))

	if got.status != evalspec.EvaluationSuccess {
		t.Errorf("status = %q, want success: reaching the conclusion 'these differ' is a successful evaluation", got.status)
	}

	if !got.scored || got.score != 0 {
		t.Errorf("score = %v (scored=%v), want 0", got.score, got.scored)
	}

	// And the contrast: no reference to compare against is a failure with no
	// score at all.
	missing := run(t, g, call(`{"a":1}`, `{"note":"no expectation recorded"}`))

	if missing.status != evalspec.EvaluationFail {
		t.Errorf("status = %q, want fail when there is nothing to compare against", missing.status)
	}

	if missing.scored {
		t.Errorf("a failed evaluation carries score %v, want none", missing.score)
	}

	if missing.code != evalspec.CodeInsufficientEvidence {
		t.Errorf("code = %q, want insufficient_evidence", missing.code)
	}
}

func TestExactMatch(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]any
		output    string
		reference string
		want      graded
	}{
		{
			name:      "identical objects",
			output:    `{"status":"shipping","eta":"2026-07-28"}`,
			reference: `{"expected_output":{"status":"shipping","eta":"2026-07-28"}}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name: "key order does not matter",
			// Comparison is semantic, not textual: two documents that mean
			// the same thing must grade the same.
			output:    `{"eta":"2026-07-28","status":"shipping"}`,
			reference: `{"expected_output":{"status":"shipping","eta":"2026-07-28"}}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "null equals null",
			output:    `null`,
			reference: `{"expected_output":null}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "null against a value",
			output:    `null`,
			reference: `{"expected_output":{"a":1}}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "mismatch"},
		},
		{
			name:      "integer and float are the same number",
			output:    `{"n":1}`,
			reference: `{"expected_output":{"n":1.0}}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "nested difference",
			output:    `{"a":{"b":[1,2,3]}}`,
			reference: `{"expected_output":{"a":{"b":[1,2,4]}}}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "mismatch"},
		},
		{
			name:      "custom reference path",
			params:    map[string]any{"reference_path": "$.golden.answer"},
			output:    `"42"`,
			reference: `{"golden":{"answer":"42"}}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "custom reference path is absent",
			params:    map[string]any{"reference_path": "$.golden.answer"},
			output:    `"42"`,
			reference: `{"golden":{}}`,
			want:      graded{status: evalspec.EvaluationFail, code: evalspec.CodeInsufficientEvidence},
		},
		{
			name:      "reference itself is absent",
			output:    `{"a":1}`,
			reference: ``,
			want:      graded{status: evalspec.EvaluationFail, code: evalspec.CodeInsufficientEvidence},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertGraded(t, run(t, build(t, "exact_match", tt.params), call(tt.output, tt.reference)), tt.want)
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]any
		output    string
		reference string
		want      graded
	}{
		{
			name:      "single substring present",
			output:    `"订单正在配送，预计明天送达"`,
			reference: `{"expected_contains":"配送"}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "single substring absent",
			output:    `"订单已签收"`,
			reference: `{"expected_contains":"配送"}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "mismatch"},
		},
		{
			name: "every listed substring must appear",
			// A list is a conjunction. Treating it as a disjunction would let
			// an answer with one of three required facts score like a
			// complete one.
			output:    `"the order shipped"`,
			reference: `{"expected_contains":["order","shipped","tomorrow"]}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "mismatch"},
		},
		{
			name:      "all substrings present",
			output:    `"the order shipped and arrives tomorrow"`,
			reference: `{"expected_contains":["order","shipped","tomorrow"]}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "case-insensitive by default",
			output:    `"The Order Shipped"`,
			reference: `{"expected_contains":"order shipped"}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "case-sensitive when asked",
			params:    map[string]any{"case_sensitive": true},
			output:    `"The Order Shipped"`,
			reference: `{"expected_contains":"order shipped"}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "mismatch"},
		},
		{
			name: "structured output is searched as compact JSON",
			// The Grader must not have to guess where the "real" text lives
			// inside a structured answer.
			output:    `{"messages":[{"content":"order shipped"}]}`,
			reference: `{"expected_contains":"order shipped"}`,
			want:      graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:      "reference is not a string",
			output:    `"anything"`,
			reference: `{"expected_contains":42}`,
			want:      graded{status: evalspec.EvaluationFail, code: evalspec.CodeInsufficientEvidence},
		},
		{
			name:      "empty substring list",
			output:    `"anything"`,
			reference: `{"expected_contains":[]}`,
			want:      graded{status: evalspec.EvaluationFail, code: evalspec.CodeInsufficientEvidence},
		},
		{
			name:      "reference path absent",
			output:    `"anything"`,
			reference: `{}`,
			want:      graded{status: evalspec.EvaluationFail, code: evalspec.CodeInsufficientEvidence},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertGraded(t, run(t, build(t, "contains", tt.params), call(tt.output, tt.reference)), tt.want)
		})
	}
}

func TestRegex(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		output string
		want   graded
	}{
		{
			name:   "pattern matches",
			params: map[string]any{"pattern": `ORD-\d+`},
			output: `"your order ORD-1234 is on the way"`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:   "pattern does not match",
			params: map[string]any{"pattern": `ORD-\d+`},
			output: `"we could not find that order"`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "mismatch"},
		},
		{
			name:   "case-insensitive by default",
			params: map[string]any{"pattern": `ord-\d+`},
			output: `"ORD-1234"`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
		{
			name:   "case-sensitive when asked",
			params: map[string]any{"pattern": `ord-\d+`, "case_sensitive": true},
			output: `"ORD-1234"`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "mismatch"},
		},
		{
			name:   "anchors apply to the whole text",
			params: map[string]any{"pattern": `^done$`},
			output: `"done"`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "match"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertGraded(t, run(t, build(t, "regex", tt.params), call(tt.output, "")), tt.want)
		})
	}
}

// TestRegexRejectsABadPatternAtBuildTime pins that a broken pattern is a
// configuration error, not a per-sample failure. Failing on sample one and
// failing on sample one thousand are the same defect; saying so before the run
// starts is strictly better.
func TestRegexRejectsABadPatternAtBuildTime(t *testing.T) {
	for _, params := range []map[string]any{
		nil,
		{},
		{"pattern": ""},
		{"pattern": `([unclosed`},
		{"pattern": 42},
	} {
		_, err := grader.Default().Build(evalspec.GraderSpec{
			ID: "g", Version: "v1", Protocol: evalspec.GraderBuiltin,
			Entry: "regex", Parameters: params,
		})
		if err == nil {
			t.Errorf("params %v must be rejected when the Grader is built", params)
		}
	}
}

func TestJSONSchema(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"status"},
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []any{"shipping", "delivered"}},
		},
	}

	tests := []struct {
		name   string
		output string
		want   graded
	}{
		{
			name:   "valid document",
			output: `{"status":"shipping"}`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "valid"},
		},
		{
			name:   "missing required property",
			output: `{}`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "invalid"},
		},
		{
			name:   "value outside the enum",
			output: `{"status":"pending"}`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "invalid"},
		},
		{
			name:   "wrong type entirely",
			output: `"a string, not an object"`,
			want:   graded{status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "invalid"},
		},
	}

	g := build(t, "json_schema", map[string]any{"schema": schema})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertGraded(t, run(t, g, call(tt.output, "")), tt.want)
		})
	}
}

func TestJSONSchemaRejectsABadSchemaAtBuildTime(t *testing.T) {
	for _, params := range []map[string]any{
		nil,
		{},
		{"schema": "not an object"},
		{"schema": map[string]any{"type": "not-a-type"}},
	} {
		_, err := grader.Default().Build(evalspec.GraderSpec{
			ID: "g", Version: "v1", Protocol: evalspec.GraderBuiltin,
			Entry: "json_schema", Parameters: params,
		})
		if err == nil {
			t.Errorf("params %v must be rejected when the Grader is built", params)
		}
	}
}

// TestJSONSchemaEvidenceNamesTheOffendingField checks that a failure report is
// usable, not merely correct.
func TestJSONSchemaEvidenceNamesTheOffendingField(t *testing.T) {
	g := build(t, "json_schema", map[string]any{
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count": map[string]any{"type": "integer"},
			},
		},
	})

	eval, err := g.Grade(t.Context(), call(`{"count":"twelve"}`, ""))
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}

	var found bool

	for _, e := range eval.Evidence {
		if strings.Contains(e.Path, "count") {
			found = true
		}
	}

	if !found {
		t.Errorf("evidence should name the offending field, got %+v", eval.Evidence)
	}
}

// TestDeclarationsMatchTheSpecification pins the fixed table.
func TestDeclarationsMatchTheSpecification(t *testing.T) {
	tests := []struct {
		entry         string
		requires      []evalspec.SessionField
		requiresJudge bool
		params        map[string]any
	}{
		{
			entry:    "exact_match",
			requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference},
		},
		{
			entry:    "contains",
			requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference},
		},
		{
			entry:    "regex",
			requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
			params:   map[string]any{"pattern": "x"},
		},
		{
			entry:    "json_schema",
			requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
			params:   map[string]any{"schema": map[string]any{"type": "object"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			d := build(t, tt.entry, tt.params).Declare()

			if d.RequiresJudge != tt.requiresJudge {
				t.Errorf("requires_judge = %v, want %v", d.RequiresJudge, tt.requiresJudge)
			}

			if len(d.Requires) != len(tt.requires) {
				t.Fatalf("requires = %v, want %v", d.Requires, tt.requires)
			}

			for i, f := range tt.requires {
				if d.Requires[i] != f {
					t.Errorf("requires[%d] = %q, want %q", i, d.Requires[i], f)
				}
			}
		})
	}
}

func assertGraded(t *testing.T, got, want graded) {
	t.Helper()

	if got.status != want.status {
		t.Errorf("status = %q, want %q", got.status, want.status)
	}

	if got.scored != want.scored {
		t.Errorf("scored = %v, want %v (score = %v)", got.scored, want.scored, got.score)
	}

	if want.scored && got.score != want.score {
		t.Errorf("score = %v, want %v", got.score, want.score)
	}

	if want.label != "" && got.label != want.label {
		t.Errorf("label = %q, want %q", got.label, want.label)
	}

	if want.code != "" && got.code != want.code {
		t.Errorf("error code = %q, want %q", got.code, want.code)
	}
}
