// Command judgestdio is a throwaway stdio-jsonl Judge used by the
// interoperability tests.
//
// It answers with the same rule as the http-json and openai-chat stubs in
// interop_test.go, so any verdict difference between the three is the
// transport's fault — which is what the test is for.
//
// It lives under testdata/ and is a main package, so it is invisible to
// ./... builds and to the linters.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type response struct {
	Content string `json:"content"`
	Usage   usage  `json:"usage"`
	Error   *jsErr `json:"error,omitempty"`
}

// usage uses the result document's field names.
type usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type jsErr struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8<<20)

	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()

	enc := json.NewEncoder(out)

	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}

		var (
			req  request
			resp response
		)

		if err := json.Unmarshal(line, &req); err != nil {
			// Answer anyway: the host waits for exactly one line, and silence
			// would hang it until the timeout.
			resp = response{Error: &jsErr{Code: "invalid_request", Message: "cannot decode the request"}}
		} else {
			prompt := userPrompt(req)
			verdict := verdictFor(prompt)

			fmt.Fprintf(os.Stderr, "judged a prompt of %d bytes\n", len(prompt))

			resp = response{
				Content: verdict,
				Usage: usage{
					InputTokens:  len(prompt) / 4,
					OutputTokens: len(verdict) / 4,
				},
			}
		}

		_ = enc.Encode(resp)

		// Flush every line, or the host waits on an answer held in this
		// process's memory.
		_ = out.Flush()
	}
}

func userPrompt(req request) string {
	for _, m := range req.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}

	return ""
}

// verdictFor applies the same rule as the in-process Judge stubs.
//
// The refusal branch is the one that matters: with no trajectory there is
// nothing to check the answer against, and a guessed score would be a number
// nobody measured.
func verdictFor(prompt string) string {
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

// section extracts one tagged block from the prompt. EvalExec wraps each
// session field in tags rather than nesting them in JSON.
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
