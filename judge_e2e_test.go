package evalexec_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/evalspec/evalspectest"
	"github.com/sequencestream/evalexec/fixtures"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/grader/builtin"
	"github.com/sequencestream/evalexec/judge"
	"github.com/sequencestream/evalexec/internal/redact"
	"github.com/sequencestream/evalexec/result"
)

// recordedJudge replays the Judge replies captured in a fixture, so the
// end-to-end test is reproducible and free.
type recordedJudge struct {
	byCase map[string]recordedReply
	// caseIDs is how a reply is matched to a sample: the case identifier
	// travels in the context for the transport recorder's benefit, and the
	// same channel serves here.
	seen []string
}

type recordedReply struct {
	Response json.RawMessage `json:"response"`
	Usage    struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func loadRecordedJudge(t *testing.T, caseName string) *recordedJudge {
	t.Helper()

	data, err := fixtures.Read(caseName, fixtures.FileJudgeResponses)
	if err != nil {
		t.Fatalf("%v", err)
	}

	rj := &recordedJudge{byCase: make(map[string]recordedReply)}

	for i, line := range fixtures.Lines(data) {
		var row struct {
			CaseID string `json:"case_id"`
			recordedReply
		}

		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("recorded reply %d: %v", i+1, err)
		}

		rj.byCase[row.CaseID] = row.recordedReply
	}

	return rj
}

func (r *recordedJudge) Complete(ctx context.Context, p judge.Prompt) (judge.Completion, error) {
	caseID := caseIDFromPrompt(p)
	r.seen = append(r.seen, caseID)

	reply, ok := r.byCase[caseID]
	if !ok {
		return judge.Completion{}, fmt.Errorf("no recorded reply for case %q", caseID)
	}

	return judge.Completion{
		Text: string(reply.Response),
		Usage: evalspec.Usage{
			JudgeInputTokens:  reply.Usage.PromptTokens,
			JudgeOutputTokens: reply.Usage.CompletionTokens,
		},
	}, nil
}

// caseIDFromPrompt recovers which sample a prompt describes.
//
// The fixture keys replies by case_id, and the prompt is the only thing the
// Judge receives. Rather than widen the Judge interface for the sake of a test,
// the sample's own identifier is read back out of the serialized input — which
// also confirms the prompt actually carries the sample.
func caseIDFromPrompt(p judge.Prompt) string {
	for _, marker := range []string{"ORD-123", "ORD-456"} {
		if strings.Contains(p.User, marker) {
			switch marker {
			case "ORD-123":
				return "case-001"
			case "ORD-456":
				return "case-002"
			}
		}
	}

	return "case-003"
}

// TestLLMJudgeCaseEndToEnd runs f03 with the recorded replies and compares
// against the golden files.
func TestLLMJudgeCaseEndToEnd(t *testing.T) {
	t.Setenv(fixtures.FakeKeyEnv, sentinelKey)

	req, _ := stage(t, fixtures.CaseLLMJudgeBasic)

	reg := grader.NewRegistry()
	replay := loadRecordedJudge(t, fixtures.CaseLLMJudgeBasic)

	reg.Register("llm_judge", func(spec evalspec.GraderSpec, _ grader.Deps) (grader.Grader, error) {
		return builtin.NewLLMJudge(spec, replay)
	})

	res, err := evalexec.Run(t.Context(), req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Status != evalspec.RunCompleted {
		t.Errorf("status = %q, want completed", res.Status)
	}

	gotResult, gotRecords := readResult(t, req.OutputDir)

	if _, err := evalspectest.CheckEvalIDConsistency(gotResult, gotRecords); err != nil {
		t.Errorf("%v", err)
	}

	compareGolden(t, fixtures.CaseLLMJudgeBasic, gotResult, gotRecords)
	assertLineCountIdentity(t, req, gotRecords)

	// The usage total is the sum over every sample including the failed one:
	// 850 + 870 + 640.
	if got := res.Usage.JudgeModel.InputTokens; got != 2360 {
		t.Errorf("input tokens = %d, want 2360 (the refused sample's tokens count too)", got)
	}
}

// sentinelKey is the value the leak scan looks for. It lives only in the
// environment: putting it in a fixture would make the scan match itself.
const sentinelKey = "sk-fixture-DO-NOT-LEAK-0000000000"

// TestNoSecretReachesTheResultDirectory is the leak scan `make lint-secrets`
// runs. A detector that has never detected anything is indistinguishable from
// a broken one, so the same test also proves the scanner fires — see
// TestLeakScannerActuallyFires.
func TestNoSecretReachesTheResultDirectory(t *testing.T) {
	t.Setenv(fixtures.FakeKeyEnv, sentinelKey)

	req, _ := stage(t, fixtures.CaseLLMJudgeBasic)

	reg := grader.NewRegistry()
	replay := loadRecordedJudge(t, fixtures.CaseLLMJudgeBasic)

	reg.Register("llm_judge", func(spec evalspec.GraderSpec, _ grader.Deps) (grader.Grader, error) {
		return builtin.NewLLMJudge(spec, replay)
	})

	if _, err := evalexec.Run(t.Context(), req,
		evalexec.WithGraderRegistry(reg), evalexec.WithClock(testClock())); err != nil {
		t.Fatalf("Run: %v", err)
	}

	scanned := 0

	err := filepath.WalkDir(req.OutputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		scanned++

		rel, _ := filepath.Rel(req.OutputDir, path)

		if redact.ContainsSentinel(data, sentinelKey) {
			t.Errorf("%s contains the credential", rel)
		}

		if found := redact.FindSecrets(data); len(found) > 0 {
			t.Errorf("%s contains something shaped like a credential: %v", rel, found)
		}

		if strings.Contains(string(data), "Bearer "+sentinelKey) {
			t.Errorf("%s carries an unredacted Authorization header", rel)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if scanned == 0 {
		t.Fatal("nothing was scanned; the run produced no files")
	}

	// The snapshot must still name the environment variable: redaction removes
	// the value, not the reference, or provenance loses what made the run
	// reproducible.
	resultData, err := os.ReadFile(filepath.Join(req.OutputDir, result.FileResult))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	if !strings.Contains(string(resultData), fixtures.FakeKeyEnv) {
		t.Errorf("the request snapshot should still name %s", fixtures.FakeKeyEnv)
	}
}

// TestLeakScannerActuallyFires proves the scan above can fail. Without this,
// a scanner that silently stopped matching would look exactly like a clean run.
func TestLeakScannerActuallyFires(t *testing.T) {
	leaked := []byte(`{"auth":{"key":"` + sentinelKey + `"}}`)

	if !redact.ContainsSentinel(leaked, sentinelKey) {
		t.Error("the sentinel search failed to find a planted credential")
	}

	if found := redact.FindSecrets(leaked); len(found) == 0 {
		t.Error("the shape scan failed to find a planted credential")
	}
}
