package evalspec_test

import (
	"encoding/json"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
)

// TestSessionThreeState is the most important test in this package. A Grader
// declaring requires:["output"] must accept a row whose output is explicitly
// null — the agent produced no final output — while rejecting a row with no
// output key at all. Ordinary Go struct decoding cannot tell those apart.
func TestSessionThreeState(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantHas    bool
		wantIsNull bool
		wantField  string
	}{
		{
			name:       "key absent",
			line:       `{"case_id":"c1","input":{"q":1}}`,
			wantHas:    false,
			wantIsNull: false,
			wantField:  "",
		},
		{
			name:       "key present with null value",
			line:       `{"case_id":"c1","input":{"q":1},"output":null}`,
			wantHas:    true,
			wantIsNull: true,
			wantField:  "null",
		},
		{
			name:       "key present with a value",
			line:       `{"case_id":"c1","input":{"q":1},"output":{"a":2}}`,
			wantHas:    true,
			wantIsNull: false,
			wantField:  `{"a":2}`,
		},
		{
			name:       "key present with an empty object",
			line:       `{"case_id":"c1","output":{}}`,
			wantHas:    true,
			wantIsNull: false,
			wantField:  `{}`,
		},
		{
			name:       "key present with false",
			line:       `{"case_id":"c1","output":false}`,
			wantHas:    true,
			wantIsNull: false,
			wantField:  `false`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s evalspec.Session
			if err := json.Unmarshal([]byte(tt.line), &s); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got := s.Has(evalspec.FieldOutput); got != tt.wantHas {
				t.Errorf("Has(output) = %v, want %v", got, tt.wantHas)
			}

			if got := s.IsNull(evalspec.FieldOutput); got != tt.wantIsNull {
				t.Errorf("IsNull(output) = %v, want %v", got, tt.wantIsNull)
			}

			if got := string(s.Field(evalspec.FieldOutput)); got != tt.wantField {
				t.Errorf("Field(output) = %q, want %q", got, tt.wantField)
			}
		})
	}
}

// TestSessionAbsentIsNotNull states the distinction the whole design rests
// on, so a future refactor that collapses it fails loudly here.
func TestSessionAbsentIsNotNull(t *testing.T) {
	var s evalspec.Session
	if err := json.Unmarshal([]byte(`{"case_id":"c1"}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if s.IsNull(evalspec.FieldOutput) {
		t.Error("an absent field must not report as null: absent and null are different states")
	}
}

func TestSessionMissingFields(t *testing.T) {
	var s evalspec.Session
	if err := json.Unmarshal([]byte(`{"case_id":"c1","input":{},"output":null}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// output is present-but-null, which satisfies the requirement.
	required := []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput}
	if got := s.MissingFields(required); len(got) != 0 {
		t.Errorf("MissingFields = %v, want none: a null output is still a present output", got)
	}

	required = append(required, evalspec.FieldReference, evalspec.FieldTrajectory)

	got := s.MissingFields(required)
	if len(got) != 2 || got[0] != evalspec.FieldReference || got[1] != evalspec.FieldTrajectory {
		t.Errorf("MissingFields = %v, want [reference trajectory] in request order", got)
	}
}

// TestSessionRoundTrip checks that re-encoding a row preserves exactly which
// keys were present, which is what the requires check depends on.
func TestSessionRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "absent keys stay absent",
			line: `{"case_id":"c1","input":{"q":1}}`,
			want: `{"case_id":"c1","input":{"q":1}}`,
		},
		{
			name: "null keys stay null",
			line: `{"case_id":"c1","input":{},"output":null}`,
			want: `{"case_id":"c1","input":{},"output":null}`,
		},
		{
			name: "fields are emitted in canonical order",
			line: `{"metadata":{"m":1},"case_id":"c1","output":{"o":1},"input":{"i":1}}`,
			want: `{"case_id":"c1","input":{"i":1},"output":{"o":1},"metadata":{"m":1}}`,
		},
		{
			name: "unrecognized fields are ignored, not preserved",
			line: `{"case_id":"c1","input":{},"future_field":{"x":1}}`,
			want: `{"case_id":"c1","input":{}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s evalspec.Session
			if err := json.Unmarshal([]byte(tt.line), &s); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			got, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			if string(got) != tt.want {
				t.Errorf("round trip =\n  %s\nwant\n  %s", got, tt.want)
			}
		})
	}
}

// TestSessionPreservesRawBytes checks that values do not round-trip through a
// Go type. A large integer would lose precision via float64, and a Grader
// must see the session content unaltered.
func TestSessionPreservesRawBytes(t *testing.T) {
	const line = `{"case_id":"c1","output":{"id":12345678901234567890,"f":1.7976931348623157e308}}`

	var s evalspec.Session
	if err := json.Unmarshal([]byte(line), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := `{"id":12345678901234567890,"f":1.7976931348623157e308}`
	if got := string(s.Field(evalspec.FieldOutput)); got != want {
		t.Errorf("Field(output) = %s\nwant %s\n(raw bytes must survive: a Grader must not see altered session content)", got, want)
	}
}

// TestSessionFieldCopyIsDefensive checks that a caller mutating a returned
// value cannot corrupt the session.
func TestSessionFieldCopyIsDefensive(t *testing.T) {
	var s evalspec.Session
	if err := json.Unmarshal([]byte(`{"case_id":"c1","output":{"a":1}}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := s.Field(evalspec.FieldOutput)
	got[0] = 'X'

	if v := string(s.Field(evalspec.FieldOutput)); v != `{"a":1}` {
		t.Errorf("session corrupted by caller mutation: %s", v)
	}
}

func TestSessionRejectsEmptyCaseID(t *testing.T) {
	var s evalspec.Session
	if err := json.Unmarshal([]byte(`{"input":{}}`), &s); err == nil {
		t.Error("unmarshal of a row without case_id must fail")
	}

	if _, err := evalspec.NewSession("", nil); err == nil {
		t.Error("NewSession with an empty case_id must fail")
	}
}

func TestNewSession(t *testing.T) {
	s, err := evalspec.NewSession("c1", map[evalspec.SessionField]json.RawMessage{
		evalspec.FieldInput:  json.RawMessage(`{"q":1}`),
		evalspec.FieldOutput: nil, // an explicit nil means present-with-null
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if !s.Has(evalspec.FieldOutput) || !s.IsNull(evalspec.FieldOutput) {
		t.Error("a nil value must be stored as present-with-null, not as absent")
	}

	if s.Has(evalspec.FieldReference) {
		t.Error("a field never passed in must be absent")
	}

	if _, err := evalspec.NewSession("c1", map[evalspec.SessionField]json.RawMessage{
		"not_a_field": json.RawMessage(`1`),
	}); err == nil {
		t.Error("NewSession must reject an unknown field name")
	}
}

func TestSessionFieldIsValid(t *testing.T) {
	for _, f := range evalspec.AllSessionFields() {
		if !f.IsValid() {
			t.Errorf("%q must be valid", f)
		}
	}

	// case_id is required unconditionally, so listing it in requires would be
	// legal-looking but meaningless.
	for _, f := range []evalspec.SessionField{"", "case_id", "Input", "outputs"} {
		if f.IsValid() {
			t.Errorf("%q must not be a valid session field", f)
		}
	}

	if got := len(evalspec.AllSessionFields()); got != 7 {
		t.Errorf("AllSessionFields returned %d fields, want 7", got)
	}
}

func TestNewGradeCallCarriesOnlyPresentFields(t *testing.T) {
	var s evalspec.Session
	if err := json.Unmarshal([]byte(`{"case_id":"c1","input":{"q":1},"output":null}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	call := evalspec.NewGradeCall("eval-1", "task-1", &s, map[string]any{"k": "v"})

	if string(call.Input) != `{"q":1}` {
		t.Errorf("Input = %s", call.Input)
	}

	if string(call.Output) != "null" {
		t.Errorf("Output = %s, want the null literal: present-with-null must reach the Grader as such", call.Output)
	}

	if call.Reference != nil {
		t.Errorf("Reference = %s, want nil for an absent field", call.Reference)
	}

	if call.EvalID != "eval-1" || call.TaskID != "task-1" || call.CaseID != "c1" {
		t.Errorf("identifiers not carried: %+v", call)
	}
}
