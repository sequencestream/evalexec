package evalspectest_test

import (
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalspec/evalspectest"
)

func mustNormalize(t *testing.T, s string) any {
	t.Helper()

	v, err := evalspectest.NormalizeJSON([]byte(s))
	if err != nil {
		t.Fatalf("normalize %s: %v", s, err)
	}

	return v
}

// TestNormalizeErasesVolatileValues covers the whole reason this package
// exists: two runs of the same evaluation differ in eval_id, timestamps and
// measured durations, and none of those differences are defects.
func TestNormalizeErasesVolatileValues(t *testing.T) {
	a := `{
		"eval_id": "0192f0c1-aaaa-7000-8000-000000000001",
		"started_at": "2026-07-27T01:00:00Z",
		"finished_at": "2026-07-27T01:02:05Z",
		"duration_ms": 125000,
		"counts": {"total": 10, "completed": 10, "skipped": 0}
	}`
	b := `{
		"eval_id": "0192f0c1-bbbb-7000-8000-000000000002",
		"started_at": "2027-01-01T00:00:00Z",
		"finished_at": "2027-01-01T00:00:01Z",
		"duration_ms": 1,
		"counts": {"total": 10, "completed": 10, "skipped": 0}
	}`

	if diffs := evalspectest.Diff(mustNormalize(t, a), mustNormalize(t, b)); len(diffs) != 0 {
		t.Errorf("two runs of the same evaluation must normalize equal, got:\n%s", strings.Join(diffs, "\n"))
	}
}

// TestNormalizePreservesNulls checks the distinction that must survive: a
// null timestamp means the sample never ran, which is exactly what separates
// a skipped record from a completed one.
func TestNormalizePreservesNulls(t *testing.T) {
	completed := `{"status":"completed","started_at":"2026-07-27T01:00:00Z","finished_at":"2026-07-27T01:00:01Z"}`
	skipped := `{"status":"skipped","started_at":null,"finished_at":null}`

	diffs := evalspectest.Diff(mustNormalize(t, completed), mustNormalize(t, skipped))
	if len(diffs) == 0 {
		t.Error("a completed record and a skipped one must not normalize equal: null timestamps carry meaning")
	}

	// And a null must stay null rather than becoming the placeholder.
	norm, ok := mustNormalize(t, skipped).(map[string]any)
	if !ok {
		t.Fatal("normalized value is not an object")
	}

	if norm["started_at"] != nil {
		t.Errorf("started_at = %v, want nil", norm["started_at"])
	}
}

// TestNormalizeKeepsDatasetChecksum guards the one hash that is fixed by the
// specification. It is taken over the raw dataset bytes, so every
// implementation must agree on it; normalizing it away would hide a real
// regression in traceability.
func TestNormalizeKeepsDatasetChecksum(t *testing.T) {
	a := `{"provenance":{"dataset_sha256":"aaa"}}`
	b := `{"provenance":{"dataset_sha256":"bbb"}}`

	diffs := evalspectest.Diff(mustNormalize(t, a), mustNormalize(t, b))
	if len(diffs) != 1 {
		t.Fatalf("want exactly one difference, got %d:\n%s", len(diffs), strings.Join(diffs, "\n"))
	}

	if !strings.Contains(diffs[0], "dataset_sha256") {
		t.Errorf("diff = %q, want it to name dataset_sha256", diffs[0])
	}
}

// TestNormalizeErasesRequestChecksum records a permanent exemption, for a
// reason specific to what that digest covers: the normalized request carries
// absolute paths, so the same evaluation run from two different directories
// yields two different request digests. A shared fixture cannot pin one.
//
// The digest is still reproducible for a given request — see the redact
// package's tests — which is what traceability actually requires.
func TestNormalizeErasesRequestChecksum(t *testing.T) {
	a := `{"provenance":{"eval_request_sha256":"aaa"}}`
	b := `{"provenance":{"eval_request_sha256":"bbb"}}`

	if diffs := evalspectest.Diff(mustNormalize(t, a), mustNormalize(t, b)); len(diffs) != 0 {
		t.Errorf("eval_request_sha256 must be normalized away: it depends on absolute paths, got:\n%s", strings.Join(diffs, "\n"))
	}
}

// TestNormalizeImplementationVersionOnly checks that only the build version
// is erased, not a Grader version — which is a meaningful assertion.
func TestNormalizeImplementationVersionOnly(t *testing.T) {
	in := `{
		"provenance": {"implementation": {"name": "evalexec", "version": "0.1.0"}},
		"evaluation": {"grader_id": "g", "grader_version": "v1"}
	}`
	other := `{
		"provenance": {"implementation": {"name": "evalexec", "version": "9.9.9"}},
		"evaluation": {"grader_id": "g", "grader_version": "v1"}
	}`

	if diffs := evalspectest.Diff(mustNormalize(t, in), mustNormalize(t, other)); len(diffs) != 0 {
		t.Errorf("the build version must be normalized away, got:\n%s", strings.Join(diffs, "\n"))
	}

	changedGrader := `{
		"provenance": {"implementation": {"name": "evalexec", "version": "0.1.0"}},
		"evaluation": {"grader_id": "g", "grader_version": "v2"}
	}`

	diffs := evalspectest.Diff(mustNormalize(t, in), mustNormalize(t, changedGrader))
	if len(diffs) != 1 || !strings.Contains(diffs[0], "grader_version") {
		t.Errorf("a changed grader_version must be reported, got:\n%s", strings.Join(diffs, "\n"))
	}
}

// TestDiffReportsFieldPaths checks that a failure names a field, not a byte
// offset — the difference between a usable and a useless test failure.
func TestDiffReportsFieldPaths(t *testing.T) {
	tests := []struct {
		name     string
		want     string
		got      string
		wantPath string
	}{
		{
			name:     "changed scalar",
			want:     `{"counts":{"total":10}}`,
			got:      `{"counts":{"total":9}}`,
			wantPath: "$.counts.total",
		},
		{
			name:     "missing key",
			want:     `{"counts":{"total":10,"skipped":0}}`,
			got:      `{"counts":{"total":10}}`,
			wantPath: "$.counts.skipped: missing",
		},
		{
			name:     "unexpected key",
			want:     `{"counts":{"total":10}}`,
			got:      `{"counts":{"total":10,"extra":1}}`,
			wantPath: "$.counts.extra: unexpected",
		},
		{
			name:     "array length",
			want:     `{"evidence":[1,2]}`,
			got:      `{"evidence":[1]}`,
			wantPath: "$.evidence: want 2 elements, got 1",
		},
		{
			name:     "array element",
			want:     `{"evidence":[{"source":"output"}]}`,
			got:      `{"evidence":[{"source":"trajectory"}]}`,
			wantPath: "$.evidence[0].source",
		},
		{
			name:     "type mismatch",
			want:     `{"score":null}`,
			got:      `{"score":0}`,
			wantPath: "$.score",
		},
		{
			name:     "object where a scalar was expected",
			want:     `{"evaluation":null}`,
			got:      `{"evaluation":{"status":"success"}}`,
			wantPath: "$.evaluation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := evalspectest.Diff(mustNormalize(t, tt.want), mustNormalize(t, tt.got))
			if len(diffs) == 0 {
				t.Fatal("Diff found nothing, want a difference")
			}

			joined := strings.Join(diffs, "\n")
			if !strings.Contains(joined, tt.wantPath) {
				t.Errorf("diffs =\n%s\nwant one naming %q", joined, tt.wantPath)
			}
		})
	}
}

func TestDiffIdentical(t *testing.T) {
	const doc = `{"a":1,"b":[1,2,{"c":null}],"d":{"e":"f"}}`

	if diffs := evalspectest.Diff(mustNormalize(t, doc), mustNormalize(t, doc)); len(diffs) != 0 {
		t.Errorf("identical documents differ:\n%s", strings.Join(diffs, "\n"))
	}
}

// TestCheckEvalIDConsistency covers acceptance criterion 9: every record must
// carry the run's eval_id. This runs before normalization erases the value,
// because otherwise normalizing would conceal exactly this defect.
func TestCheckEvalIDConsistency(t *testing.T) {
	result := map[string]any{"eval_id": "eval-1"}

	consistent := []map[string]any{
		{"eval_id": "eval-1", "case_id": "c1"},
		{"eval_id": "eval-1", "case_id": "c2"},
	}

	id, err := evalspectest.CheckEvalIDConsistency(result, consistent)
	if err != nil {
		t.Fatalf("CheckEvalIDConsistency: %v", err)
	}

	if id != "eval-1" {
		t.Errorf("id = %q, want eval-1", id)
	}

	mismatched := []map[string]any{
		{"eval_id": "eval-1", "case_id": "c1"},
		{"eval_id": "eval-2", "case_id": "c2"},
	}

	_, err = evalspectest.CheckEvalIDConsistency(result, mismatched)
	if err == nil {
		t.Fatal("a record carrying a different eval_id must be reported")
	}

	if !strings.Contains(err.Error(), "record[1]") {
		t.Errorf("error = %q, want it to point at record[1]", err)
	}

	if _, err := evalspectest.CheckEvalIDConsistency(map[string]any{}, nil); err == nil {
		t.Error("an empty result eval_id must be reported")
	}
}

func TestNormalizeJSONRejectsGarbage(t *testing.T) {
	if _, err := evalspectest.NormalizeJSON([]byte(`{not json`)); err == nil {
		t.Error("malformed JSON must be rejected")
	}
}
