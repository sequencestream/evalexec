package evalspectest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Placeholders substituted for values that legitimately differ between runs.
const (
	PlaceholderEvalID = "<eval-id>"
	// PlaceholderEvalRequestSHA256 stands in for eval_request_sha256.
	//
	// Unlike dataset_sha256, which is taken over the raw dataset bytes and is
	// therefore the same everywhere, this hash covers the normalized request —
	// and normalization makes the dataset and output paths absolute. Two
	// machines running the same evaluation from different directories produce
	// the same result and two different request digests, by design.
	//
	// So no shared fixture can pin a value here. That is not a gap: the digest
	// is still reproducible for a given request, which is what traceability
	// needs, and the redact package's own tests assert exactly that.
	PlaceholderEvalRequestSHA256 = "<eval-request-sha256>"
	PlaceholderTimestamp         = "<ts>"
	PlaceholderVersion           = "<version>"
)

// volatileTimestampKeys are replaced with PlaceholderTimestamp wherever they
// appear. A null value is left null: the difference between "this ran at some
// time" and "this never ran" is exactly what distinguishes a completed record
// from a skipped one, and must survive normalization.
var volatileTimestampKeys = []string{"started_at", "finished_at"}

// volatileDurationKeys are replaced with 0. As above, null stays null.
var volatileDurationKeys = []string{"duration_ms", "latency_ms"}

// Normalize replaces run-specific values in a decoded result or record with
// stable placeholders, returning a new value and leaving v untouched.
//
// It does not touch dataset_sha256 or eval_request_sha256: those must be
// reproducible, so normalizing them would hide a real regression.
func Normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))

		for k, val := range t {
			switch {
			case k == "eval_id":
				out[k] = PlaceholderEvalID
			case k == "eval_request_sha256":
				out[k] = PlaceholderEvalRequestSHA256
			case slices.Contains(volatileTimestampKeys, k):
				out[k] = replaceIfPresent(val, PlaceholderTimestamp)
			case slices.Contains(volatileDurationKeys, k):
				out[k] = replaceIfPresent(val, float64(0))
			case k == "version" && looksLikeImplementation(t):
				out[k] = PlaceholderVersion
			default:
				out[k] = Normalize(val)
			}
		}

		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = Normalize(val)
		}

		return out
	default:
		return v
	}
}

// replaceIfPresent substitutes a placeholder for a real value but preserves
// an explicit null.
func replaceIfPresent(val, placeholder any) any {
	if val == nil {
		return nil
	}

	return placeholder
}

// looksLikeImplementation reports whether the enclosing object is the
// provenance implementation block, so that only the build version is
// normalized and not, say, a Grader version — which is a meaningful value.
func looksLikeImplementation(obj map[string]any) bool {
	_, hasName := obj["name"]
	_, hasVersion := obj["version"]

	return hasName && hasVersion && len(obj) == 2
}

// NormalizeJSON decodes JSON, normalizes it and returns the result.
func NormalizeJSON(data []byte) (any, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("evalspectest: decode: %w", err)
	}

	return Normalize(v), nil
}

// Diff returns a human-readable description of every difference between two
// normalized values, or an empty slice when they match. Paths are reported in
// dotted JSON form so a failure names a field rather than a byte offset.
func Diff(want, got any) []string {
	var diffs []string

	diffValue("$", want, got, &diffs)
	sort.Strings(diffs)

	return diffs
}

func diffValue(path string, want, got any, diffs *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: want object, got %s", path, typeName(got)))

			return
		}

		diffObject(path, w, g, diffs)
	case []any:
		g, ok := got.([]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: want array, got %s", path, typeName(got)))

			return
		}

		if len(w) != len(g) {
			*diffs = append(*diffs, fmt.Sprintf("%s: want %d elements, got %d", path, len(w), len(g)))

			return
		}

		for i := range w {
			diffValue(fmt.Sprintf("%s[%d]", path, i), w[i], g[i], diffs)
		}
	default:
		if !reflect.DeepEqual(want, got) {
			*diffs = append(*diffs, fmt.Sprintf("%s: want %s, got %s", path, render(want), render(got)))
		}
	}
}

func diffObject(path string, want, got map[string]any, diffs *[]string) {
	for _, k := range sortedKeys(want) {
		child := path + "." + k

		gv, ok := got[k]
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: missing (want %s)", child, render(want[k])))

			continue
		}

		diffValue(child, want[k], gv, diffs)
	}

	for _, k := range sortedKeys(got) {
		if _, ok := want[k]; !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s.%s: unexpected (got %s)", path, k, render(got[k])))
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func render(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}

	return string(b)
}

// CheckEvalIDConsistency verifies that the result and every record agree on
// one non-empty eval_id, and returns it.
//
// This runs before normalization erases the value. Acceptance criterion 9
// requires every sample's record to carry the run's eval_id, so a mismatch
// here is a genuine defect that normalizing would otherwise conceal.
func CheckEvalIDConsistency(result map[string]any, records []map[string]any) (string, error) {
	id, _ := result["eval_id"].(string)
	if id == "" {
		return "", fmt.Errorf("evalspectest: result eval_id is empty")
	}

	var mismatched []string

	for i, rec := range records {
		got, _ := rec["eval_id"].(string)
		if got != id {
			mismatched = append(mismatched, fmt.Sprintf("record[%d] has %q", i, got))
		}
	}

	if len(mismatched) > 0 {
		return "", fmt.Errorf("evalspectest: eval_id %q not carried by every record: %s",
			id, strings.Join(mismatched, ", "))
	}

	return id, nil
}
