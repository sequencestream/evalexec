// Command consumer is the downstream smoke test: a separate module that
// imports evalexec and exercises the three things a library user does.
//
//  1. run one evaluation through evalexec.Run
//  2. implement and register a Grader of its own
//  3. check that Grader against the shared fixtures
//
// It lives in its own module because an examples/ directory inside the main
// module only proves the code compiles. It cannot prove that everything a
// downstream program needs is exported — a required type left behind a
// lowercase identifier compiles fine from inside the module and fails from
// outside. Only a cross-module build catches that.
//
// The README quotes this file rather than repeating it, because a copied example
// rots.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	evalexec "github.com/sequencestream/evalexec"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/fixtures"
	"github.com/sequencestream/evalexec/grader"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "consumer:", err)
		os.Exit(1)
	}

	fmt.Println("consumer: all three library use cases work")
}

func run() error {
	dir, err := os.MkdirTemp("", "evalexec-consumer-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	if err := runBuiltIn(dir); err != nil {
		return fmt.Errorf("built-in Grader: %w", err)
	}

	if err := runCustomGrader(dir); err != nil {
		return fmt.Errorf("custom Grader: %w", err)
	}

	if err := checkAgainstFixtures(); err != nil {
		return fmt.Errorf("fixture self-check: %w", err)
	}

	return nil
}

// runBuiltIn is use case 1: one evaluation, one call.
func runBuiltIn(dir string) error {
	dataset := filepath.Join(dir, "sessions.jsonl")

	rows := `{"case_id":"c1","input":{"q":"status of ORD-1"},"output":{"status":"shipping"},"reference":{"expected_output":{"status":"shipping"}}}
{"case_id":"c2","input":{"q":"status of ORD-2"},"output":{"status":"pending"},"reference":{"expected_output":{"status":"delivered"}}}
`

	if err := os.WriteFile(dataset, []byte(rows), 0o644); err != nil {
		return err
	}

	req := &evalspec.EvalRequest{
		SpecVersion: evalspec.SpecVersion,
		TaskID:      "consumer-smoke",
		Dataset:     evalspec.Dataset{Path: dataset},
		Grader: evalspec.GraderSpec{
			ID: "order-status", Version: "v1",
			Protocol: evalspec.GraderBuiltin, Entry: "exact_match",
			Requires: []evalspec.SessionField{
				evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
			},
		},
		OutputDir: filepath.Join(dir, "out-builtin"),
	}

	// An eval_id is generated when absent, so a caller that does not care about
	// correlation can leave it out.
	res, err := evalexec.Run(context.Background(), req)
	if err != nil {
		return err
	}

	if res.Status != evalspec.RunCompleted {
		return fmt.Errorf("status = %q, want completed", res.Status)
	}

	// Both samples were graded. The mismatch is a *success* with a score of 0:
	// the Grader compared the values and reached a conclusion.
	if res.Evaluation.Success != 2 {
		return fmt.Errorf("success = %d, want 2 (a mismatch is a successful evaluation)", res.Evaluation.Success)
	}

	if res.Evaluation.Score.Count != 2 {
		return fmt.Errorf("score.count = %d, want 2", res.Evaluation.Score.Count)
	}

	fmt.Printf("  built-in: %s, %d graded, mean %.2f\n",
		res.Status, res.Evaluation.Evaluated, *res.Evaluation.Score.Mean)

	return nil
}

// lengthGrader is use case 2: a Grader written outside the module.
//
// It scores an answer by whether it is within a length budget — deliberately
// trivial, because what is being demonstrated is the extension point, not the
// rule.
type lengthGrader struct {
	maxRunes int
}

// Declare states what this Grader needs, so a run can be validated before the
// Grader is ever called.
func (g *lengthGrader) Declare() grader.Declaration {
	return grader.Declaration{
		Entry:    "answer_length",
		Requires: []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
		// Declaring the parameter names is worth doing: a misspelled key is then
		// rejected before the run rather than silently ignored. Leaving Params
		// nil means "do not police my parameters", which is permitted but gives
		// up that check.
		Params: []string{"max_runes"},
	}
}

// Grade evaluates one sample.
//
// Note which situations are which. An answer that is too long is a *success*
// with a score of 0: the check ran and reached a verdict. An output that cannot
// be read at all is a *failure* with no score, because nothing was measured —
// and a failure must never be recorded as a zero.
func (g *lengthGrader) Grade(_ context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	if len(call.Output) == 0 {
		return evalspec.NewFailEvaluation(evalspec.CodeInsufficientEvidence,
			"the session carries no output", "nothing to measure", nil, evalspec.Usage{}, 0), nil
	}

	var answer string
	if err := json.Unmarshal(call.Output, &answer); err != nil {
		// Not a string, so measure its serialized form instead.
		answer = string(call.Output)
	}

	runes := len([]rune(answer))
	within := runes <= g.maxRunes

	score, label := 0.0, "too_long"
	if within {
		score, label = 1.0, "within_budget"
	}

	return evalspec.NewSuccessEvaluation(&score, &label,
		fmt.Sprintf("the answer is %d runes against a budget of %d", runes, g.maxRunes),
		[]evalspec.Evidence{{Source: "output", Path: "$", Value: runes}},
		evalspec.Usage{}, 0), nil
}

// runCustomGrader registers the Grader above and runs it under protocol
// "builtin" with its own entry name. No subprocess is involved.
func runCustomGrader(dir string) error {
	registry := grader.NewRegistry()

	registry.Register("answer_length", func(spec evalspec.GraderSpec, _ grader.Deps) (grader.Grader, error) {
		budget := 20
		if v, ok := spec.Parameters["max_runes"].(float64); ok {
			budget = int(v)
		}

		return &lengthGrader{maxRunes: budget}, nil
	})

	dataset := filepath.Join(dir, "answers.jsonl")

	rows := `{"case_id":"short","input":{"q":"how are you"},"output":"fine, thanks"}
{"case_id":"long","input":{"q":"how are you"},"output":"I am doing quite well today, thank you very much for asking"}
`

	if err := os.WriteFile(dataset, []byte(rows), 0o644); err != nil {
		return err
	}

	req := &evalspec.EvalRequest{
		SpecVersion: evalspec.SpecVersion,
		TaskID:      "consumer-smoke",
		Dataset:     evalspec.Dataset{Path: dataset},
		Grader: evalspec.GraderSpec{
			ID: "answer-length", Version: "v1",
			// "builtin" is the protocol for a Grader compiled into the binary,
			// including one registered downstream. It does not mean "one of the
			// five that ship with EvalExec".
			Protocol: evalspec.GraderBuiltin, Entry: "answer_length",
			Requires:   []evalspec.SessionField{evalspec.FieldInput, evalspec.FieldOutput},
			Parameters: map[string]any{"max_runes": float64(20)},
		},
		OutputDir: filepath.Join(dir, "out-custom"),
	}

	res, err := evalexec.Run(context.Background(), req, evalexec.WithGraderRegistry(registry))
	if err != nil {
		return err
	}

	if res.Evaluation.Success != 2 {
		return fmt.Errorf("success = %d, want 2", res.Evaluation.Success)
	}

	// One within budget, one over: the mean is 0.5.
	if res.Evaluation.Score.Mean == nil || *res.Evaluation.Score.Mean != 0.5 {
		return fmt.Errorf("score.mean = %v, want 0.5", res.Evaluation.Score.Mean)
	}

	fmt.Printf("  custom Grader: %d graded, mean %.2f\n",
		res.Evaluation.Evaluated, *res.Evaluation.Score.Mean)

	return nil
}

// checkAgainstFixtures is use case 3: read the shared fixtures to check a
// Grader's own behaviour.
//
// The fixtures are published for exactly this: they are the same data EvalExec
// tests itself against, reachable through an embedded filesystem so a downstream
// program needs no copy of its own.
func checkAgainstFixtures() error {
	data, err := fixtures.Read(fixtures.CaseMixedSuccessFail, fixtures.FileDataset)
	if err != nil {
		return err
	}

	lines := fixtures.Lines(data)
	if len(lines) == 0 {
		return fmt.Errorf("the fixture dataset is empty")
	}

	g := &lengthGrader{maxRunes: 40}

	var success, failed int

	for i, line := range lines {
		var session evalspec.Session
		if err := json.Unmarshal([]byte(line), &session); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}

		// The requirement check is the same one the host performs, and it is
		// about presence: a field explicitly set to null is present.
		if missing := session.MissingFields(g.Declare().Requires); len(missing) > 0 {
			return fmt.Errorf("line %d (%s) is missing %v", i+1, session.CaseID, missing)
		}

		call := evalspec.NewGradeCall("consumer-fixture-check", "consumer", &session, nil)

		eval, err := g.Grade(context.Background(), call)
		if err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}

		switch eval.Status {
		case evalspec.EvaluationSuccess:
			success++
		case evalspec.EvaluationFail:
			failed++
		}

		// The invariant holds for a downstream Grader too, and the controlled
		// constructors are what guarantee it.
		if eval.Status == evalspec.EvaluationFail && eval.Score != nil {
			return fmt.Errorf("line %d: a failed evaluation carries a score", i+1)
		}
	}

	fmt.Printf("  fixture check: %d graded (%d success, %d fail) from %s\n",
		len(lines), success, failed, strings.TrimSuffix(fixtures.FileDataset, ".jsonl"))

	return nil
}
