// Command graderstdio is a throwaway stdio-jsonl Grader used by the
// interoperability tests.
//
// It is not a reference implementation and makes no promises to anyone: it
// exists so the stdio-jsonl transport is exercised by a real subprocess rather
// than by an in-process stub. The grading rule matches the built-in exact_match
// Grader, so any verdict difference is the transport's fault.
//
// It lives under testdata/ and is a main package, so it is invisible to
// ./... builds and to the linters.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
)

// gradeCall is the request the host sends. Only the fields used here are
// declared; ignoring the rest is what lets the protocol grow.
type gradeCall struct {
	CaseID    string          `json:"case_id"`
	Output    json.RawMessage `json:"output"`
	Reference json.RawMessage `json:"reference"`
}

type evaluation struct {
	Status   string     `json:"status"`
	Score    *float64   `json:"score"`
	Label    *string    `json:"label"`
	Reason   string     `json:"reason,omitempty"`
	Evidence []evidence `json:"evidence"`
	Usage    usage      `json:"usage"`
	Error    *evalError `json:"error"`
}

type evidence struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Value  any    `json:"value"`
}

// usage counts Judge tokens; a rule-based Grader spends none.
type usage struct {
	JudgeInputTokens  int `json:"judge_input_tokens"`
	JudgeOutputTokens int `json:"judge_output_tokens"`
}

type evalError struct {
	Code    string `json:"code"`
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
			call gradeCall
			eval evaluation
		)

		if err := json.Unmarshal(line, &call); err != nil {
			// Answer anyway. The host waits for exactly one line per call, so
			// going silent would hang it until the timeout instead of failing
			// immediately.
			eval = evaluation{
				Status:   "fail",
				Evidence: []evidence{},
				Error:    &evalError{Code: "protocol_error", Message: "the grade call could not be decoded"},
			}
		} else {
			// Diagnostics go to stderr, which the host collects into logs/.
			// stdout carries the answer and nothing else.
			fmt.Fprintf(os.Stderr, "grading %s\n", call.CaseID)

			eval = grade(call)
		}

		_ = enc.Encode(eval)

		// Flush every line, or the host waits on an answer sitting in this
		// process's memory.
		_ = out.Flush()
	}
}

// grade compares the output to the expected value.
func grade(call gradeCall) evaluation {
	expected, found := expectedOutput(call.Reference)
	if !found {
		// No conclusion is possible. The score stays null: a zero is a
		// measurement, and inventing one puts a number nobody took into the
		// average.
		return evaluation{
			Status:   "fail",
			Evidence: []evidence{},
			Reason:   "there is no expected output to compare against",
			Error:    &evalError{Code: "insufficient_evidence", Message: "reference.expected_output is absent"},
		}
	}

	var actual any
	if len(call.Output) > 0 {
		if err := json.Unmarshal(call.Output, &actual); err != nil {
			return evaluation{
				Status:   "fail",
				Evidence: []evidence{},
				Error:    &evalError{Code: "protocol_error", Message: "output is not valid JSON"},
			}
		}
	}

	score, label := 0.0, "mismatch"
	if reflect.DeepEqual(actual, expected) {
		score, label = 1.0, "match"
	}

	// A mismatch is a *successful* evaluation reporting zero: the comparison
	// was made and a conclusion reached.
	return evaluation{
		Status: "success",
		Score:  &score,
		Label:  &label,
		Reason: "compared output with reference.expected_output: " + label,
		Evidence: []evidence{
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
