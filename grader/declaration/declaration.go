// Package declaration holds what each built-in Grader requires of a session.
//
// It is a package of its own, below both validate and grader, because the
// pre-check phase must know a Grader's requirements before any Grader is
// constructed. Putting the table in the grader package would make validation
// depend on the implementations it exists to validate.
//
// For a built-in Grader the declaration is authoritative and the configuration
// file merely restates it: a mismatch is a configuration error. The Grader's
// own code decides what it needs to do its job, and a file claiming otherwise
// is wrong rather than persuasive.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise. The declarations themselves are part of the
// specification and change only with it.
package declaration

import (
	"fmt"
	"maps"
	"slices"

	"github.com/sequencestream/evalexec/evalspec"
)

// Built-in Grader entry names.
const (
	EntryExactMatch = "exact_match"
	EntryContains   = "contains"
	EntryRegex      = "regex"
	EntryJSONSchema = "json_schema"
	EntryLLMJudge   = "llm_judge"
)

// Declaration is a Grader's statement of what it needs, which is what lets
// EvalExec validate a run end to end without understanding the Grader.
type Declaration struct {
	// Entry is the built-in Grader name.
	Entry string
	// Requires is the base requirement list, before parameters are consulted.
	Requires []evalspec.SessionField
	// RequiresJudge says whether a judge_model must be configured.
	RequiresJudge bool
	// Params names the parameters this Grader understands. A parameter
	// outside this set is an error rather than a silent no-op: a misspelled
	// rubric key that is quietly ignored produces a run that looks fine and
	// graded the wrong thing.
	Params []string
	// Optional lists parameters that may extend Requires, described by which
	// field each one adds when true.
	Optional map[string]evalspec.SessionField
}

// builtins is the fixed table from the specification.
var builtins = map[string]Declaration{
	EntryExactMatch: {
		Entry:         EntryExactMatch,
		Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference},
		RequiresJudge: false,
		Params:        []string{"reference_path"},
	},
	EntryContains: {
		Entry:         EntryContains,
		Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference},
		RequiresJudge: false,
		Params:        []string{"reference_path", "case_sensitive"},
	},
	EntryRegex: {
		Entry:         EntryRegex,
		Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
		RequiresJudge: false,
		Params:        []string{"pattern", "case_sensitive"},
	},
	EntryJSONSchema: {
		Entry:         EntryJSONSchema,
		Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
		RequiresJudge: false,
		Params:        []string{"schema"},
	},
	EntryLLMJudge: {
		Entry:         EntryLLMJudge,
		Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
		RequiresJudge: true,
		Params:        []string{"rubric", "min_score", "max_score", "use_reference", "use_trajectory"},
		// The specification says llm_judge "may add reference or trajectory
		// according to its parameters" without saying how. These two booleans
		// are that rule made explicit: anything less would leave the pre-check
		// unable to decide whether a dataset satisfies the Grader.
		Optional: map[string]evalspec.SessionField{
			"use_reference":  evalspec.FieldReference,
			"use_trajectory": evalspec.FieldTrajectory,
		},
	},
}

// Lookup returns the declaration for a built-in entry.
func Lookup(entry string) (Declaration, bool) {
	d, ok := builtins[entry]

	return d, ok
}

// Entries returns the known built-in entry names, sorted.
func Entries() []string {
	return slices.Sorted(maps.Keys(builtins))
}

// EffectiveRequires resolves what this Grader needs given its parameters.
//
// It also rejects unknown parameters. A rubric misspelled as "rubrick" that is
// silently ignored yields a run which completes, reports scores, and graded
// against nothing — the worst possible failure mode for an evaluation tool.
func (d Declaration) EffectiveRequires(params map[string]any) ([]evalspec.SessionField, error) {
	if err := d.checkParams(params); err != nil {
		return nil, err
	}

	requires := slices.Clone(d.Requires)

	// Iterate the optional table in a fixed order so the resulting list — and
	// therefore any error message quoting it — is deterministic.
	for _, name := range slices.Sorted(maps.Keys(d.Optional)) {
		on, err := boolParam(params, name)
		if err != nil {
			return nil, err
		}

		if on {
			requires = append(requires, d.Optional[name])
		}
	}

	return requires, nil
}

func (d Declaration) checkParams(params map[string]any) error {
	if len(params) == 0 {
		return nil
	}

	known := make(map[string]bool, len(d.Params))
	for _, p := range d.Params {
		known[p] = true
	}

	for _, name := range slices.Sorted(maps.Keys(params)) {
		if !known[name] {
			return fmt.Errorf("unknown parameter %q; %s accepts %v", name, d.Entry, d.Params)
		}
	}

	return nil
}

// boolParam reads an optional boolean parameter, defaulting to false.
func boolParam(params map[string]any, name string) (bool, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return false, nil
	}

	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("parameter %q must be true or false, got %T", name, v)
	}

	return b, nil
}
