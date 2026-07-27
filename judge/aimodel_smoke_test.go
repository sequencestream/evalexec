//go:build e2e

// This smoke test proves the pinned aimodel version can actually reach a live
// OpenAI-compatible endpoint and fill in token usage. It is excluded from
// `make test` on purpose: it needs credentials and costs money. Run it with
// `make test-e2e`.
package judge_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/aimodel/ais"
	"github.com/vogo/aimodel/provider/openai"
)

// TestAimodelChatCompletionSmoke is the first piece of the end-to-end
// verification: it checks that the endpoint used for the final E2E run
// answers, and that Usage comes back populated (EvalExec's usage summary is
// built entirely from those counters).
func TestAimodelChatCompletionSmoke(t *testing.T) {
	baseURL, apiKey, model := os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("set OPENAI_BASE_URL, OPENAI_API_KEY and OPENAI_MODEL to run the aimodel smoke test")
	}

	// A dedicated http.Client per aimodel client: NewClient overwrites the
	// client's Timeout, so sharing one would let clients clobber each other.
	client, err := aimodel.NewClient(
		aimodel.WithProvider(openai.Name),
		aimodel.WithAPIKey(apiKey),
		aimodel.WithBaseURL(baseURL),
		aimodel.WithTimeout(60*time.Second),
	)
	if err != nil {
		t.Fatalf("aimodel.NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	resp, err := client.ChatCompletion(ctx, &ais.ChatRequest{
		Model: model,
		Messages: []ais.Message{
			{Role: ais.RoleUser, Content: ais.NewTextContent(`Reply with exactly: {"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("no choices in response")
	}

	if got := resp.Choices[0].Message.Content.Text(); got == "" {
		t.Error("empty message content")
	} else {
		t.Logf("model %s replied: %q", resp.Model, got)
	}

	if resp.Usage.PromptTokens <= 0 {
		t.Errorf("usage.prompt_tokens = %d, want > 0", resp.Usage.PromptTokens)
	}

	t.Logf("usage: prompt=%d completion=%d total=%d cache_read=%d reasoning=%d",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens,
		resp.Usage.CacheReadTokens, resp.Usage.ReasoningTokens)
}
