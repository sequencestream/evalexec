// Command grader-stdio is a reference stdio-jsonl Grader.
//
// It reads one JSON grade call per line on stdin and writes one JSON evaluation
// per line on stdout. The grading rule is identical to contract/grader-http, so
// the two differ only in transport — which is the point: an implementer picks
// whichever transport suits and writes the same JSON handling either way.
//
// Each file under contract/ is deliberately self-contained, duplication and
// all. Someone implementing this protocol in another language reads one file,
// not a package graph.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
)

// maxLineBytes matches the host's limit, so a large evidence list is not
// truncated on either side.
const maxLineBytes = 32 << 20

// GradeCall is the request the host sends. Only the fields this Grader uses are
// declared; ignoring the rest is what lets the protocol grow.
type GradeCall struct {
	EvalID    string          `json:"eval_id"`
	TaskID    string          `json:"task_id"`
	CaseID    string          `json:"case_id"`
	Output    json.RawMessage `json:"output"`
	Reference json.RawMessage `json:"reference"`
}

// Evaluation is the reply. It mirrors the evaluation object of a record.
type Evaluation struct {
	Status   string     `json:"status"`
	Score    *float64   `json:"score"`
	Label    *string    `json:"label"`
	Reason   string     `json:"reason,omitempty"`
	Evidence []Evidence `json:"evidence"`
	Usage    Usage      `json:"usage"`
	Error    *EvalError `json:"error"`
}

// Evidence is one cited value.
type Evidence struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Value  any    `json:"value"`
}

// Usage counts Judge tokens; a rule-based Grader spends none.
type Usage struct {
	JudgeInputTokens  int `json:"judge_input_tokens"`
	JudgeOutputTokens int `json:"judge_output_tokens"`
}

// EvalError explains a failure.
type EvalError struct {
	Code    string `json:"code"`
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
			// End of input is how the host says it is finished.
			return
		}

		if len(line) == 0 {
			continue
		}

		var (
			call       GradeCall
			evaluation Evaluation
		)

		if err := json.Unmarshal(line, &call); err != nil {
			fmt.Fprintf(os.Stderr, "cannot decode grade call: %v\n", err)

			// Answer anyway. The host waits for exactly one line per call, so
			// going silent would hang it until the timeout instead of failing
			// immediately.
			evaluation = Evaluation{
				Status:   "fail",
				Evidence: []Evidence{},
				Error:    &EvalError{Code: "protocol_error", Message: "the grade call could not be decoded"},
			}
		} else {
			// Diagnostics go to stderr, which the host collects into logs/.
			// stdout carries the answer and nothing else.
			fmt.Fprintf(os.Stderr, "grading %s\n", call.CaseID)

			evaluation = grade(call)
		}

		_ = enc.Encode(evaluation)

		// Flush every line. Buffered output leaves the host waiting for an
		// answer that is sitting in this process's memory.
		_ = out.Flush()
	}
}

// readLine reads one line, growing past the buffered reader's window so a large
// call is not truncated.
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
				// A final line with no newline is still a call.
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

// grade compares the output to the expected value.
func grade(call GradeCall) Evaluation {
	expected, found := expectedOutput(call.Reference)
	if !found {
		// No conclusion is possible. The score stays null: a zero is a
		// measurement, and inventing one puts a number nobody took into the
		// average.
		return Evaluation{
			Status:   "fail",
			Evidence: []Evidence{},
			Reason:   "there is no expected output to compare against",
			Error:    &EvalError{Code: "insufficient_evidence", Message: "reference.expected_output is absent"},
		}
	}

	var actual any
	if len(call.Output) > 0 {
		if err := json.Unmarshal(call.Output, &actual); err != nil {
			return Evaluation{
				Status:   "fail",
				Evidence: []Evidence{},
				Error:    &EvalError{Code: "protocol_error", Message: "output is not valid JSON"},
			}
		}
	}

	score, label := 0.0, "mismatch"
	if reflect.DeepEqual(actual, expected) {
		score, label = 1.0, "match"
	}

	// A mismatch is a *successful* evaluation reporting zero: the comparison
	// was made and a conclusion reached. That is the Grader's job done, however
	// the agent performed.
	return Evaluation{
		Status: "success",
		Score:  &score,
		Label:  &label,
		Reason: "compared output with reference.expected_output: " + label,
		Evidence: []Evidence{
			{Source: "output", Path: "$", Value: actual},
			{Source: "reference", Path: "$.expected_output", Value: expected},
		},
	}
}

func expectedOutput(reference json.RawMessage) (any, bool) {
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
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}

	return v, true
}
