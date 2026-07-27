package evalspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// SessionField names one of the seven gradeable fields of a Session. It is
// also the element type of Grader.requires.
//
// case_id is deliberately not a SessionField: the specification requires it
// on every row unconditionally, so listing it in requires would be a legal
// but meaningless declaration.
type SessionField string

// The seven fields a Grader may declare in requires.
const (
	// FieldInput is the raw input the agent received.
	FieldInput SessionField = "input"
	// FieldOutput is the agent's final output and terminal state. Its value
	// is explicitly null when the agent produced no final output — which is
	// still a present key, and therefore satisfies requires.
	FieldOutput SessionField = "output"
	// FieldTrajectory is the ordered execution process, normalized into steps.
	FieldTrajectory SessionField = "trajectory"
	// FieldReference is the expected result, expected tools, or process
	// constraints.
	FieldReference SessionField = "reference"
	// FieldContext is read-only background or tool definitions visible to the
	// agent at execution time.
	FieldContext SessionField = "context"
	// FieldCriteria is a per-sample assertion or rubric.
	FieldCriteria SessionField = "criteria"
	// FieldMetadata is session, agent version, tag and provenance data.
	FieldMetadata SessionField = "metadata"
)

// allSessionFields is the canonical order used by AllSessionFields and by
// deterministic serialization.
var allSessionFields = [...]SessionField{
	FieldInput, FieldOutput, FieldTrajectory, FieldReference,
	FieldContext, FieldCriteria, FieldMetadata,
}

// AllSessionFields returns the seven valid requires elements in canonical
// order. It exists so downstream code can enumerate them without copying the
// constant list.
func AllSessionFields() []SessionField {
	return slices.Clone(allSessionFields[:])
}

// IsValid reports whether f is one of the seven gradeable fields.
func (f SessionField) IsValid() bool {
	return slices.Contains(allSessionFields[:], f)
}

func (f SessionField) String() string { return string(f) }

// Session is one already-executed agent session: a single dataset row.
//
// Field values are kept as raw JSON rather than decoded structures for two
// reasons. A Grader must not alter the original session content, and raw
// bytes are the strongest guarantee of that — nothing round-trips through a Go
// type, so number precision, key order and unknown fields all survive intact.
// The internal shape of input and output is also decided by the upstream
// agent framework, which EvalExec explicitly does not try to understand.
//
// The zero Session is not usable; construct one with NewSession or by
// unmarshalling a dataset line.
type Session struct {
	// CaseID is unique within the dataset file.
	CaseID string

	// fields holds only the keys that were actually present in the row. A key
	// mapping to the four bytes "null" is present-with-a-null-value, which is
	// a different thing from being absent — see Has and IsNull.
	fields map[SessionField]json.RawMessage
}

// ErrDuplicateCaseID is reported when a dataset contains the same case_id
// twice. It is defined here because case_id uniqueness is a property of the
// protocol, not of any one reader.
var ErrDuplicateCaseID = errors.New("duplicate case_id")

// NewSession builds a Session from a case ID and the fields that are present.
// A key mapped to a nil or empty value is stored as JSON null, i.e. present.
// To leave a field absent, omit the key entirely.
//
// It returns an error for an empty case ID or an unknown field name.
func NewSession(caseID string, fields map[SessionField]json.RawMessage) (*Session, error) {
	if caseID == "" {
		return nil, errors.New("evalspec: case_id must not be empty")
	}

	s := &Session{CaseID: caseID, fields: make(map[SessionField]json.RawMessage, len(fields))}

	for f, v := range fields {
		if !f.IsValid() {
			return nil, fmt.Errorf("evalspec: %q is not a valid session field", f)
		}

		if len(v) == 0 {
			v = jsonNull
		}

		s.fields[f] = slices.Clone(v)
	}

	return s, nil
}

var jsonNull = json.RawMessage("null")

// Has reports whether the field key was present in the session row, whatever
// its value. This — not a nil check on the value — is what a requires
// declaration is validated against: "output": null satisfies requires
// ["output"], because the agent legitimately produced no final output.
func (s *Session) Has(f SessionField) bool {
	_, ok := s.fields[f]

	return ok
}

// IsNull reports whether the field is present and its value is JSON null.
// An absent field is not null; it is absent. Callers that care about the
// difference must consult Has first.
func (s *Session) IsNull(f SessionField) bool {
	v, ok := s.fields[f]

	return ok && isJSONNull(v)
}

// Field returns the raw JSON value of the field, or nil when the field is
// absent. The returned slice is a copy; mutating it cannot corrupt the
// session.
func (s *Session) Field(f SessionField) json.RawMessage {
	v, ok := s.fields[f]
	if !ok {
		return nil
	}

	return slices.Clone(v)
}

// Fields returns the present field names in canonical order.
func (s *Session) Fields() []SessionField {
	present := make([]SessionField, 0, len(s.fields))

	for _, f := range allSessionFields {
		if _, ok := s.fields[f]; ok {
			present = append(present, f)
		}
	}

	return present
}

// MissingFields returns which of the required fields are absent from this
// session, in the order they were requested. An empty result means the
// session satisfies the declaration.
func (s *Session) MissingFields(required []SessionField) []SessionField {
	var missing []SessionField

	for _, f := range required {
		if !s.Has(f) {
			missing = append(missing, f)
		}
	}

	return missing
}

// isJSONNull reports whether raw is the JSON null literal, tolerating
// surrounding whitespace.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), jsonNull)
}

// UnmarshalJSON decodes one dataset row, preserving which keys were present.
//
// The row is decoded into a map rather than a struct because a struct cannot
// express the distinction this type exists for. encoding/json resolves a JSON
// null against a pointer field by setting the pointer to nil — exactly what an
// absent key produces — so even *json.RawMessage fields would collapse
// "output": null and a missing "output" into the same state. Decoding to a map
// keyed by the raw JSON names keeps a null-valued key as a present key whose
// value is the four bytes "null".
func (s *Session) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	idRaw, ok := raw["case_id"]
	if !ok {
		return errors.New("evalspec: case_id is required")
	}

	var caseID string
	if err := json.Unmarshal(idRaw, &caseID); err != nil {
		return fmt.Errorf("evalspec: case_id must be a string: %w", err)
	}

	if caseID == "" {
		return errors.New("evalspec: case_id must not be empty")
	}

	s.CaseID = caseID
	s.fields = make(map[SessionField]json.RawMessage, len(allSessionFields))

	// Unrecognized keys are ignored rather than rejected: forward
	// compatibility depends on it (02-core-spec.md §1).
	for _, f := range allSessionFields {
		v, present := raw[string(f)]
		if !present {
			continue
		}

		if len(v) == 0 {
			v = jsonNull
		}

		s.fields[f] = slices.Clone(v)
	}

	return nil
}

// MarshalJSON re-encodes the row, emitting exactly the keys that were
// present. Absent fields stay absent; null-valued fields stay null.
func (s Session) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString(`{"case_id":`)

	id, err := json.Marshal(s.CaseID)
	if err != nil {
		return nil, err
	}

	buf.Write(id)

	// Canonical field order keeps golden-file comparison stable.
	for _, f := range allSessionFields {
		v, ok := s.fields[f]
		if !ok {
			continue
		}

		buf.WriteString(`,"`)
		buf.WriteString(string(f))
		buf.WriteString(`":`)

		if len(v) == 0 {
			buf.Write(jsonNull)
		} else {
			buf.Write(v)
		}
	}

	buf.WriteString("}")

	return buf.Bytes(), nil
}
