package judge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/judge"
)

const testKeyEnv = "EVALEXEC_TEST_JUDGE_KEY"

// spec builds a judge_model configuration pointing at a test server.
func spec(endpoint string) *evalspec.JudgeModelSpec {
	return &evalspec.JudgeModelSpec{
		Protocol:   evalspec.JudgeOpenAIChat,
		Endpoint:   endpoint,
		Auth:       evalspec.Auth{Type: evalspec.AuthBearerEnv, Env: testKeyEnv},
		Parameters: map[string]any{"model": "test-model", "temperature": float64(0)},
		TimeoutMS:  2000,
	}
}

// chatServer answers chat completion requests with a canned reply.
func chatServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return srv
}

// okReply writes a well-formed OpenAI-shaped response.
func okReply(content string, promptTokens, completionTokens int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"model":   "test-model",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}}},
			"usage": map[string]any{
				"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
				"total_tokens": promptTokens + completionTokens,
			},
		})
	}
}

func TestCompleteAgainstAServer(t *testing.T) {
	t.Setenv(testKeyEnv, "test-key-value")

	var gotAuth, gotBody string

	srv := chatServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		var body map[string]any

		_ = json.NewDecoder(r.Body).Decode(&body)

		if b, err := json.Marshal(body); err == nil {
			gotBody = string(b)
		}

		okReply(`{"score":1,"reason":"fine"}`, 100, 20)(w, r)
	})

	j, err := judge.New(spec(srv.URL), 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := j.Complete(t.Context(), judge.Prompt{System: "sys", User: "usr"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got.Text != `{"score":1,"reason":"fine"}` {
		t.Errorf("text = %q", got.Text)
	}

	if got.Usage.JudgeInputTokens != 100 || got.Usage.JudgeOutputTokens != 20 {
		t.Errorf("usage = %+v, want 100 in and 20 out", got.Usage)
	}

	if gotAuth != "Bearer test-key-value" {
		t.Errorf("Authorization = %q, want the credential from the environment", gotAuth)
	}

	// The model must be set on the request rather than left to any client
	// default, so aimodel's AI_MODEL fallback can never take effect.
	if !strings.Contains(gotBody, `"model":"test-model"`) {
		t.Errorf("request body should set the model explicitly: %s", gotBody)
	}
}

// TestUsageCarriesReasoningTokens matters for reasoning Judges, which
// routinely spend more tokens thinking than answering. Without this the run
// total cannot be reconciled with the bill.
func TestUsageCarriesReasoningTokens(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	srv := chatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "{}"}}},
			"usage": map[string]any{
				"prompt_tokens": 13, "completion_tokens": 33, "total_tokens": 46,
				"completion_tokens_details": map[string]any{"reasoning_tokens": 27},
				"prompt_tokens_details":     map[string]any{"cached_tokens": 5},
			},
		})
	})

	j, err := judge.New(spec(srv.URL), 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := j.Complete(t.Context(), judge.Prompt{User: "u"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got.Usage.JudgeReasoningTokens != 27 {
		t.Errorf("reasoning tokens = %d, want 27", got.Usage.JudgeReasoningTokens)
	}

	if got.Usage.JudgeCacheReadTokens != 5 {
		t.Errorf("cache read tokens = %d, want 5", got.Usage.JudgeCacheReadTokens)
	}
}

func TestErrorCodes(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	tests := []struct {
		name    string
		handler http.HandlerFunc
		timeout int64
		want    evalspec.ErrorCode
	}{
		{
			name: "server error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error","code":"internal"}}`))
			},
			want: evalspec.CodeJudgeError,
		},
		{
			name: "rate limited",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"slow down","code":"rate_limit_exceeded"}}`))
			},
			// EvalExec does not retry. A rate limit is a failed evaluation,
			// and re-running the whole thing is the caller's decision.
			want: evalspec.CodeJudgeError,
		},
		{
			name: "unparseable response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`this is not json`))
			},
			want: evalspec.CodeJudgeError,
		},
		{
			name: "no choices",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"choices":[]}`))
			},
			want: evalspec.CodeJudgeError,
		},
		{
			name: "slower than the timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(2 * time.Second):
				case <-r.Context().Done():
					return
				}

				okReply("{}", 1, 1)(w, r)
			},
			timeout: 50,
			want:    evalspec.CodeTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := chatServer(t, tt.handler)

			cfg := spec(srv.URL)
			if tt.timeout > 0 {
				cfg.TimeoutMS = tt.timeout
			}

			j, err := judge.New(cfg, 1)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			_, err = j.Complete(t.Context(), judge.Prompt{User: "u"})
			if err == nil {
				t.Fatal("Complete returned no error")
			}

			code, isFailure := judge.CodeOf(err)
			if !isFailure {
				t.Fatalf("error %v should classify as a failed evaluation", err)
			}

			if code != tt.want {
				t.Errorf("code = %q, want %q (error: %v)", code, tt.want, err)
			}
		})
	}
}

// TestErrorMessageOmitsTheResponseBody guards against leaking a prompt echo
// into a result document. The status and provider code are useful; the body
// belongs in logs/.
func TestErrorMessageOmitsTheResponseBody(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	const echoed = "SENSITIVE-PROMPT-ECHO-12345"

	srv := chatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"code":"invalid_request"}}`, echoed)
	})

	j, err := judge.New(spec(srv.URL), 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = j.Complete(t.Context(), judge.Prompt{User: "u"})
	if err == nil {
		t.Fatal("Complete returned no error")
	}

	var je *judge.Error
	if !errors.As(err, &je) {
		t.Fatalf("error is %T, want *judge.Error", err)
	}

	if strings.Contains(je.Message, echoed) {
		t.Errorf("the recorded message carries the response body: %q", je.Message)
	}

	if !strings.Contains(je.Message, "400") {
		t.Errorf("the recorded message should name the status: %q", je.Message)
	}
}

// TestCancellationIsNotATimeout is dev-plan's headline risk. A cancelled call
// means the sample was never finished — it belongs to the run's stop path and
// must not be recorded as a failed evaluation at all.
func TestCancellationIsNotATimeout(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	started, release := make(chan struct{}), make(chan struct{})

	srv := chatServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)

		// Wait to be released rather than for the request context, so the
		// handler cannot outlive the test: httptest.Server.Close blocks on
		// outstanding handlers, and relying on the server to notice a closed
		// connection would make shutdown depend on transport timing.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	// Cleanups run last-registered-first, so this releases the handler before
	// chatServer's own cleanup closes the server.
	t.Cleanup(func() { close(release) })

	j, err := judge.New(spec(srv.URL), 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		<-started
		cancel()
	}()

	_, err = j.Complete(ctx, judge.Prompt{User: "u"})
	if err == nil {
		t.Fatal("Complete returned no error")
	}

	if !errors.Is(err, judge.ErrCancelled) {
		t.Fatalf("error = %v, want ErrCancelled", err)
	}

	code, isFailure := judge.CodeOf(err)
	if isFailure {
		t.Errorf("a cancelled call classified as a %q failure; it must not be recorded as one", code)
	}
}

// TestClassifyDistinguishesCancellationFromDeadline is the same rule at the
// unit level, where both context errors are exercised directly.
func TestClassifyDistinguishesCancellationFromDeadline(t *testing.T) {
	cancelled := judge.Classify(fmt.Errorf("post failed: %w", context.Canceled))
	if !errors.Is(cancelled, judge.ErrCancelled) {
		t.Errorf("a cancellation must map to ErrCancelled, got %v", cancelled)
	}

	if _, isFailure := judge.CodeOf(cancelled); isFailure {
		t.Error("a cancellation must not classify as a failed evaluation")
	}

	deadline := judge.Classify(fmt.Errorf("post failed: %w", context.DeadlineExceeded))

	code, isFailure := judge.CodeOf(deadline)
	if !isFailure || code != evalspec.CodeTimeout {
		t.Errorf("a deadline must map to a timeout failure, got %q (failure=%v)", code, isFailure)
	}

	if judge.Classify(nil) != nil {
		t.Error("Classify(nil) must be nil")
	}
}

func TestParameterAllowList(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	tests := []struct {
		name    string
		params  map[string]any
		wantErr string
	}{
		{
			name: "every supported key",
			params: map[string]any{
				"model": "m", "temperature": float64(0), "max_completion_tokens": float64(512),
				"top_p": 0.9, "top_k": float64(40), "stop": []any{"END"},
				"reasoning_effort": "low", "parallel_tool_calls": false,
			},
		},
		{
			// aimodel v0.5.0 narrowed its canonical fields to those shared by
			// at least two providers, and seed did not survive. Accepting it
			// silently would promise a determinism nothing delivers.
			name:    "seed is not supported",
			params:  map[string]any{"model": "m", "seed": float64(42)},
			wantErr: "seed",
		},
		{
			name:    "misspelled parameter",
			params:  map[string]any{"model": "m", "temperatur": float64(0)},
			wantErr: "temperatur",
		},
		{
			name:    "wrong type",
			params:  map[string]any{"model": "m", "temperature": "cold"},
			wantErr: "temperature",
		},
		{
			name:    "fractional token limit",
			params:  map[string]any{"model": "m", "max_completion_tokens": 100.5},
			wantErr: "whole number",
		},
		{
			name:    "model is required",
			params:  map[string]any{"temperature": float64(0)},
			wantErr: "model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := spec("https://example.invalid/v1")
			cfg.Parameters = tt.params

			_, err := judge.New(cfg, 1)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("New: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("params %v must be rejected", tt.params)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestConstructionValidatesConfiguration(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	tests := []struct {
		name    string
		mutate  func(*evalspec.JudgeModelSpec)
		wantErr string
	}{
		{
			name:    "missing endpoint",
			mutate:  func(s *evalspec.JudgeModelSpec) { s.Endpoint = "" },
			wantErr: "base URL",
		},
		{
			name:    "unsupported protocol",
			mutate:  func(s *evalspec.JudgeModelSpec) { s.Protocol = "grpc" },
			wantErr: "unsupported protocol",
		},
		{
			name: "stdio-jsonl needs an executable",
			mutate: func(s *evalspec.JudgeModelSpec) {
				s.Protocol = evalspec.JudgeStdioJSONL
				s.Endpoint = "/definitely/not/an/executable"
			},
			wantErr: "cannot use",
		},
		{
			name: "credential environment variable is empty",
			mutate: func(s *evalspec.JudgeModelSpec) {
				s.Auth = evalspec.Auth{Type: evalspec.AuthBearerEnv, Env: "EVALEXEC_DEFINITELY_UNSET"}
			},
			wantErr: "EVALEXEC_DEFINITELY_UNSET",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := spec("https://example.invalid/v1")
			tt.mutate(cfg)

			_, err := judge.New(cfg, 1)
			if err == nil {
				t.Fatal("New returned no error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestAuthNoneNeedsNoCredential covers the local unauthenticated Judge.
// aimodel rejects an empty API key outright, so the implementation supplies a
// meaningless placeholder rather than leaving the endpoint unreachable.
func TestAuthNoneNeedsNoCredential(t *testing.T) {
	srv := chatServer(t, okReply(`{"score":1,"reason":"ok"}`, 1, 1))

	cfg := spec(srv.URL)
	cfg.Auth = evalspec.Auth{Type: evalspec.AuthNone}

	j, err := judge.New(cfg, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := j.Complete(t.Context(), judge.Prompt{User: "u"}); err != nil {
		t.Errorf("Complete: %v", err)
	}
}

// TestCheckerIsThePreCheck confirms the pre-check hook is the real constructor,
// so an unusable configuration fails before the run rather than on it.
func TestCheckerIsThePreCheck(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	c := judge.Checker{Concurrency: 1}

	if err := c.Check(spec("https://example.invalid/v1")); err != nil {
		t.Errorf("a valid configuration must pass: %v", err)
	}

	bad := spec("")
	if err := c.Check(bad); err == nil {
		t.Error("a configuration with no endpoint must be rejected")
	}
}

// TestEveryProtocolConstructs checks that all four Judge protocols resolve to a
// provider. Until M6 two of them returned an error; a test asserting that would
// now be asserting the opposite of the truth.
func TestEveryProtocolConstructs(t *testing.T) {
	t.Setenv(testKeyEnv, "k")

	tests := []struct {
		protocol evalspec.JudgeProtocol
		endpoint string
	}{
		{protocol: evalspec.JudgeOpenAIChat, endpoint: "https://example.invalid/v1"},
		{protocol: evalspec.JudgeAnthropicMessages, endpoint: "https://example.invalid"},
		{protocol: evalspec.JudgeHTTPJSON, endpoint: "https://example.invalid/grade"},
	}

	for _, tt := range tests {
		t.Run(string(tt.protocol), func(t *testing.T) {
			cfg := spec(tt.endpoint)
			cfg.Protocol = tt.protocol

			if _, err := judge.New(cfg, 1); err != nil {
				t.Errorf("New: %v", err)
			}
		})
	}

	// stdio-jsonl needs a real executable, so it is checked separately with one.
	script := filepath.Join(t.TempDir(), "judge.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := spec(script)
	cfg.Protocol = evalspec.JudgeStdioJSONL

	if _, err := judge.New(cfg, 1); err != nil {
		t.Errorf("New for stdio-jsonl: %v", err)
	}
}

func TestSupportedParameters(t *testing.T) {
	got := judge.SupportedParameters()
	if len(got) != 10 {
		t.Errorf("got %d supported parameters, want 10: %v", len(got), got)
	}

	// Mutating the returned slice must not change the package's own list.
	got[0] = "tampered"

	if judge.SupportedParameters()[0] == "tampered" {
		t.Error("SupportedParameters returned the package's own slice")
	}
}
