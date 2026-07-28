//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/fixtures"
	"github.com/sequencestream/evalexec/internal/result"
)

// Environment variables that configure a live run. They mirror the names the
// OpenAI ecosystem already uses, so an existing shell profile works unchanged.
const (
	envBaseURL = "OPENAI_BASE_URL"
	envAPIKey  = "OPENAI_API_KEY"
	envModel   = "OPENAI_MODEL"
)

// endpoint holds the resolved live configuration.
type endpoint struct {
	baseURL string
	apiKey  string
	model   string
}

// liveEndpoint reads the environment, skipping the test when it is incomplete.
//
// Skipping rather than failing is deliberate: a machine without credentials
// should report "not configured", not something that reads like a defect.
func liveEndpoint(t *testing.T) endpoint {
	t.Helper()

	e := endpoint{
		baseURL: os.Getenv(envBaseURL),
		apiKey:  os.Getenv(envAPIKey),
		model:   os.Getenv(envModel),
	}

	if e.baseURL == "" || e.apiKey == "" || e.model == "" {
		t.Skipf("set %s, %s and %s to run the live tests", envBaseURL, envAPIKey, envModel)
	}

	return e
}

// There is deliberately no injected clock here. These tests compare nothing
// against a golden file — a live model's answers are not reproducible — so
// pinning the timestamps would hide the real durations without buying anything.

// writeDataset writes rows into a temporary directory.
func writeDataset(t *testing.T, dir, rows string) string {
	t.Helper()

	path := filepath.Join(dir, "dataset.jsonl")
	if err := os.WriteFile(path, []byte(rows), 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	return path
}

// readRecords loads a published records.jsonl.
func readRecords(t *testing.T, outDir string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(outDir, result.FileRecords))
	if err != nil {
		t.Fatalf("read records: %v", err)
	}

	var out []map[string]any

	for i, line := range fixtures.Lines(data) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse record %d: %v", i+1, err)
		}

		out = append(out, rec)
	}

	return out
}

// assertLineCountIdentity is the assertion every run shares: one record per
// dataset row, with the sequences covering 1..n exactly once.
func assertLineCountIdentity(t *testing.T, datasetPath string, records []map[string]any) {
	t.Helper()

	data, err := os.ReadFile(datasetPath)
	if err != nil {
		t.Fatalf("read dataset: %v", err)
	}

	rows := len(fixtures.Lines(data))
	if len(records) != rows {
		t.Fatalf("records.jsonl has %d lines, the dataset has %d rows", len(records), rows)
	}

	seen := make(map[int]bool, rows)

	for i, rec := range records {
		seq, ok := rec["sequence"].(float64)
		if !ok {
			t.Fatalf("record %d has no sequence", i+1)
		}

		n := int(seq)
		if seen[n] {
			t.Errorf("sequence %d appears more than once", n)
		}

		seen[n] = true
	}

	if len(seen) != rows {
		t.Errorf("sequences cover %d of %d rows", len(seen), rows)
	}
}

// logVerdicts reports what the Judge concluded per sample.
//
// A live run that plumbs correctly but judges oddly should be visible rather
// than hidden behind a pass: the model is not under test, but its answers are
// the only way to tell a working chain from a lucky one.
func logVerdicts(t *testing.T, records []map[string]any) {
	t.Helper()

	for _, rec := range records {
		eval, ok := rec["evaluation"].(map[string]any)
		if !ok {
			t.Logf("  %-12v status=%v (no evaluation)", rec["case_id"], rec["status"])

			continue
		}

		t.Logf("  %-12v status=%v score=%v label=%v", rec["case_id"],
			eval["status"], eval["score"], eval["label"])
		t.Logf("               reason=%v", eval["reason"])

		if e := eval["error"]; e != nil {
			t.Logf("               error=%v", e)
		}
	}
}

// summarize logs the run totals.
func summarize(t *testing.T, res *evalspec.EvalResult) {
	t.Helper()

	t.Logf("status=%s completed=%d skipped=%d success=%d fail=%d fail_by_code=%v",
		res.Status, res.Counts.Completed, res.Counts.Skipped,
		res.Evaluation.Success, res.Evaluation.Fail, res.Evaluation.FailByCode)

	if s := res.Evaluation.Score; s.Count > 0 {
		t.Logf("score: count=%d mean=%.4f min=%.4f max=%.4f", s.Count, *s.Mean, *s.Min, *s.Max)
	} else {
		t.Logf("score: nothing scored, statistics are null")
	}

	u := res.Usage.JudgeModel
	t.Logf("usage: input=%d output=%d reasoning=%d cache_read=%d",
		u.InputTokens, u.OutputTokens, u.ReasoningTokens, u.CacheReadTokens)
}
