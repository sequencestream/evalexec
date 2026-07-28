package evalexec_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/fixtures"
)

// Interoperability is the one place the "protocol over SDK" boundary can be
// demonstrated rather than merely stated. Everything up to here has been an
// in-process call; these tests run the same fixtures through a remote service
// and through a subprocess, and require the results to agree.

// verdict is the part of an evaluation that has to match across transports.
//
// reason and evidence are excluded on purpose: different wording is not
// incompatibility, and requiring identical prose would test the phrasing rather
// than the protocol.
type verdictSummary struct {
	caseID string
	status string
	score  any
	label  any
	code   string
}

func verdictsOf(t *testing.T, records []map[string]any) []verdictSummary {
	t.Helper()

	sortBySequence(records)

	out := make([]verdictSummary, 0, len(records))

	for _, rec := range records {
		v := verdictSummary{caseID: fmt.Sprint(rec["case_id"]), status: fmt.Sprint(rec["status"])}

		if eval, ok := rec["evaluation"].(map[string]any); ok {
			v.status = fmt.Sprint(eval["status"])
			v.score = eval["score"]
			v.label = eval["label"]

			if e, ok := eval["error"].(map[string]any); ok {
				v.code = fmt.Sprint(e["code"])
			}
		}

		out = append(out, v)
	}

	return out
}

func assertSameVerdicts(t *testing.T, wantName string, want []verdictSummary, gotName string, got []verdictSummary) {
	t.Helper()

	if len(want) != len(got) {
		t.Fatalf("%s produced %d records, %s produced %d", wantName, len(want), gotName, len(got))
	}

	for i := range want {
		if want[i] != got[i] {
			t.Errorf("record %d (%s) differs:\n  %s: %+v\n  %s: %+v",
				i+1, want[i].caseID, wantName, want[i], gotName, got[i])
		}
	}
}

// referenceGraderServer starts an http-json Grader in-process.
//
// It applies the same rule as the built-in exact_match Grader and as the stdio
// subprocess under testdata/, so a verdict difference points at the transport.
func referenceGraderServer(t *testing.T, status int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"the grader is unwell"}`))

			return
		}

		var call evalspec.GradeCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, "cannot decode", http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(referenceGrade(call))
	}))

	t.Cleanup(srv.Close)

	return srv
}

// referenceGrade mirrors the rule the other implementations apply.
func referenceGrade(call evalspec.GradeCall) map[string]any {
	expected, found := expectedOutputOf(call.Reference)
	if !found {
		return map[string]any{
			"status": "fail", "score": nil, "label": nil,
			"reason":   "there is no expected output to compare against",
			"evidence": []any{},
			"usage":    map[string]any{"judge_input_tokens": 0, "judge_output_tokens": 0},
			"error": map[string]any{
				"code": "insufficient_evidence", "message": "reference.expected_output is absent",
			},
		}
	}

	var actual any
	if len(call.Output) > 0 {
		_ = json.Unmarshal(call.Output, &actual)
	}

	matched := fmt.Sprint(actual) == fmt.Sprint(expected)

	score, label := 0.0, "mismatch"
	if matched {
		score, label = 1.0, "match"
	}

	return map[string]any{
		"status": "success", "score": score, "label": label,
		"reason": "compared output with reference.expected_output: " + label,
		"evidence": []any{
			map[string]any{"source": "output", "path": "$", "value": actual},
			map[string]any{"source": "reference", "path": "$.expected_output", "value": expected},
		},
		"usage": map[string]any{"judge_input_tokens": 0, "judge_output_tokens": 0},
		"error": nil,
	}
}

func expectedOutputOf(reference json.RawMessage) (any, bool) {
	if len(reference) == 0 {
		return nil, false
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(reference, &obj); err != nil {
		return nil, false
	}

	raw, ok := obj["expected_output"]
	if !ok {
		return nil, false
	}

	var v any

	_ = json.Unmarshal(raw, &v)

	return v, true
}

// buildHelper compiles one of the stdio test programs under testdata/.
var (
	helperOnce  sync.Map
	helperMutex sync.Mutex
)

func buildHelper(t *testing.T, pkg string) string {
	t.Helper()

	helperMutex.Lock()
	defer helperMutex.Unlock()

	if cached, ok := helperOnce.Load(pkg); ok {
		return cached.(string)
	}

	dir, err := os.MkdirTemp("", "evalexec-stdio-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}

	bin := filepath.Join(dir, filepath.Base(pkg))

	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./"+pkg)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}

	helperOnce.Store(pkg, bin)

	return bin
}

// TestBuiltinAndExternalGradersAgree is the cross-transport check: the same
// fixture graded through the built-in Grader, a remote service and a subprocess
// must reach the same verdicts.
func TestBuiltinAndExternalGradersAgree(t *testing.T) {
	for _, caseName := range []string{fixtures.CaseExactMatchAllPass, fixtures.CaseMixedSuccessFail} {
		t.Run(caseName, func(t *testing.T) {
			builtin := runWithGrader(t, caseName, func(req *evalspec.EvalRequest) {})

			srv := referenceGraderServer(t, http.StatusOK)

			overHTTP := runWithGrader(t, caseName, func(req *evalspec.EvalRequest) {
				req.Grader.Protocol = evalspec.GraderHTTPJSON
				req.Grader.Entry = srv.URL + "/grade"
			})

			assertSameVerdicts(t, "builtin", builtin, "http-json", overHTTP)

			stdioBinary := buildHelper(t, "testdata/graderstdio")

			overStdio := runWithGrader(t, caseName, func(req *evalspec.EvalRequest) {
				req.Grader.Protocol = evalspec.GraderStdioJSONL
				req.Grader.Entry = stdioBinary
			})

			assertSameVerdicts(t, "builtin", builtin, "stdio-jsonl", overStdio)
		})
	}
}

// runWithGrader stages a fixture, applies a Grader override and returns the
// verdicts.
func runWithGrader(t *testing.T, caseName string, override func(*evalspec.EvalRequest)) []verdictSummary {
	t.Helper()

	req, _ := stage(t, caseName)
	override(req)

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCompleted {
		t.Fatalf("status = %q, want completed", res.Status)
	}

	_, records := readResult(t, req.OutputDir)
	assertLineCountIdentity(t, req, records)

	return verdictsOf(t, records)
}

// TestExternalGraderStillFacesThePreChecks covers the rule that an external
// Grader does not escape validation. Its declaration comes from its
// configuration, which is exactly what makes this possible: asking the process
// would mean contacting the thing the pre-check exists to validate first.
func TestExternalGraderStillFacesThePreChecks(t *testing.T) {
	root := t.TempDir()

	// The second row lacks the declared output field.
	rows := `{"case_id":"c1","input":{},"output":{"a":1},"reference":{"expected_output":{"a":1}}}
{"case_id":"c2","input":{},"reference":{"expected_output":{"a":2}}}
`

	datasetPath := filepath.Join(root, "dataset.jsonl")
	if err := os.WriteFile(datasetPath, []byte(rows), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	srv := referenceGraderServer(t, http.StatusOK)

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Protocol = evalspec.GraderHTTPJSON
	req.Grader.Entry = srv.URL + "/grade"

	_, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err == nil {
		t.Fatal("an external Grader must not lose the requires pre-check")
	}

	if !strings.Contains(err.Error(), "output") {
		t.Errorf("error = %q, want it to name the missing field", err)
	}

	// And nothing was written.
	if _, statErr := os.Stat(req.OutputDir); !os.IsNotExist(statErr) {
		t.Errorf("a rejected run created its output directory (stat err = %v)", statErr)
	}
}

// TestExternalGraderFailureModes covers what happens when the far side
// misbehaves. Every case is a protocol_error rather than a repaired result: an
// implementation whose output is silently corrected never learns it is wrong.
func TestExternalGraderFailureModes(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    evalspec.ErrorCode
	}{
		{
			name: "non-2xx status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"broken"}`))
			},
			want: evalspec.CodeProtocolError,
		},
		{
			name: "response is not an evaluation",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"nonsense": true}`))
			},
			want: evalspec.CodeProtocolError,
		},
		{
			name: "response is not JSON at all",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`I am a teapot`))
			},
			want: evalspec.CodeProtocolError,
		},
		{
			name: "failure carrying a score",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// The invariant an external implementation is most likely to
				// get wrong: a failure is not a zero.
				_, _ = w.Write([]byte(
					`{"status":"fail","score":0,"label":null,"evidence":[],` +
						`"usage":{"judge_input_tokens":0,"judge_output_tokens":0},` +
						`"error":{"code":"insufficient_evidence"}}`))
			},
			want: evalspec.CodeProtocolError,
		},
		{
			name: "declared failure is adopted verbatim",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// A well-formed failure is the Grader's prerogative, not an
				// error on its part.
				_, _ = w.Write([]byte(
					`{"status":"fail","score":null,"label":null,"evidence":[],` +
						`"usage":{"judge_input_tokens":0,"judge_output_tokens":0},` +
						`"error":{"code":"insufficient_evidence","message":"nothing to go on"}}`))
			},
			want: evalspec.CodeInsufficientEvidence,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()

			datasetPath := filepath.Join(root, "dataset.jsonl")
			if err := os.WriteFile(datasetPath, []byte(oneMatchingRow), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
			req.Grader.Protocol = evalspec.GraderHTTPJSON
			req.Grader.Entry = srv.URL

			res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if got := res.Evaluation.FailByCode[tt.want]; got != 1 {
				t.Errorf("fail_by_code = %v, want one %q", res.Evaluation.FailByCode, tt.want)
			}

			// Whatever went wrong, the sample was still processed and the line
			// count still holds.
			if res.Counts.Completed != 1 {
				t.Errorf("completed = %d, want 1", res.Counts.Completed)
			}
		})
	}
}

// TestStdioGraderSurvivesChattyStderr covers the deadlock this protocol invites:
// a process that fills the stderr pipe buffer blocks on the write while the host
// waits on stdout, and nothing moves again.
func TestStdioGraderSurvivesChattyStderr(t *testing.T) {
	root := t.TempDir()

	script := filepath.Join(root, "chatty.sh")

	// A megabyte of stderr per sample, far past any pipe buffer.
	body := `#!/bin/sh
while IFS= read -r line; do
  i=0
  while [ $i -lt 2000 ]; do
    echo "diagnostic line $i for good measure and then some more text to fill the buffer" >&2
    i=$((i + 1))
  done
  printf '{"status":"success","score":1,"label":"ok","evidence":[],"usage":{"judge_input_tokens":0,"judge_output_tokens":0},"error":null}\n'
done
`

	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	datasetPath := writeMatchingDataset(t, root, 3)

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Protocol = evalspec.GraderStdioJSONL
	req.Grader.Entry = script

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Counts.Completed != 3 {
		t.Errorf("completed = %d, want 3: a chatty subprocess must not deadlock the host", res.Counts.Completed)
	}
}

// TestStdioGraderCrashIsAProtocolError checks that a process which dies mid-run
// costs its samples and nothing more.
func TestStdioGraderCrashIsAProtocolError(t *testing.T) {
	root := t.TempDir()

	script := filepath.Join(root, "crash.sh")

	body := `#!/bin/sh
read -r line
echo "about to die" >&2
exit 1
`

	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	datasetPath := writeMatchingDataset(t, root, 2)

	req := exactMatchRequest(datasetPath, filepath.Join(root, "out"))
	req.Grader.Protocol = evalspec.GraderStdioJSONL
	req.Grader.Entry = script

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Counts.Total != 2 || res.Counts.Completed != 2 {
		t.Errorf("counts = %+v, want both samples processed", res.Counts)
	}

	if got := res.Evaluation.FailByCode[evalspec.CodeProtocolError]; got != 2 {
		t.Errorf("fail_by_code = %v, want two protocol_error", res.Evaluation.FailByCode)
	}
}

// TestStdioGraderIsNotExecutable checks that a misconfigured command fails the
// pre-check rather than every sample.
func TestStdioGraderIsNotExecutable(t *testing.T) {
	root := t.TempDir()

	notExecutable := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(notExecutable, []byte("not a program"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	datasetPath := writeMatchingDataset(t, root, 2)

	for _, entry := range []string{notExecutable, filepath.Join(root, "missing")} {
		req := exactMatchRequest(datasetPath, filepath.Join(root, "out-"+filepath.Base(entry)))
		req.Grader.Protocol = evalspec.GraderStdioJSONL
		req.Grader.Entry = entry

		if _, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock())); err == nil {
			t.Errorf("entry %s must be rejected before the run starts", entry)
		}
	}
}

// judgeVerdict is the Judge rule all three transports answer with, duplicated
// here so the test does not import the testdata program.
func judgeVerdict(prompt string) string {
	trajectory := promptSection(prompt, "trajectory")

	switch {
	case trajectory == "" || trajectory == "[]" || trajectory == "(absent)":
		return `{"insufficient_evidence": true, "reason": "the trajectory carries no facts"}`
	case strings.Contains(promptSection(prompt, "output"), "签收") &&
		!strings.Contains(trajectory, "delivered"):
		return `{"score": 0, "label": "unfaithful", "reason": "claims delivery the trajectory does not show"}`
	default:
		return `{"score": 1, "label": "faithful", "reason": "consistent with the trajectory"}`
	}
}

func promptSection(prompt, name string) string {
	open, closing := "<"+name+">", "</"+name+">"

	start := strings.Index(prompt, open)
	if start < 0 {
		return ""
	}

	start += len(open)

	end := strings.Index(prompt[start:], closing)
	if end < 0 {
		return ""
	}

	return strings.TrimSpace(prompt[start : start+end])
}

// TestJudgeProtocolsAgree is the other half of that check: the same
// fixture judged over the OpenAI-compatible protocol and over EvalExec's own
// http-json and stdio-jsonl protocols must reach the same verdicts.
//
// All three answer with the identical rule, so any difference is the transport's
// fault — which is exactly what is under test.
func TestJudgeProtocolsAgree(t *testing.T) {
	t.Setenv(fixtures.FakeKeyEnv, sentinelKey)

	// An OpenAI-shaped server.
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}

		_ = json.NewDecoder(r.Body).Decode(&body)

		prompt := ""

		for _, m := range body.Messages {
			if m.Role == "user" {
				prompt = m.Content
			}
		}

		verdict := judgeVerdict(prompt)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": verdict},
			}},
			"usage": map[string]any{
				"prompt_tokens": len(prompt) / 4, "completion_tokens": len(verdict) / 4,
			},
		})
	}))
	t.Cleanup(openaiSrv.Close)

	// An http-json server, EvalExec's own protocol.
	httpJSONSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}

		_ = json.NewDecoder(r.Body).Decode(&body)

		prompt := ""

		for _, m := range body.Messages {
			if m.Role == "user" {
				prompt = m.Content
			}
		}

		verdict := judgeVerdict(prompt)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": verdict,
			"usage": map[string]any{
				"input_tokens": len(prompt) / 4, "output_tokens": len(verdict) / 4,
			},
		})
	}))
	t.Cleanup(httpJSONSrv.Close)

	overOpenAI := runWithJudge(t, evalspec.JudgeOpenAIChat, openaiSrv.URL)
	overHTTPJSON := runWithJudge(t, evalspec.JudgeHTTPJSON, httpJSONSrv.URL+"/judge")

	assertSameVerdicts(t, "openai-chat", overOpenAI, "http-json", overHTTPJSON)

	stdioBinary := buildHelper(t, "testdata/judgestdio")
	overStdio := runWithJudge(t, evalspec.JudgeStdioJSONL, stdioBinary)

	assertSameVerdicts(t, "openai-chat", overOpenAI, "stdio-jsonl", overStdio)
}

// runWithJudge runs the llm_judge fixture over one Judge protocol.
func runWithJudge(t *testing.T, protocol evalspec.JudgeProtocol, endpoint string) []verdictSummary {
	t.Helper()

	req, _ := stage(t, fixtures.CaseLLMJudgeBasic)
	req.JudgeModel.Protocol = protocol
	req.JudgeModel.Endpoint = endpoint

	res, err := evalexec.Run(t.Context(), req, evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run over %s: %v", protocol, err)
	}

	if res.Status != evalspec.RunCompleted {
		t.Fatalf("status = %q over %s, want completed", res.Status, protocol)
	}

	// The usage total must be non-zero on every transport: a protocol that
	// silently drops token counts would pass a verdict comparison while making
	// the run unaccountable.
	if res.Usage.JudgeModel.InputTokens == 0 {
		t.Errorf("no input tokens were recorded over %s", protocol)
	}

	_, records := readResult(t, req.OutputDir)
	assertLineCountIdentity(t, req, records)

	return verdictsOf(t, records)
}
