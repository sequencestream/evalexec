//go:build e2e

// This file runs a full evaluation against a live Judge. It is excluded from
// `make test` because it needs credentials and costs money; `make test-e2e`
// runs it, and it skips itself when the environment is not configured.
package evalexec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/redact"
	"github.com/sequencestream/evalexec/result"
)

const liveKeyEnv = "OPENAI_API_KEY"

// TestLLMJudgeAgainstALiveModel is the end-to-end proof that the whole chain
// works against a real endpoint: configuration to provider to prompt to
// structured reply to record to summary.
//
// The dataset is chosen so the right answers are unambiguous, because the point
// is to exercise the plumbing, not to measure the model. It does include one
// sample with no evidence at all, to see whether a real Judge takes the refusal
// path rather than inventing a score — the behaviour the prompt asks for and
// the one that matters most.
func TestLLMJudgeAgainstALiveModel(t *testing.T) {
	baseURL, apiKey, model := os.Getenv("OPENAI_BASE_URL"), os.Getenv(liveKeyEnv), os.Getenv("OPENAI_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("set OPENAI_BASE_URL, OPENAI_API_KEY and OPENAI_MODEL to run the live Judge test")
	}

	root := t.TempDir()

	rows := `{"case_id":"faithful","input":{"messages":[{"role":"user","content":"查询订单 ORD-123 的状态"}]},"output":{"messages":[{"role":"assistant","content":"订单正在配送中，预计 2026-07-28 送达。"}]},"trajectory":[{"sequence":1,"type":"tool","name":"query_order","result":{"status":"shipping","eta":"2026-07-28"}}]}
{"case_id":"unfaithful","input":{"messages":[{"role":"user","content":"查询订单 ORD-456 的状态"}]},"output":{"messages":[{"role":"assistant","content":"订单已经签收了。"}]},"trajectory":[{"sequence":1,"type":"tool","name":"query_order","result":{"status":"shipping","eta":"2026-07-30"}}]}
{"case_id":"no-evidence","input":{"messages":[{"role":"user","content":"我的包裹呢"}]},"output":{"messages":[{"role":"assistant","content":"请提供订单号。"}]},"trajectory":[]}
`

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(rows), 0o644); err != nil {
		t.Fatalf("write dataset: %v", err)
	}

	req := &evalspec.EvalRequest{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      "e2e-live-judge",
		TaskID:      "order-agent-faithfulness",
		Dataset:     evalspec.Dataset{Path: datasetPath},
		Grader: evalspec.GraderSpec{
			ID: "answer-faithfulness", Version: "v1",
			Protocol: evalspec.GraderBuiltin, Entry: "llm_judge",
			Requires: []evalspec.SessionField{
				evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldTrajectory,
			},
			RequiresJudge: true,
			Parameters: map[string]any{
				"rubric": "判断最终回答是否忠实于轨迹中工具返回的事实。" +
					"若回答陈述了轨迹中不存在的事实，即为不忠实。" +
					"若轨迹为空、没有任何可核对的事实，则证据不足，不要猜测分数。",
				"min_score":      float64(0),
				"max_score":      float64(1),
				"use_trajectory": true,
			},
			TimeoutMS: 60000,
		},
		JudgeModel: &evalspec.JudgeModelSpec{
			Protocol:   evalspec.JudgeOpenAIChat,
			Endpoint:   baseURL + "/v1",
			Auth:       evalspec.Auth{Type: evalspec.AuthBearerEnv, Env: liveKeyEnv},
			Parameters: map[string]any{"model": model, "temperature": float64(0)},
			TimeoutMS:  60000,
		},
		Execution: &evalspec.Execution{Concurrency: 1},
		OutputDir: filepath.Join(root, "out"),
	}

	res, err := evalexec.Run(t.Context(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCompleted {
		t.Fatalf("status = %q, want completed", res.Status)
	}

	if res.Counts.Total != 3 || res.Counts.Completed != 3 {
		t.Errorf("counts = %+v, want 3 completed", res.Counts)
	}

	// Every sample must have reached a verdict of some kind — either a score or
	// an explicit refusal. A protocol error would mean the model could not be
	// made to answer in the agreed shape, which is the one thing a live run
	// tests that a replay cannot.
	if got := res.Evaluation.FailByCode[evalspec.CodeProtocolError]; got != 0 {
		t.Errorf("%d samples failed with protocol_error; the reply contract is not holding", got)
	}

	// The unambiguous samples must be judged, not refused: the faithful answer
	// restates the tool's own date and the unfaithful one contradicts its
	// status, so a Judge that declines either has not been given what it needs.
	if res.Evaluation.Success < 2 {
		t.Errorf("success = %d, want at least 2: the two unambiguous samples should both be judged",
			res.Evaluation.Success)
	}

	// The live model actually spent tokens, and the summary knows it.
	if res.Usage.JudgeModel.InputTokens == 0 {
		t.Error("no input tokens were recorded")
	}

	t.Logf("status=%s success=%d fail=%d fail_by_code=%v score=%+v",
		res.Status, res.Evaluation.Success, res.Evaluation.Fail,
		res.Evaluation.FailByCode, res.Evaluation.Score)
	t.Logf("usage: input=%d output=%d reasoning=%d cache_read=%d",
		res.Usage.JudgeModel.InputTokens, res.Usage.JudgeModel.OutputTokens,
		res.Usage.JudgeModel.ReasoningTokens, res.Usage.JudgeModel.CacheReadTokens)

	// Report what the Judge concluded per sample, so a run that plumbs
	// correctly but judges oddly is visible rather than hidden behind a pass.
	_, records := readResult(t, req.OutputDir)
	for _, rec := range records {
		eval, _ := rec["evaluation"].(map[string]any)
		t.Logf("  %-12s status=%v score=%v label=%v reason=%v error=%v",
			rec["case_id"], eval["status"], eval["score"], eval["label"], eval["reason"], eval["error"])
	}

	assertLineCountIdentity(t, req, records)
	assertNoCredentialInOutput(t, req.OutputDir, apiKey)
}

// assertNoCredentialInOutput is the leak check against a real credential,
// which is the only version of it that fully counts.
func assertNoCredentialInOutput(t *testing.T, dir, apiKey string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}

		if redact.ContainsSentinel(data, apiKey) {
			t.Errorf("%s contains the live API key", e.Name())
		}
	}

	// And the snapshot still records which variable was used.
	data, err := os.ReadFile(filepath.Join(dir, result.FileResult))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	if !strings.Contains(string(data), liveKeyEnv) {
		t.Errorf("the request snapshot should name %s", liveKeyEnv)
	}
}
