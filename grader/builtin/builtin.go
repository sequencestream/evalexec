// Package builtin implements the rule-based Graders that ship with EvalExec.
//
// They share one rule that is easy to get backwards: reaching a conclusion is
// a success, whatever the conclusion. A Grader that compares an answer to its
// reference and finds them different has done its job perfectly — that is a
// success with a score of zero, not a failure. Only being unable to conclude
// at all — no reference to compare against, a Judge that would not answer — is
// a failure, and a failure carries no score rather than a zero.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise. The graders' observable behaviour is pinned by the
// shared fixtures.
package builtin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader/declaration"
)

// Evidence source names, matching the session field a cited value came from.
const (
	sourceOutput    = "output"
	sourceReference = "reference"
	sourceGrader    = "grader"
)

// Labels reported by the rule Graders.
const (
	LabelMatch    = "match"
	LabelMismatch = "mismatch"
	LabelValid    = "valid"
	LabelInvalid  = "invalid"
)

// scoreMatch and scoreMismatch are the two scores a rule Grader reports. They
// are the Grader's own scale, which EvalExec passes through without
// interpreting: nothing here decides whether 0 means "failed".
var (
	scoreMatch    = 1.0
	scoreMismatch = 0.0
)

// success builds a successful evaluation with a score and label.
func success(score float64, label, reason string, evidence []evalspec.Evidence) evalspec.Evaluation {
	s, l := score, label

	return evalspec.NewSuccessEvaluation(&s, &l, reason, evidence, evalspec.Usage{}, 0)
}

// insufficient builds the failure a rule Grader reports when it has nothing to
// compare against.
func insufficient(reason, detail string) evalspec.Evaluation {
	return evalspec.NewFailEvaluation(evalspec.CodeInsufficientEvidence, detail, reason, nil, evalspec.Usage{}, 0)
}

// textOf renders a raw JSON value as the text a substring or pattern Grader
// searches.
//
// A JSON string yields its unquoted contents, so a pattern written against the
// visible answer matches. Anything else yields its compact JSON, so a
// structured output is still searchable without the Grader having to guess
// where the "real" text lives.
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	return string(raw)
}

// decode unmarshals raw JSON into a generic value.
func decode(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}

	return v, nil
}

// lookupPath resolves a dotted path such as "$.expected_output" against a
// decoded value, reporting whether the key exists.
//
// The path grammar is deliberately tiny: a leading "$" and dotted keys, with
// numeric segments indexing arrays. A full JSONPath implementation would be a
// dependency and a specification of its own; the reference documents only ever
// use simple key access.
func lookupPath(v any, path string) (any, bool) {
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")

	if path == "" {
		return v, true
	}

	cur := v

	for seg := range strings.SplitSeq(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}

			cur = next
		case []any:
			i, err := parseIndex(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}

			cur = node[i]
		default:
			return nil, false
		}
	}

	return cur, true
}

func parseIndex(s string) (int, error) {
	var i int
	if _, err := fmt.Sscanf(s, "%d", &i); err != nil {
		return 0, err
	}

	return i, nil
}

// Parameter accessors live in the declaration package, next to the table that
// says which parameters exist, so the pre-check and the Grader cannot disagree
// about how a value is read.
var (
	stringParam = declaration.StringParam
	boolParam   = declaration.BoolParam
)

// evidenceOf builds one evidence entry, decoding the raw value so the result
// carries structure rather than an escaped JSON string.
func evidenceOf(source, path string, raw json.RawMessage) evalspec.Evidence {
	v, err := decode(raw)
	if err != nil {
		return evalspec.Evidence{Source: source, Path: path, Value: string(raw)}
	}

	return evalspec.Evidence{Source: source, Path: path, Value: v}
}
