package redact_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/internal/redact"
)

func validRequest() *evalspec.EvalRequest {
	return &evalspec.EvalRequest{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      "eval-1",
		TaskID:      "task-1",
		Dataset:     evalspec.Dataset{Path: "/data/sessions.jsonl"},
		Grader: evalspec.GraderSpec{
			ID: "g", Version: "v1",
			Protocol: evalspec.GraderBuiltin, Entry: "llm_judge",
			Requires:      []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
			RequiresJudge: true,
			Parameters:    map[string]any{"rubric": "judge faithfulness", "max_score": 1},
		},
		JudgeModel: &evalspec.JudgeModelSpec{
			Protocol: evalspec.JudgeOpenAIChat,
			Endpoint: "https://judge.example.invalid/v1",
			Auth:     evalspec.Auth{Type: evalspec.AuthBearerEnv, Env: "JUDGE_API_KEY"},
		},
		OutputDir: "/results/out",
	}
}

// TestSnapshotKeepsTheCredentialReference checks that redaction is not
// deletion: the snapshot must still say which environment variable was used,
// or provenance loses the one detail that makes a run reproducible.
func TestSnapshotKeepsTheCredentialReference(t *testing.T) {
	snap, err := redact.Request(validRequest())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	s := string(snap.JSON)

	for _, want := range []string{`"bearer_env"`, `"JUDGE_API_KEY"`} {
		if !strings.Contains(s, want) {
			t.Errorf("snapshot lost %s:\n%s", want, s)
		}
	}
}

// TestCredentialInConfigurationIsRefused pins the choice not to silently
// strip. Quietly removing a secret would tell the user it was handled safely,
// when in fact it is still sitting in their configuration file on disk.
func TestCredentialInConfigurationIsRefused(t *testing.T) {
	leaks := []struct {
		name  string
		apply func(*evalspec.EvalRequest)
	}{
		{
			name: "openai style key in a grader parameter",
			apply: func(r *evalspec.EvalRequest) {
				r.Grader.Parameters["note"] = "use sk-abcdefghij0123456789 for now"
			},
		},
		{
			name: "bearer token in a judge parameter",
			apply: func(r *evalspec.EvalRequest) {
				r.JudgeModel.Parameters = map[string]any{"header": "Bearer abcdefghij0123456789"}
			},
		},
		{
			name: "github token in the endpoint",
			apply: func(r *evalspec.EvalRequest) {
				r.JudgeModel.Endpoint = "https://ghp_abcdefghij0123456789@judge.example.invalid"
			},
		},
		{
			name: "aws key in the task id",
			apply: func(r *evalspec.EvalRequest) {
				r.TaskID = "AKIAIOSFODNN7EXAMPLE"
			},
		},
	}

	for _, tt := range leaks {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.apply(req)

			_, err := redact.Request(req)
			if err == nil {
				t.Fatal("a credential in the configuration must be refused, not stripped")
			}

			if !strings.Contains(err.Error(), "credential") {
				t.Errorf("error should say what it found: %v", err)
			}
		})
	}
}

// TestCanonicalIsDeterministic is what makes the request digest comparable
// between runs: the same request must serialize identically no matter how the
// JSON happened to be written.
func TestCanonicalIsDeterministic(t *testing.T) {
	a := `{"b": 2, "a": 1, "c": {"z": 26, "y": 25}}`
	b := `{"c": {"y": 25, "z": 26}, "a": 1, "b": 2}`

	var va, vb any
	if err := json.Unmarshal([]byte(a), &va); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}

	if err := json.Unmarshal([]byte(b), &vb); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}

	ca, err := redact.Canonical(va)
	if err != nil {
		t.Fatalf("Canonical a: %v", err)
	}

	cb, err := redact.Canonical(vb)
	if err != nil {
		t.Fatalf("Canonical b: %v", err)
	}

	if string(ca) != string(cb) {
		t.Errorf("the same document serialized differently:\n%s\n%s", ca, cb)
	}

	if want := `{"a":1,"b":2,"c":{"y":25,"z":26}}`; string(ca) != want {
		t.Errorf("Canonical = %s, want %s (keys sorted, no whitespace)", ca, want)
	}
}

// TestCanonicalDoesNotEscapeHTML covers a real corruption risk: encoding/json
// rewrites <, > and & by default, which would both change the digest and
// mangle a rubric's text on the way back out.
func TestCanonicalDoesNotEscapeHTML(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"rubric": "score > 0.5 && answer <> null"}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got, err := redact.Canonical(v)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	if strings.Contains(string(got), `\u003e`) {
		t.Errorf("HTML escaping must be off: %s", got)
	}

	if !strings.Contains(string(got), "score > 0.5 && answer <> null") {
		t.Errorf("rubric text was altered: %s", got)
	}
}

// TestDigestIsStableAcrossRuns pins that the digest depends on the request and
// nothing else.
func TestDigestIsStableAcrossRuns(t *testing.T) {
	first, err := redact.Request(validRequest())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	second, err := redact.Request(validRequest())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if first.SHA256 != second.SHA256 {
		t.Errorf("digest is not reproducible: %s != %s", first.SHA256, second.SHA256)
	}

	if len(first.SHA256) != 64 {
		t.Errorf("digest = %q, want 64 hex characters", first.SHA256)
	}

	// A different request must produce a different digest.
	changed := validRequest()
	changed.TaskID = "task-2"

	third, err := redact.Request(changed)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if third.SHA256 == first.SHA256 {
		t.Error("a changed request must change the digest")
	}
}

func TestFindSecretsAndSentinel(t *testing.T) {
	published := []byte(`{"result": "ok", "note": "nothing to see"}`)

	if found := redact.FindSecrets(published); len(found) != 0 {
		t.Errorf("false positives on clean output: %v", found)
	}

	leaked := []byte(`{"auth": "sk-abcdefghij0123456789"}`)
	if found := redact.FindSecrets(leaked); len(found) == 0 {
		t.Error("a leaked key must be found")
	}

	// The pattern scan proves the scanner works; only an exact search proves
	// this particular secret did not get out.
	if !redact.ContainsSentinel([]byte("prefix TOP-SECRET-VALUE suffix"), "TOP-SECRET-VALUE") {
		t.Error("ContainsSentinel must find an exact match")
	}

	if redact.ContainsSentinel(published, "") {
		t.Error("an empty sentinel must never match")
	}
}

// TestScannerDoesNotFlagOrdinaryConfiguration guards against the failure mode
// that gets a leak scanner switched off.
func TestScannerDoesNotFlagOrdinaryConfiguration(t *testing.T) {
	ordinary := []string{
		`{"auth":{"type":"bearer_env","env":"JUDGE_API_KEY"}}`,
		`["--task-id","cs-regression-20260727","--grader","relevance.json"]`,
		`{"model":"deepseek-v4-flash","endpoint":"https://api.deepseek.com"}`,
		`{"rubric":"判断回答是否与参考答案语义一致"}`,
	}

	for _, s := range ordinary {
		if found := redact.FindSecrets([]byte(s)); len(found) != 0 {
			t.Errorf("false positive on %s: %v", s, found)
		}
	}
}
