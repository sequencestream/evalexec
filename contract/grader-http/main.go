// Command grader-http is a reference http-json Grader.
//
// It exists so that someone implementing a Grader in another language has a
// working counterpart to compare against, and so that EvalExec's own tests
// exercise the protocol rather than a mock of it.
//
// The grading rule is the simplest thing that produces all three outcomes:
// compare output to reference.expected_output, and refuse when there is nothing
// to compare against. That refusal is the part worth copying — a Grader that
// cannot conclude must say so, because a guessed score is worse than a recorded
// failure.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
)

// GradeCall is the request EvalExec posts. Only the fields this Grader uses are
// declared; the rest are ignored, which is what makes the protocol extensible.
type GradeCall struct {
	EvalID    string          `json:"eval_id"`
	TaskID    string          `json:"task_id"`
	CaseID    string          `json:"case_id"`
	Output    json.RawMessage `json:"output"`
	Reference json.RawMessage `json:"reference"`
}

// Evaluation is the response. It mirrors the evaluation object of a record.
type Evaluation struct {
	Status   string     `json:"status"`
	Score    *float64   `json:"score"`
	Label    *string    `json:"label"`
	Reason   string     `json:"reason,omitempty"`
	Evidence []Evidence `json:"evidence"`
	Usage    Usage      `json:"usage"`
	Error    *EvalError `json:"error"`
}

type Evidence struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Value  any    `json:"value"`
}

type Usage struct {
	JudgeInputTokens  int `json:"judge_input_tokens"`
	JudgeOutputTokens int `json:"judge_output_tokens"`
}

type EvalError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on")

	flag.Parse()

	http.HandleFunc("/grade", handle)

	log.Printf("reference http-json grader listening on %s", *addr)

	if err := http.ListenAndServe(*addr, nil); err != nil { //nolint:gosec // reference implementation
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	var call GradeCall
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		// A malformed call is the host's fault, so it gets a 4xx rather than an
		// evaluation.
		http.Error(w, "cannot decode the grade call", http.StatusBadRequest)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Grade(call))
}

// Grade is the reference rule, exported so the stdio variant shares it.
func Grade(call GradeCall) Evaluation {
	expected, ok := expectedOutput(call.Reference)
	if !ok {
		// No conclusion is possible. A failure carries no score: counting it as
		// zero would put a number nobody measured into the average.
		return Evaluation{
			Status:   "fail",
			Evidence: []Evidence{},
			Error:    &EvalError{Code: "insufficient_evidence", Message: "reference.expected_output is absent"},
			Reason:   "there is no expected output to compare against",
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

	// A mismatch is a *successful* evaluation reporting zero. The Grader did its
	// job; the agent did not.
	return Evaluation{
		Status: "success",
		Score:  &score,
		Label:  &label,
		Reason: fmt.Sprintf("compared output with reference.expected_output: %s", label),
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
