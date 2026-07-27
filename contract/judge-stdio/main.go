// Command judge-stdio is a reference stdio-jsonl Judge.
//
// The payload is byte-for-byte the same as contract/judge-http's: one request
// line in, one response line out. Only the transport differs, which is what
// makes the two protocols one contract with two carriers rather than two
// contracts.
//
// Each file under contract/ is deliberately self-contained. Someone
// implementing this in another language reads one file, not a package graph.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const maxLineBytes = 32 << 20

// Request is what the host sends.
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Response is what the host expects back.
type Response struct {
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
	Error   *Error `json:"error,omitempty"`
}

// Usage uses the result document's field names.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Error reports a Judge-side failure.
type Error struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func main() {
	in := bufio.NewReaderSize(os.Stdin, 64*1024)
	out := bufio.NewWriter(os.Stdout)

	defer func() { _ = out.Flush() }()

	enc := json.NewEncoder(out)

	for {
		line, err := readLine(in)
		if err != nil {
			return
		}

		if len(line) == 0 {
			continue
		}

		var (
			req  Request
			resp Response
		)

		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(os.Stderr, "cannot decode judge request: %v\n", err)

			// Answer anyway: the host waits for exactly one line, and silence
			// would hang it until the timeout.
			resp = Response{Error: &Error{Code: "invalid_request", Message: "cannot decode the request"}}
		} else {
			prompt := userPrompt(req)
			verdict := verdictFor(prompt)

			fmt.Fprintf(os.Stderr, "judged a prompt of %d bytes\n", len(prompt))

			resp = Response{
				Content: verdict,
				Usage: Usage{
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

func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte

	for {
		chunk, err := r.ReadSlice('\n')
		out = append(out, chunk...)

		switch {
		case err == nil:
			return trimNewline(out), nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(out) > maxLineBytes {
				return nil, fmt.Errorf("line over the %d byte limit", maxLineBytes)
			}

			continue
		default:
			if len(out) > 0 {
				return trimNewline(out), nil
			}

			return nil, err
		}
	}
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}

	return b
}

func userPrompt(req Request) string {
	for _, m := range req.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}

	return ""
}

// verdictFor applies the same rule as the HTTP reference.
//
// The refusal branch is the one worth copying: with no trajectory there is
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
