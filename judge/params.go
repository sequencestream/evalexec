package judge

import (
	"fmt"
	"math"
	"os"
)

// getenv is a variable so tests can supply an environment without mutating the
// process's.
var getenv = os.Getenv

// The parameter readers below all treat an absent or null value as "not set",
// returning a nil pointer so the field is omitted from the wire request
// entirely. That distinction matters for temperature in particular: 0 is a
// meaningful value and must not be confused with "unspecified".

func stringOf(params map[string]any, name string) (string, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return "", nil
	}

	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("judge: parameter %q must be a string, got %T", name, v)
	}

	return s, nil
}

func floatOf(params map[string]any, name string) (*float64, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return nil, nil
	}

	f, ok := asFloat(v)
	if !ok {
		return nil, fmt.Errorf("judge: parameter %q must be a number, got %T", name, v)
	}

	return &f, nil
}

func intOf(params map[string]any, name string) (*int, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return nil, nil
	}

	f, ok := asFloat(v)
	if !ok {
		return nil, fmt.Errorf("judge: parameter %q must be a number, got %T", name, v)
	}

	// JSON has no integers, so a token limit arrives as a float64. Rejecting a
	// fractional one is better than truncating: "max_tokens": 100.5 is a
	// mistake, and silently making it 100 hides it.
	if f != math.Trunc(f) {
		return nil, fmt.Errorf("judge: parameter %q must be a whole number, got %v", name, f)
	}

	n := int(f)

	return &n, nil
}

func boolOf(params map[string]any, name string) (*bool, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return nil, nil
	}

	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("judge: parameter %q must be true or false, got %T", name, v)
	}

	return &b, nil
}

func stringsOf(params map[string]any, name string) ([]string, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return nil, nil
	}

	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []string:
		return t, nil
	case []any:
		out := make([]string, 0, len(t))

		for i, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("judge: parameter %q element %d must be a string, got %T", name, i, e)
			}

			out = append(out, s)
		}

		return out, nil
	default:
		return nil, fmt.Errorf("judge: parameter %q must be a string or a list of strings, got %T", name, v)
	}
}

// asFloat accepts the numeric types a decoded JSON document or a Go caller may
// produce.
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}
