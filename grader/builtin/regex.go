package builtin

import (
	"context"
	"fmt"
	"regexp"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/grader/declaration"
)

func init() {
	grader.Register(declaration.EntryRegex, newRegex)
}

type regexGrader struct {
	pattern *regexp.Regexp
	source  string
}

// newRegex builds the Grader. The pattern was already compiled once during
// the pre-check, so a failure here would mean the configuration changed
// underneath us.
func newRegex(spec evalspec.GraderSpec, _ grader.Deps) (grader.Grader, error) {
	re, source, err := declaration.CompilePattern(spec.Parameters)
	if err != nil {
		return nil, err
	}

	return &regexGrader{pattern: re, source: source}, nil
}

func (g *regexGrader) Declare() grader.Declaration {
	d, _ := declaration.Lookup(declaration.EntryRegex)

	return d
}

// Grade searches the output for the pattern.
func (g *regexGrader) Grade(_ context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	text := textOf(call.Output)

	evidence := []evalspec.Evidence{
		{Source: sourceOutput, Path: "$", Value: text},
		{Source: sourceGrader, Path: "$.parameters.pattern", Value: g.source},
	}

	loc := g.pattern.FindStringIndex(text)
	if loc == nil {
		return success(scoreMismatch, LabelMismatch, "output does not match the pattern", evidence), nil
	}

	evidence = append(evidence, evalspec.Evidence{
		Source: sourceOutput, Path: fmt.Sprintf("$[%d:%d]", loc[0], loc[1]), Value: text[loc[0]:loc[1]],
	})

	return success(scoreMatch, LabelMatch, "output matches the pattern", evidence), nil
}
