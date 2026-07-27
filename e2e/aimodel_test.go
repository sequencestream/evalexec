//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/aimodel/ais"
	"github.com/vogo/aimodel/provider/openai"
)

// TestAimodelReachesTheEndpoint is the narrowest live check: the pinned aimodel
// version can talk to the configured endpoint and token usage comes back
// populated.
//
// It sits below the full evaluation tests on purpose. When a live run fails,
// this says whether the problem is the endpoint or everything above it — and
// EvalExec's usage summary is built entirely from these counters, so a provider
// that answers without them would produce a result that cannot be reconciled
// with a bill.
func TestAimodelReachesTheEndpoint(t *testing.T) {
	e := liveEndpoint(t)

	// A dedicated http.Client per aimodel client: NewClient overwrites the
	// client's Timeout, so sharing one would let clients clobber each other.
	client, err := aimodel.NewClient(
		aimodel.WithProvider(openai.Name),
		aimodel.WithAPIKey(e.apiKey),
		aimodel.WithBaseURL(e.baseURL),
		aimodel.WithTimeout(60*time.Second),
	)
	if err != nil {
		t.Fatalf("aimodel.NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	resp, err := client.ChatCompletion(ctx, &ais.ChatRequest{
		Model: e.model,
		Messages: []ais.Message{
			{Role: ais.RoleUser, Content: ais.NewTextContent(`Reply with exactly: {"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("no choices in the response")
	}

	if got := resp.Choices[0].Message.Content.Text(); got == "" {
		t.Error("the message content is empty")
	} else {
		t.Logf("model %s replied: %q", resp.Model, got)
	}

	if resp.Usage.PromptTokens <= 0 {
		t.Errorf("usage.prompt_tokens = %d, want more than 0", resp.Usage.PromptTokens)
	}

	t.Logf("usage: prompt=%d completion=%d total=%d cache_read=%d reasoning=%d",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens,
		resp.Usage.CacheReadTokens, resp.Usage.ReasoningTokens)

	// Worth noticing rather than asserting: a reasoning model spends most of its
	// output budget on thinking. DeepSeek's flash model reports roughly 70% of
	// completion tokens as reasoning tokens, which is why EvalExec reports them
	// as a separate field instead of folding them into the output count.
	if resp.Usage.ReasoningTokens > 0 {
		t.Logf("this model reasons: %d of %d completion tokens were thinking",
			resp.Usage.ReasoningTokens, resp.Usage.CompletionTokens)
	}
}
