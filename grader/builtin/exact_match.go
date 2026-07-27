package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/grader/declaration"
)

// DefaultReferencePath is where exact_match looks for the expected value.
const DefaultReferencePath = "$.expected_output"

func init() {
	grader.Register(declaration.EntryExactMatch, newExactMatch)
}

type exactMatch struct {
	referencePath string
}

func newExactMatch(spec evalspec.GraderSpec) (grader.Grader, error) {
	path, err := stringParam(spec.Parameters, "reference_path", DefaultReferencePath)
	if err != nil {
		return nil, err
	}

	return &exactMatch{referencePath: path}, nil
}

func (g *exactMatch) Declare() grader.Declaration {
	d, _ := declaration.Lookup(declaration.EntryExactMatch)

	return d
}

// Grade compares the output to the expected value for semantic JSON equality.
func (g *exactMatch) Grade(_ context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	reference, err := decode(call.Reference)
	if err != nil {
		return insufficient("reference is not valid JSON", err.Error()), nil
	}

	expected, ok := lookupPath(reference, g.referencePath)
	if !ok {
		// Nothing to compare against, so no conclusion is possible. This is a
		// failure, not a zero: scoring it zero would put a number nobody
		// measured into the average.
		return insufficient(
			"reference has no expected_output to compare against",
			fmt.Sprintf("%s is absent", referenceLabel(g.referencePath)),
		), nil
	}

	actual, err := decode(call.Output)
	if err != nil {
		return insufficient("output is not valid JSON", err.Error()), nil
	}

	evidence := []evalspec.Evidence{
		evidenceOf(sourceOutput, "$", call.Output),
		{Source: sourceReference, Path: g.referencePath, Value: expected},
	}

	// A mismatch is a successful evaluation reporting a score of zero: the
	// Grader compared the values and reached a conclusion.
	if !jsonEqual(actual, expected) {
		return success(scoreMismatch, LabelMismatch,
			"output differs from "+referenceLabel(g.referencePath), evidence), nil
	}

	return success(scoreMatch, LabelMatch,
		"output equals "+referenceLabel(g.referencePath), evidence), nil
}

// referenceLabel names the compared field the way the published fixtures do,
// so a reason string reads the same whether the default path is in use or a
// custom one.
func referenceLabel(path string) string {
	if path == DefaultReferencePath {
		return "reference.expected_output"
	}

	return "reference at " + path
}

// jsonEqual compares two decoded JSON values semantically.
//
// Comparison is on decoded values rather than on bytes so that key order and
// whitespace cannot change the verdict — two documents that mean the same
// thing must grade the same.
func jsonEqual(a, b any) bool {
	return reflect.DeepEqual(normalizeNumbers(a), normalizeNumbers(b))
}

// normalizeNumbers is a no-op today and exists as the single place to handle
// numeric representation should it ever matter. encoding/json decodes every
// number to float64, so 1 and 1.0 already compare equal, which is the
// behaviour a reference file written by hand expects.
func normalizeNumbers(v any) any { return v }

// compile-time check that the raw message type is what the call carries.
var _ = json.RawMessage(nil)
