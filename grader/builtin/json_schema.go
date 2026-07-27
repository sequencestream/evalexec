package builtin

import (
	"context"
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/grader/declaration"
)

// maxSchemaViolations caps how many violations reach the evidence list. A
// deeply wrong document can produce hundreds, and a record carrying all of
// them buries the first — which is the one worth reading.
const maxSchemaViolations = 10

func init() {
	grader.Register(declaration.EntryJSONSchema, newJSONSchema)
}

type jsonSchemaGrader struct {
	schema *jsonschema.Schema
}

// newJSONSchema builds the Grader. The schema was already compiled once during
// the pre-check.
func newJSONSchema(spec evalspec.GraderSpec, _ grader.Deps) (grader.Grader, error) {
	s, err := declaration.CompileSchema(spec.Parameters)
	if err != nil {
		return nil, err
	}

	return &jsonSchemaGrader{schema: s}, nil
}

func (g *jsonSchemaGrader) Declare() grader.Declaration {
	d, _ := declaration.Lookup(declaration.EntryJSONSchema)

	return d
}

// Grade validates the output against the schema.
//
// An invalid document is a successful evaluation scoring zero: the Grader
// checked it and reached a verdict. Only a document that cannot be parsed at
// all leaves nothing to check.
func (g *jsonSchemaGrader) Grade(_ context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	value, err := decode(call.Output)
	if err != nil {
		return insufficient("output is not valid JSON", err.Error()), nil
	}

	evidence := []evalspec.Evidence{evidenceOf(sourceOutput, "$", call.Output)}

	if err := g.schema.Validate(value); err != nil {
		var ve *jsonschema.ValidationError
		if !asValidationError(err, &ve) {
			return insufficient("schema validation could not be performed", err.Error()), nil
		}

		return success(scoreMismatch, LabelInvalid, "output does not satisfy the schema",
			append(evidence, violations(ve)...)), nil
	}

	return success(scoreMatch, LabelValid, "output satisfies the schema", evidence), nil
}

// violations flattens the validation error tree into evidence entries, deepest
// causes first — those name the actual offending field, while the outer levels
// only say that something below them was wrong.
func violations(ve *jsonschema.ValidationError) []evalspec.Evidence {
	var out []evalspec.Evidence

	var walk func(e *jsonschema.ValidationError)

	walk = func(e *jsonschema.ValidationError) {
		if len(out) >= maxSchemaViolations {
			return
		}

		if len(e.Causes) == 0 {
			// Error() rather than ErrorKind.LocalizedString: the latter needs
			// a message printer and panics on a nil one, while Error() uses
			// the library's own default. Accepting a printer here would pull
			// golang.org/x/text into the direct dependency set for the sake
			// of one string.
			out = append(out, evalspec.Evidence{
				Source: sourceOutput,
				Path:   instancePath(e),
				Value:  e.Error(),
			})

			return
		}

		for _, c := range e.Causes {
			walk(c)
		}
	}

	walk(ve)

	return out
}

// instancePath renders the JSON pointer of the offending location as a dotted
// path, so it reads like the other Graders' evidence paths.
func instancePath(e *jsonschema.ValidationError) string {
	if len(e.InstanceLocation) == 0 {
		return "$"
	}

	p := "$"
	for _, seg := range e.InstanceLocation {
		p += "." + seg
	}

	return p
}

// asValidationError is errors.As specialized for the schema library's error
// type, kept separate so the import of errors stays local to this concern.
func asValidationError(err error, target **jsonschema.ValidationError) bool {
	return errors.As(err, target)
}
