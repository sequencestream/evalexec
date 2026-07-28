package grader

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Parameter validation lives here, next to the parameter table, rather than in
// the Grader implementations that consume it.
//
// The alternative — letting each built-in package attach its validator to the
// table from an init function — would make validation depend on whether the
// implementation package happened to be imported. A pre-check that silently
// weakens when a package is not linked in is worse than no pre-check, because
// it still looks like one.

// Errors reported for missing mandatory parameters.
var (
	ErrPatternRequired = errors.New(`parameter "pattern" is required`)
	ErrSchemaRequired  = errors.New(`parameter "schema" is required`)
)

// StringParam reads a string parameter, falling back to a default.
func StringParam(params map[string]any, name, fallback string) (string, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return fallback, nil
	}

	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("parameter %q must be a string, got %T", name, v)
	}

	return s, nil
}

// BoolParam reads a boolean parameter, falling back to a default.
func BoolParam(params map[string]any, name string, fallback bool) (bool, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return fallback, nil
	}

	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("parameter %q must be true or false, got %T", name, v)
	}

	return b, nil
}

// CompilePattern builds the regular expression a regex Grader searches with,
// returning it alongside the pattern as written.
//
// Case-insensitivity is applied as an inline (?i) flag rather than by lowering
// both sides, so that the pattern keeps its own semantics — character classes
// and anchors included.
func CompilePattern(params map[string]any) (re *regexp.Regexp, source string, err error) {
	source, err = StringParam(params, "pattern", "")
	if err != nil {
		return nil, "", err
	}

	if source == "" {
		return nil, "", ErrPatternRequired
	}

	sensitive, err := BoolParam(params, "case_sensitive", false)
	if err != nil {
		return nil, "", err
	}

	expr := source
	if !sensitive {
		expr = "(?i)" + expr
	}

	re, err = regexp.Compile(expr)
	if err != nil {
		return nil, "", fmt.Errorf("parameter %q is not a valid regular expression: %w", "pattern", err)
	}

	return re, source, nil
}

// CompileSchema builds the JSON Schema a json_schema Grader validates against.
func CompileSchema(params map[string]any) (*jsonschema.Schema, error) {
	v, ok := params["schema"]
	if !ok || v == nil {
		return nil, ErrSchemaRequired
	}

	doc, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parameter %q must be a JSON Schema object, got %T", "schema", v)
	}

	c := jsonschema.NewCompiler()

	const resource = "inline://schema.json"

	if err := c.AddResource(resource, doc); err != nil {
		return nil, fmt.Errorf("parameter %q is not a usable JSON Schema: %w", "schema", err)
	}

	s, err := c.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("parameter %q is not a valid JSON Schema: %w", "schema", err)
	}

	return s, nil
}

// validateRegexParams and validateSchemaParams are the pre-check hooks wired
// into the built-in table.
func validateRegexParams(params map[string]any) error {
	_, _, err := CompilePattern(params)

	return err
}

func validateSchemaParams(params map[string]any) error {
	_, err := CompileSchema(params)

	return err
}
