//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/internal/redact"
	"github.com/sequencestream/evalexec/result"
)

// faithfulnessRows is a dataset built so the right answers are unambiguous.
//
// The point is to exercise the chain, not to measure the model, so every fact
// the Judge needs is spelled out. The dates are literal rather than relative:
// an earlier version of this dataset said "tomorrow", and the Judge correctly
// refused to grade it because it could not know today's date — a good judgement
// that told us nothing about the plumbing.
const faithfulnessRows = `{"case_id":"faithful","input":{"messages":[{"role":"user","content":"查询订单 ORD-123 的状态"}]},"output":{"messages":[{"role":"assistant","content":"订单正在配送中，预计 2026-07-28 送达。"}]},"trajectory":[{"sequence":1,"type":"tool","name":"query_order","result":{"status":"shipping","eta":"2026-07-28"}}]}
{"case_id":"unfaithful","input":{"messages":[{"role":"user","content":"查询订单 ORD-456 的状态"}]},"output":{"messages":[{"role":"assistant","content":"订单已经签收了。"}]},"trajectory":[{"sequence":1,"type":"tool","name":"query_order","result":{"status":"shipping","eta":"2026-07-30"}}]}
{"case_id":"no-evidence","input":{"messages":[{"role":"user","content":"我的包裹呢"}]},"output":{"messages":[{"role":"assistant","content":"请提供订单号。"}]},"trajectory":[]}
`

const faithfulnessRubric = "判断最终回答是否忠实于轨迹中工具返回的事实。" +
	"若回答陈述了轨迹中不存在的事实，即为不忠实。" +
	"若轨迹为空、没有任何可核对的事实，则证据不足，不要猜测分数。"

// liveJudgeRequest builds the request for a live faithfulness evaluation.
func liveJudgeRequest(e endpoint, datasetPath, outputDir string, concurrency int) *evalspec.EvalRequest {
	return &evalspec.EvalRequest{
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
				"rubric":         faithfulnessRubric,
				"min_score":      float64(0),
				"max_score":      float64(1),
				"use_trajectory": true,
			},
			TimeoutMS: 60000,
		},
		JudgeModel: &evalspec.JudgeModelSpec{
			Protocol:   evalspec.JudgeOpenAIChat,
			Endpoint:   e.baseURL + "/v1",
			Auth:       evalspec.Auth{Type: evalspec.AuthBearerEnv, Env: envAPIKey},
			Parameters: map[string]any{"model": e.model, "temperature": float64(0)},
			TimeoutMS:  60000,
		},
		Execution: &evalspec.Execution{Concurrency: concurrency},
		OutputDir: outputDir,
	}
}

// TestLLMJudgeAgainstALiveModel is the full-chain proof: configuration to
// provider to prompt to reply to record to summary, against a real endpoint.
//
// The one thing only a live run can check is whether a real model can be made
// to answer in the agreed shape. A replayed fixture answers correctly by
// construction.
func TestLLMJudgeAgainstALiveModel(t *testing.T) {
	e := liveEndpoint(t)

	root := t.TempDir()
	datasetPath := writeDataset(t, root, faithfulnessRows)

	req := liveJudgeRequest(e, datasetPath, filepath.Join(root, "out"), 1)

	res, err := evalexec.Run(t.Context(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	summarize(t, res)

	records := readRecords(t, req.OutputDir)
	logVerdicts(t, records)

	if res.Status != evalspec.RunCompleted {
		t.Fatalf("status = %q, want completed", res.Status)
	}

	if res.Counts.Total != 3 || res.Counts.Completed != 3 {
		t.Errorf("counts = %+v, want three completed", res.Counts)
	}

	// A protocol error means the model could not be made to answer in the
	// agreed shape — the failure a replay cannot surface.
	if got := res.Evaluation.FailByCode[evalspec.CodeProtocolError]; got != 0 {
		t.Errorf("%d samples failed with protocol_error; the reply contract is not holding", got)
	}

	// The two unambiguous samples must be judged rather than refused: the
	// faithful answer restates the tool's own date and the unfaithful one
	// contradicts its status, so a Judge that declines either was not given what
	// it needed.
	if res.Evaluation.Success < 2 {
		t.Errorf("success = %d, want at least 2: both unambiguous samples should be judged",
			res.Evaluation.Success)
	}

	// The sample with no trajectory has nothing to check against. A model that
	// scores it anyway has invented a number, which is worse than a recorded
	// failure — this is the behaviour the rubric and the system prompt both ask
	// for.
	if got := res.Evaluation.FailByCode[evalspec.CodeInsufficientEvidence]; got != 1 {
		t.Errorf("insufficient_evidence count = %d, want 1: the evidence-free sample should be refused", got)
	}

	if res.Usage.JudgeModel.InputTokens == 0 {
		t.Error("no input tokens were recorded")
	}

	assertLineCountIdentity(t, datasetPath, records)
	assertNoCredentialInOutput(t, req.OutputDir, e.apiKey)
	assertFailuresCarryNoScore(t, records)
}

// TestLLMJudgeUnderConcurrencyAgainstALiveModel runs the same evaluation with
// several workers, because the connection pooling and the per-call deadlines only
// really get exercised against a service that takes time to answer.
func TestLLMJudgeUnderConcurrencyAgainstALiveModel(t *testing.T) {
	e := liveEndpoint(t)

	root := t.TempDir()
	datasetPath := writeDataset(t, root, faithfulnessRows)

	req := liveJudgeRequest(e, datasetPath, filepath.Join(root, "out"), 3)
	req.EvalID = "e2e-live-judge-concurrent"

	res, err := evalexec.Run(t.Context(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	summarize(t, res)

	records := readRecords(t, req.OutputDir)

	if res.Counts.Total != 3 || res.Counts.Completed != 3 {
		t.Errorf("counts = %+v, want three completed", res.Counts)
	}

	// The identity that must survive every path, checked here against a service
	// whose reply times vary.
	assertLineCountIdentity(t, datasetPath, records)
	assertFailuresCarryNoScore(t, records)
	assertNoCredentialInOutput(t, req.OutputDir, e.apiKey)
}

// assertFailuresCarryNoScore is the rule the whole status model rests on,
// checked against whatever a real model actually returned.
func assertFailuresCarryNoScore(t *testing.T, records []map[string]any) {
	t.Helper()

	for _, rec := range records {
		eval, ok := rec["evaluation"].(map[string]any)
		if !ok {
			continue
		}

		if eval["status"] == "fail" && eval["score"] != nil {
			t.Errorf("%v failed but carries score %v, want null",
				rec["case_id"], eval["score"])
		}
	}
}

// assertNoCredentialInOutput is the leak check against a real credential, which
// is the only version of it that fully counts.
func assertNoCredentialInOutput(t *testing.T, dir, apiKey string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			// logs/ is scanned too: it holds the raw request, headers included,
			// which is where a credential is most likely to escape.
			assertNoCredentialInOutput(t, filepath.Join(dir, e.Name()), apiKey)

			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}

		if redact.ContainsSentinel(data, apiKey) {
			t.Errorf("%s contains the live API key", e.Name())
		}

		if found := redact.FindSecrets(data); len(found) > 0 {
			t.Errorf("%s contains something shaped like a credential: %v", e.Name(), found)
		}
	}

	// The snapshot must still name the variable: redaction removes the value,
	// not the reference, or provenance loses what made the run reproducible.
	resultPath := filepath.Join(dir, result.FileResult)
	if data, err := os.ReadFile(resultPath); err == nil {
		if !strings.Contains(string(data), envAPIKey) {
			t.Errorf("the request snapshot should still name %s", envAPIKey)
		}
	}
}
