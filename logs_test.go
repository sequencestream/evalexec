package evalexec_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/internal/redact"
	"github.com/sequencestream/evalexec/internal/result"
)

const logTestKeyEnv = "EVALEXEC_LOGTEST_KEY"

// judgeServer answers chat completions with a per-case reply, and counts the
// connections it was given.
type judgeServer struct {
	*httptest.Server
	openConns atomic.Int64
	maxConns  atomic.Int64
	requests  atomic.Int64
}

// newJudgeServer starts a server whose reply depends on the sample.
func newJudgeServer(t *testing.T, reply func(caseID string) string) *judgeServer {
	t.Helper()

	js := &judgeServer{}

	js.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		js.requests.Add(1)

		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}

		_ = json.NewDecoder(r.Body).Decode(&body)

		caseID := ""

		for _, m := range body.Messages {
			if i := strings.Index(m.Content, "CASE:"); i >= 0 {
				rest := m.Content[i+len("CASE:"):]
				if j := strings.IndexAny(rest, "\"\n <"); j >= 0 {
					caseID = rest[:j]
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": reply(caseID)},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))

	js.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			now := js.openConns.Add(1)
			for {
				peak := js.maxConns.Load()
				if now <= peak || js.maxConns.CompareAndSwap(peak, now) {
					break
				}
			}
		case http.StateClosed, http.StateHijacked:
			js.openConns.Add(-1)
		}
	}

	js.Start()
	t.Cleanup(js.Close)

	return js
}

// judgeDataset writes rows whose input carries a CASE: marker, so the fake
// server can tell which sample it is answering.
func judgeDataset(t *testing.T, dir string, rows int) string {
	t.Helper()

	var b strings.Builder

	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&b, `{"case_id":"c%d","input":{"marker":"CASE:c%d"},"output":{"a":%d}}`+"\n", i, i, i)
	}

	path := filepath.Join(dir, "dataset.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	return path
}

func llmJudgeRequest(datasetPath, outputDir, endpoint string, concurrency int) *evalspec.EvalRequest {
	return &evalspec.EvalRequest{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      "eval-logs",
		TaskID:      "task-logs",
		Dataset:     evalspec.Dataset{Path: datasetPath},
		Grader: evalspec.GraderSpec{
			ID: "g", Version: "v1",
			Protocol: evalspec.GraderBuiltin, Entry: "llm_judge",
			Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
			RequiresJudge: true,
			Parameters:    map[string]any{"rubric": "judge it"},
		},
		JudgeModel: &evalspec.JudgeModelSpec{
			Protocol:   evalspec.JudgeOpenAIChat,
			Endpoint:   endpoint,
			Auth:       evalspec.Auth{Type: evalspec.AuthBearerEnv, Env: logTestKeyEnv},
			Parameters: map[string]any{"model": "test-model"},
			TimeoutMS:  5000,
		},
		Execution: &evalspec.Execution{Concurrency: concurrency},
		OutputDir: outputDir,
	}
}

// TestLogsAreKeptOnlyForFailures pins the retention rule: a successful run of
// ten thousand samples must not leave ten thousand prompt echoes on disk, and a
// failing sample must leave enough to diagnose it.
func TestLogsAreKeptOnlyForFailures(t *testing.T) {
	t.Setenv(logTestKeyEnv, sentinelKey)

	root := t.TempDir()
	datasetPath := judgeDataset(t, root, 6)

	// Odd samples answer properly; even ones return prose, which is a protocol
	// error.
	srv := newJudgeServer(t, func(caseID string) string {
		if strings.HasSuffix(caseID, "2") || strings.HasSuffix(caseID, "4") {
			return "I have thoughts but no verdict."
		}

		return `{"score":1,"label":"ok","reason":"fine"}`
	})

	req := llmJudgeRequest(datasetPath, filepath.Join(root, "out"), srv.URL, 1)

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Evaluation.Fail != 2 {
		t.Fatalf("fail = %d, want 2 (fail_by_code = %v)", res.Evaluation.Fail, res.Evaluation.FailByCode)
	}

	logsDir := filepath.Join(req.OutputDir, result.DirLogs)

	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatalf("read logs dir: %v", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	if len(names) != 2 {
		t.Errorf("logs/ holds %d files (%v), want 2 — one per failed sample", len(names), names)
	}

	for _, want := range []string{"judge-c2.jsonl", "judge-c4.jsonl"} {
		if _, err := os.Stat(filepath.Join(logsDir, want)); err != nil {
			t.Errorf("%s missing: %v", want, err)
		}
	}

	// A successful sample leaves nothing.
	if _, err := os.Stat(filepath.Join(logsDir, "judge-c1.jsonl")); err == nil {
		t.Error("a successful sample must leave no log")
	}

	// The artifacts block names the directory only because it exists.
	if res.Artifacts.Logs != result.DirLogs {
		t.Errorf("artifacts.logs = %q, want %q", res.Artifacts.Logs, result.DirLogs)
	}
}

// TestLogsRedactTheCredential is the leak check extended to diagnostics, which
// are the most likely place for a credential to escape: they hold the raw
// request, headers included.
func TestLogsRedactTheCredential(t *testing.T) {
	t.Setenv(logTestKeyEnv, sentinelKey)

	root := t.TempDir()
	datasetPath := judgeDataset(t, root, 2)

	srv := newJudgeServer(t, func(string) string { return "not json at all" })

	req := llmJudgeRequest(datasetPath, filepath.Join(root, "out"), srv.URL, 1)

	if _, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock())); err != nil {
		t.Fatalf("Run: %v", err)
	}

	logsDir := filepath.Join(req.OutputDir, result.DirLogs)

	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatalf("read logs dir: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("no logs were written; there is nothing to check")
	}

	sawAuthorization := false

	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(logsDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}

		if redact.ContainsSentinel(data, sentinelKey) {
			t.Errorf("%s contains the credential", e.Name())
		}

		if found := redact.FindSecrets(data); len(found) > 0 {
			t.Errorf("%s contains something shaped like a credential: %v", e.Name(), found)
		}

		if strings.Contains(string(data), "Bearer ***") {
			sawAuthorization = true
		}
	}

	// The header must be present and replaced, not simply absent: knowing a
	// credential was sent is part of diagnosing an authentication failure.
	if !sawAuthorization {
		t.Error("no redacted Authorization header was recorded")
	}
}

// TestConnectionsAreReusedUnderConcurrency covers the transport tuning. Go's
// default MaxIdleConnsPerHost is 2, so above two concurrent samples the pool
// stops serving and every call negotiates a fresh connection — which becomes
// the dominant cost against a real endpoint.
func TestConnectionsAreReusedUnderConcurrency(t *testing.T) {
	t.Setenv(logTestKeyEnv, sentinelKey)

	const (
		rows        = 60
		concurrency = 8
	)

	root := t.TempDir()
	datasetPath := judgeDataset(t, root, rows)

	srv := newJudgeServer(t, func(string) string { return `{"score":1,"reason":"ok"}` })

	req := llmJudgeRequest(datasetPath, filepath.Join(root, "out"), srv.URL, concurrency)

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Counts.Completed != rows {
		t.Fatalf("completed = %d, want %d", res.Counts.Completed, rows)
	}

	if got := srv.requests.Load(); got != rows {
		t.Errorf("the server saw %d requests, want %d", got, rows)
	}

	// With the pool sized to the concurrency, the peak connection count should
	// not exceed it. Without the tuning this run would open dozens.
	if peak := srv.maxConns.Load(); peak > int64(concurrency) {
		t.Errorf("peak connections = %d for %d requests at concurrency %d; the pool is not being reused",
			peak, rows, concurrency)
	} else {
		t.Logf("peak connections = %d for %d requests at concurrency %d", peak, rows, concurrency)
	}
}

// TestStressAtHighConcurrency is the pressure case from the plan: enough samples
// and workers that a lock or ordering mistake has room to show up, especially
// under -race.
func TestStressAtHighConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}

	t.Setenv(logTestKeyEnv, sentinelKey)

	const (
		rows        = 1000
		concurrency = 16
	)

	root := t.TempDir()
	datasetPath := judgeDataset(t, root, rows)

	var mu sync.Mutex

	answered := make(map[string]bool, rows)

	srv := newJudgeServer(t, func(caseID string) string {
		mu.Lock()
		answered[caseID] = true
		mu.Unlock()

		return `{"score":0.5,"label":"middling","reason":"ok"}`
	})

	req := llmJudgeRequest(datasetPath, filepath.Join(root, "out"), srv.URL, concurrency)

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Counts.Total != rows || res.Counts.Completed != rows {
		t.Errorf("counts = %+v, want %d completed", res.Counts, rows)
	}

	if res.Evaluation.Success != rows {
		t.Errorf("success = %d, want %d", res.Evaluation.Success, rows)
	}

	if res.Evaluation.Score.Count != rows {
		t.Errorf("score.count = %d, want %d", res.Evaluation.Score.Count, rows)
	}

	// Every sample reached the Judge exactly once.
	mu.Lock()
	distinct := len(answered)
	mu.Unlock()

	if distinct != rows {
		t.Errorf("the Judge answered %d distinct samples, want %d", distinct, rows)
	}

	_, records := readResult(t, req.OutputDir)
	assertLineCountIdentity(t, req, records)

	// A run in which nothing failed leaves no logs directory at all, which is
	// the honest signal: an empty directory would suggest diagnostics were
	// attempted and came back blank.
	if _, err := os.Stat(filepath.Join(req.OutputDir, result.DirLogs)); err == nil {
		t.Error("a run with no failures must leave no logs directory")
	}

	if res.Artifacts.Logs != "" {
		t.Errorf("artifacts.logs = %q, want empty when no logs were written", res.Artifacts.Logs)
	}

	// And nothing leaked, at scale.
	for _, name := range []string{result.FileResult, result.FileRecords} {
		data, err := os.ReadFile(filepath.Join(req.OutputDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		if redact.ContainsSentinel(data, sentinelKey) {
			t.Errorf("%s contains the credential", name)
		}
	}
}
