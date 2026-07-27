package evalspec_test

import (
	"encoding/json"
	"fmt"

	"github.com/sequencestream/evalexec/evalspec"
)

// A session distinguishes an absent field from one that is explicitly null.
// This is what a Grader's requires declaration is checked against: an agent
// that produced no final output says so with "output": null, which is a
// different thing from a row that forgot to mention output at all.
func ExampleSession_Has() {
	withNull := `{"case_id":"c1","input":{},"output":null}`
	withoutKey := `{"case_id":"c2","input":{}}`

	for _, line := range []string{withNull, withoutKey} {
		var s evalspec.Session
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			panic(err)
		}

		fmt.Printf("%s: has=%v isNull=%v\n", s.CaseID, s.Has(evalspec.FieldOutput), s.IsNull(evalspec.FieldOutput))
	}

	// Output:
	// c1: has=true isNull=true
	// c2: has=false isNull=false
}

// MissingFields answers the pre-check question directly: does this row carry
// everything the Grader declared it needs?
func ExampleSession_MissingFields() {
	var s evalspec.Session
	if err := json.Unmarshal([]byte(`{"case_id":"c1","input":{},"output":null}`), &s); err != nil {
		panic(err)
	}

	required := []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference}
	fmt.Println(s.MissingFields(required))

	// Output:
	// [reference]
}

// A failed evaluation never carries a score. The constructor takes no score
// argument, so there is no way to record a failure as a zero.
func ExampleNewFailEvaluation() {
	e := evalspec.NewFailEvaluation(
		evalspec.CodeInsufficientEvidence,
		"reference.expected_output is absent",
		"nothing to compare against",
		nil,
		evalspec.Usage{JudgeInputTokens: 640, JudgeOutputTokens: 32},
		180,
	)

	fmt.Println("status:", e.Status)
	fmt.Println("score is nil:", e.Score == nil)
	fmt.Println("usage is still recorded:", e.Usage.JudgeInputTokens)

	// Output:
	// status: fail
	// score is nil: true
	// usage is still recorded: 640
}

// Validate refuses a result whose counts do not add up, which is what stops a
// hand-assembled result from being written to disk.
func ExampleEvalResult_Validate() {
	r := evalspec.EvalResult{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      "eval-1",
		TaskID:      "task-1",
		Status:      evalspec.RunCompleted,
		Counts:      evalspec.Counts{Total: 10, Completed: 9, Skipped: 0},
		Evaluation: evalspec.EvaluationSummary{
			Evaluated: 9, Success: 9,
			Score: evalspec.ScoreStats{Count: 0},
		},
	}

	fmt.Println(r.Validate())

	// Output:
	// counts.total: must equal completed + skipped: 10 != 9 + 0
}

// A backfilled record has a fixed shape: no evaluation, no timestamps, and a
// reason matching the run's stop reason.
func ExampleNewSkippedRecord() {
	r := evalspec.NewSkippedRecord("task-1", "eval-1", "case-100", 100, evalspec.StopInterrupt)

	data, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))

	// Output:
	// {"task_id":"task-1","eval_id":"eval-1","case_id":"case-100","sequence":100,"status":"skipped","evaluation":null,"started_at":null,"finished_at":null,"error":{"code":"skipped","reason":"interrupt"}}
}
