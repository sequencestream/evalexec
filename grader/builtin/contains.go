package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/grader/declaration"
)

// DefaultContainsPath is where contains looks for the expected substrings.
const DefaultContainsPath = "$.expected_contains"

func init() {
	grader.Register(declaration.EntryContains, newContains)
}

type contains struct {
	referencePath string
	caseSensitive bool
}

func newContains(spec evalspec.GraderSpec) (grader.Grader, error) {
	path, err := stringParam(spec.Parameters, "reference_path", DefaultContainsPath)
	if err != nil {
		return nil, err
	}

	sensitive, err := boolParam(spec.Parameters, "case_sensitive", false)
	if err != nil {
		return nil, err
	}

	return &contains{referencePath: path, caseSensitive: sensitive}, nil
}

func (g *contains) Declare() grader.Declaration {
	d, _ := declaration.Lookup(declaration.EntryContains)

	return d
}

// Grade checks that every expected substring appears in the output.
//
// All of them must appear, not any: a reference listing three required phrases
// is a conjunction. Reporting a partial match as a success would let an answer
// containing one of three required facts score the same as a complete one.
func (g *contains) Grade(_ context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	reference, err := decode(call.Reference)
	if err != nil {
		return insufficient("reference is not valid JSON", err.Error()), nil
	}

	raw, ok := lookupPath(reference, g.referencePath)
	if !ok {
		return insufficient(
			"reference has no expected substrings to look for",
			fmt.Sprintf("reference path %s is absent", g.referencePath),
		), nil
	}

	wanted, err := asStringList(raw)
	if err != nil {
		return insufficient("reference is not a string or list of strings", err.Error()), nil
	}

	if len(wanted) == 0 {
		return insufficient(
			"reference lists no substrings to look for",
			fmt.Sprintf("reference path %s is empty", g.referencePath),
		), nil
	}

	text := textOf(call.Output)
	haystack := text

	if !g.caseSensitive {
		haystack = strings.ToLower(text)
	}

	var missing []string

	for _, w := range wanted {
		needle := w
		if !g.caseSensitive {
			needle = strings.ToLower(w)
		}

		if !strings.Contains(haystack, needle) {
			missing = append(missing, w)
		}
	}

	evidence := []evalspec.Evidence{
		{Source: sourceOutput, Path: "$", Value: text},
		{Source: sourceReference, Path: g.referencePath, Value: wanted},
	}

	if len(missing) > 0 {
		evidence = append(evidence, evalspec.Evidence{
			Source: sourceReference, Path: g.referencePath + " (missing)", Value: missing,
		})

		return success(scoreMismatch, LabelMismatch,
			fmt.Sprintf("output is missing %d of %d expected substrings", len(missing), len(wanted)),
			evidence), nil
	}

	return success(scoreMatch, LabelMatch,
		fmt.Sprintf("output contains all %d expected substrings", len(wanted)), evidence), nil
}

// asStringList accepts either a single string or a list of them.
func asStringList(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))

		for i, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, want a string", i, e)
			}

			out = append(out, s)
		}

		return out, nil
	default:
		return nil, fmt.Errorf("value is %T, want a string or a list of strings", v)
	}
}
