// Command judge-http is a reference http-json Judge.
//
// It answers EvalExec's Judge protocol: one request, one reply, a flat usage
// object. The format is deliberately simpler than any vendor's Chat Completions
// API, because its purpose is to be easy to implement in another language rather
// than to be compatible with a particular service.
//
// It does not call a model. It applies a trivial rule and reports plausible
// token counts, which is enough to exercise the transport, the usage
// accounting, and — most usefully — the refusal path.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// Request is what EvalExec posts.
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	// The sampling parameters arrive only when configured, so an
	// implementation handles what it was actually sent.
	Temperature         *float64 `json:"temperature,omitempty"`
	MaxCompletionTokens *int     `json:"max_completion_tokens,omitempty"`
	ResponseFormat      any      `json:"response_format,omitempty"`
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Response is what EvalExec expects back.
type Response struct {
	// Content is the reply text. One answer, not a list of candidates: the host
	// has no use for alternatives.
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
	Error   *Error `json:"error,omitempty"`
}

// Usage uses the same field names as the result document's usage block, so an
// implementer has one fewer translation to keep straight.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Error reports a failure in the Judge's own terms. The host maps it to a
// judge_error.
type Error struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on")

	flag.Parse()

	http.HandleFunc("/judge", handle)

	log.Printf("reference http-json judge listening on %s", *addr)

	if err := http.ListenAndServe(*addr, nil); err != nil { //nolint:gosec // reference implementation
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(Response{
			Error: &Error{Code: "invalid_request", Message: "cannot decode the request"},
		})

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Judge(req))
}

// Judge applies the reference rule.
func Judge(req Request) Response {
	prompt := userPrompt(req)

	verdict := Verdict(prompt)

	// Token counts are approximate but non-zero, so the host's usage summary
	// has something real to add up.
	return Response{
		Content: verdict,
		Usage: Usage{
			InputTokens:  len(prompt) / 4,
			OutputTokens: len(verdict) / 4,
		},
	}
}

func userPrompt(req Request) string {
	for _, m := range req.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}

	return ""
}

// Verdict is the reference rule, shared with the stdio variant in spirit: the
// trajectory decides.
//
// An empty trajectory means there is nothing to check the answer against, and
// the honest reply is a refusal. A Judge that guesses a score in that situation
// produces a number nobody measured, which is worse than a recorded failure —
// so this is the branch worth copying.
func Verdict(prompt string) string {
	trajectory := section(prompt, "trajectory")

	switch {
	case trajectory == "" || trajectory == "[]" || trajectory == "(absent)":
		return `{"insufficient_evidence": true, ` +
			`"reason": "the trajectory carries no facts to check the answer against"}`
	case strings.Contains(section(prompt, "output"), "签收") &&
		!strings.Contains(trajectory, "delivered"):
		return `{"score": 0, "label": "unfaithful", ` +
			`"reason": "the answer claims delivery but the trajectory does not show it", ` +
			`"evidence": [{"source": "trajectory", "path": "$[0].result.status"}]}`
	default:
		return `{"score": 1, "label": "faithful", ` +
			`"reason": "the answer is consistent with the trajectory", ` +
			`"evidence": [{"source": "trajectory", "path": "$[0].result"}]}`
	}
}

// section extracts one tagged block from the prompt.
//
// EvalExec wraps each session field in tags rather than nesting them in JSON:
// it costs fewer tokens, and a session whose content happens to contain JSON
// cannot be mistaken for the envelope around it.
func section(prompt, name string) string {
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
