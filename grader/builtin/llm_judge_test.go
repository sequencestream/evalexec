package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/grader/builtin"
	"github.com/sequencestream/evalexec/judge"
)

// fakeJudge answers with canned text, so the Grader's own logic is tested
// without a model, a network, or a bill.
//
// The fake implements EvalExec's Judge interface — one method — rather than
// the vendor's chat interface. That is the payoff of keeping the extension
// point narrow.
type fakeJudge struct {
	reply  string
	usage  evalspec.Usage
	err    error
	prompt judge.Prompt
	calls  int
}

func (f *fakeJudge) Complete(_ context.Context, p judge.Prompt) (judge.Completion, error) {
	f.calls++
	f.prompt = p

	if f.err != nil {
		return judge.Completion{}, f.err
	}

	return judge.Completion{Text: f.reply, Usage: f.usage}, nil
}

func llmJudge(t *testing.T, j judge.Judge, params map[string]any) grader.Grader {
	t.Helper()

	if params == nil {
		params = map[string]any{"rubric": "judge faithfulness"}
	}

	g, err := builtin.NewLLMJudge(evalspec.GraderSpec{
		ID: "g", Version: "v1", Protocol: evalspec.GraderBuiltin,
		Entry: "llm_judge", Parameters: params,
	}, j)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}

	return g
}

func judgeCall() evalspec.GradeCall {
	return evalspec.GradeCall{
		EvalID: "e", TaskID: "t", CaseID: "case-001",
		Input:      json.RawMessage(`{"messages":[{"role":"user","content":"查询订单"}]}`),
		Output:     json.RawMessage(`{"messages":[{"role":"assistant","content":"订单正在配送"}]}`),
		Trajectory: json.RawMessage(`[{"sequence":1,"type":"tool","result":{"status":"shipping"}}]`),
		Reference:  json.RawMessage(`{"expected_output":"shipping"}`),
	}
}

// TestVerdictParsing covers what a model actually returns when asked for JSON.
func TestVerdictParsing(t *testing.T) {
	tests := []struct {
		name   string
		reply  string
		status evalspec.EvaluationStatus
		code   evalspec.ErrorCode
		score  float64
		scored bool
		label  string
	}{
		{
			name:   "bare json",
			reply:  `{"score":1,"label":"faithful","reason":"matches the tool result"}`,
			status: evalspec.EvaluationSuccess, score: 1, scored: true, label: "faithful",
		},
		{
			name: "wrapped in a code fence",
			// Near-universal model behaviour; unwrapping costs nothing.
			reply:  "```json\n{\"score\":0.5,\"label\":\"partial\",\"reason\":\"half right\"}\n```",
			status: evalspec.EvaluationSuccess, score: 0.5, scored: true, label: "partial",
		},
		{
			name:   "code fence without a language tag",
			reply:  "```\n{\"score\":1,\"reason\":\"ok\"}\n```",
			status: evalspec.EvaluationSuccess, score: 1, scored: true,
		},
		{
			name:   "surrounded by prose",
			reply:  `Here is my assessment: {"score":0,"label":"wrong","reason":"contradicts"} Hope that helps!`,
			status: evalspec.EvaluationSuccess, score: 0, scored: true, label: "wrong",
		},
		{
			name:   "braces inside a string do not confuse the extractor",
			reply:  `{"score":1,"reason":"the output contained {\"a\": 1} verbatim"}`,
			status: evalspec.EvaluationSuccess, score: 1, scored: true,
		},
		{
			name:   "label only, no score",
			reply:  `{"label":"faithful","reason":"no numeric scale in this rubric"}`,
			status: evalspec.EvaluationSuccess, label: "faithful",
		},
		{
			name:   "refusal is a failure, not a low score",
			reply:  `{"insufficient_evidence":true,"reason":"the trajectory is empty"}`,
			status: evalspec.EvaluationFail, code: evalspec.CodeInsufficientEvidence,
		},
		{
			name: "malformed json is a protocol error",
			// Single quotes and trailing commas are not repaired: that is the
			// model failing the contract, and hiding it behind a lenient
			// parser would mask a Judge that needs a better prompt.
			reply:  `{'score': 1, 'reason': 'nope',}`,
			status: evalspec.EvaluationFail, code: evalspec.CodeProtocolError,
		},
		{
			name:   "no json at all",
			reply:  `I think the answer is pretty good overall.`,
			status: evalspec.EvaluationFail, code: evalspec.CodeProtocolError,
		},
		{
			name:   "empty reply",
			reply:  ``,
			status: evalspec.EvaluationFail, code: evalspec.CodeProtocolError,
		},
		{
			name:   "neither score nor label",
			reply:  `{"reason":"I have opinions but no verdict"}`,
			status: evalspec.EvaluationFail, code: evalspec.CodeProtocolError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := llmJudge(t, &fakeJudge{reply: tt.reply}, nil)

			eval, err := g.Grade(t.Context(), judgeCall())
			if err != nil {
				t.Fatalf("Grade: %v", err)
			}

			if eval.Status != tt.status {
				t.Fatalf("status = %q, want %q (reason: %s)", eval.Status, tt.status, eval.Reason)
			}

			if tt.code != "" {
				if eval.Error == nil || eval.Error.Code != tt.code {
					t.Errorf("error = %+v, want code %q", eval.Error, tt.code)
				}
			}

			if tt.scored {
				if eval.Score == nil || *eval.Score != tt.score {
					t.Errorf("score = %v, want %v", eval.Score, tt.score)
				}
			} else if eval.Score != nil {
				t.Errorf("score = %v, want none", *eval.Score)
			}

			if tt.label != "" && (eval.Label == nil || *eval.Label != tt.label) {
				t.Errorf("label = %v, want %q", eval.Label, tt.label)
			}
		})
	}
}

// TestScoreIsPassedThroughUnjudged pins the no-interpretation rule. min_score and
// max_score are scale metadata; EvalExec neither clamps to them nor rejects a
// score outside them, because interpreting a score is not its job.
func TestScoreIsPassedThroughUnjudged(t *testing.T) {
	g := llmJudge(t, &fakeJudge{reply: `{"score":7.5,"label":"great","reason":"way outside the stated scale"}`},
		map[string]any{"rubric": "r", "min_score": float64(0), "max_score": float64(1)})

	eval, err := g.Grade(t.Context(), judgeCall())
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}

	if eval.Score == nil || *eval.Score != 7.5 {
		t.Errorf("score = %v, want 7.5 recorded verbatim despite max_score being 1", eval.Score)
	}

	if eval.Status != evalspec.EvaluationSuccess {
		t.Errorf("status = %q, want success: a score outside the scale is the Grader's business, not ours", eval.Status)
	}
}

// TestUsageIsRecordedOnFailure pins the rule the specification calls out by
// name: a failed evaluation still spent its tokens.
func TestUsageIsRecordedOnFailure(t *testing.T) {
	usage := evalspec.Usage{JudgeInputTokens: 640, JudgeOutputTokens: 32, JudgeReasoningTokens: 24}
	g := llmJudge(t, &fakeJudge{reply: `{"insufficient_evidence":true,"reason":"nothing to go on"}`, usage: usage}, nil)

	eval, err := g.Grade(t.Context(), judgeCall())
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}

	if eval.Status != evalspec.EvaluationFail {
		t.Fatalf("status = %q, want fail", eval.Status)
	}

	if eval.Usage != usage {
		t.Errorf("usage = %+v, want %+v: a failure's tokens must not vanish from the total", eval.Usage, usage)
	}

	if eval.Score != nil {
		t.Errorf("score = %v, want null on a failure", *eval.Score)
	}
}

// TestScoreIsDiscardedOnFailure checks that a Judge which both refuses and
// offers a number has the number thrown away.
func TestScoreIsDiscardedOnFailure(t *testing.T) {
	g := llmJudge(t, &fakeJudge{
		reply: `{"insufficient_evidence":true,"score":0,"label":"bad","reason":"cannot tell"},`,
	}, nil)

	eval, err := g.Grade(t.Context(), judgeCall())
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}

	if eval.Score != nil {
		t.Errorf("score = %v, want null: a refusal is not a zero even when the judge offers one", *eval.Score)
	}
}

// TestJudgeErrorsBecomeFailures covers the transport-level codes.
func TestJudgeErrorsBecomeFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want evalspec.ErrorCode
	}{
		{
			name: "timeout",
			err:  &judge.Error{Code: evalspec.CodeTimeout, Message: "deadline exceeded"},
			want: evalspec.CodeTimeout,
		},
		{
			name: "judge error",
			err:  &judge.Error{Code: evalspec.CodeJudgeError, Message: "HTTP 500"},
			want: evalspec.CodeJudgeError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := llmJudge(t, &fakeJudge{err: tt.err}, nil)

			eval, err := g.Grade(t.Context(), judgeCall())
			if err != nil {
				t.Fatalf("Grade: %v", err)
			}

			if eval.Status != evalspec.EvaluationFail {
				t.Fatalf("status = %q, want fail", eval.Status)
			}

			if eval.Error == nil || eval.Error.Code != tt.want {
				t.Errorf("error = %+v, want code %q", eval.Error, tt.want)
			}
		})
	}
}

// TestCancellationPropagates guards the single easiest distinction to get
// wrong. A cancelled call is not a failed evaluation: the
// sample was never finished, so it belongs to the run's stop path and must
// reach the runner as an error rather than as a fail record.
func TestCancellationPropagates(t *testing.T) {
	g := llmJudge(t, &fakeJudge{err: judge.ErrCancelled}, nil)

	_, err := g.Grade(t.Context(), judgeCall())
	if err == nil {
		t.Fatal("a cancelled judge call must propagate, not become a failed evaluation")
	}

	if !errors.Is(err, judge.ErrCancelled) {
		t.Errorf("error = %v, want it to wrap ErrCancelled", err)
	}
}

// TestPromptCarriesOnlyDeclaredFields checks that the prompt matches what the
// Grader declared it requires. Sending more would inflate every call and let
// the Judge lean on evidence the pre-check never guaranteed.
func TestPromptCarriesOnlyDeclaredFields(t *testing.T) {
	tests := []struct {
		name        string
		params      map[string]any
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name:        "neither optional field",
			params:      map[string]any{"rubric": "r"},
			wantPresent: []string{"<input>", "<output>", "<rubric>"},
			wantAbsent:  []string{"<trajectory>", "<reference>"},
		},
		{
			name:        "trajectory only",
			params:      map[string]any{"rubric": "r", "use_trajectory": true},
			wantPresent: []string{"<trajectory>"},
			wantAbsent:  []string{"<reference>"},
		},
		{
			name:        "reference only",
			params:      map[string]any{"rubric": "r", "use_reference": true},
			wantPresent: []string{"<reference>"},
			wantAbsent:  []string{"<trajectory>"},
		},
		{
			name:        "both",
			params:      map[string]any{"rubric": "r", "use_reference": true, "use_trajectory": true},
			wantPresent: []string{"<trajectory>", "<reference>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeJudge{reply: `{"score":1,"reason":"ok"}`}

			g := llmJudge(t, f, tt.params)
			if _, err := g.Grade(t.Context(), judgeCall()); err != nil {
				t.Fatalf("Grade: %v", err)
			}

			for _, want := range tt.wantPresent {
				if !strings.Contains(f.prompt.User, want) {
					t.Errorf("prompt lacks %s:\n%s", want, f.prompt.User)
				}
			}

			for _, unwanted := range tt.wantAbsent {
				if strings.Contains(f.prompt.User, unwanted) {
					t.Errorf("prompt contains %s but the Grader did not declare it:\n%s", unwanted, f.prompt.User)
				}
			}
		})
	}
}

// TestPromptAsksForARefusalOption checks that the system prompt tells the
// Judge it may decline. Without that, a Judge with nothing to go on invents a
// score, and an invented number is worse than a recorded failure.
func TestPromptAsksForARefusalOption(t *testing.T) {
	f := &fakeJudge{reply: `{"score":1,"reason":"ok"}`}

	g := llmJudge(t, f, nil)
	if _, err := g.Grade(t.Context(), judgeCall()); err != nil {
		t.Fatalf("Grade: %v", err)
	}

	if !strings.Contains(f.prompt.System, "insufficient_evidence") {
		t.Errorf("system prompt should offer a refusal path:\n%s", f.prompt.System)
	}

	// Structured output is off by default: it is not portable across
	// OpenAI-compatible endpoints, and since EvalExec does not retry, an
	// endpoint that rejects the request loses the sample.
	if f.prompt.ResponseSchema != nil {
		t.Error("structured output must be opt-in, not the default")
	}

	optedIn := &fakeJudge{reply: `{"score":1,"reason":"ok"}`}

	g = llmJudge(t, optedIn, map[string]any{"rubric": "r", "structured_output": true})
	if _, err := g.Grade(t.Context(), judgeCall()); err != nil {
		t.Fatalf("Grade: %v", err)
	}

	if optedIn.prompt.ResponseSchema == nil {
		t.Error("structured_output=true must request a schema")
	}
}

func TestRubricIsRequired(t *testing.T) {
	for _, params := range []map[string]any{
		nil,
		{},
		{"rubric": ""},
		{"rubric": "   "},
	} {
		_, err := builtin.NewLLMJudge(evalspec.GraderSpec{Entry: "llm_judge", Parameters: params}, &fakeJudge{})
		if err == nil {
			t.Errorf("params %v must be rejected: a Judge with no rubric grades against nothing", params)
		}
	}
}

// TestNilJudgeIsPermittedForDeclaration covers why the constructor tolerates a
// nil Judge: the pre-check builds a Grader at step 3 purely to ask what it
// declares, and step 4 is where the Judge configuration is checked. Refusing
// here would report a Judge problem as a Grader-declaration one.
func TestNilJudgeIsPermittedForDeclaration(t *testing.T) {
	g, err := builtin.NewLLMJudge(evalspec.GraderSpec{
		Entry: "llm_judge", Parameters: map[string]any{"rubric": "r"},
	}, nil)
	if err != nil {
		t.Fatalf("a nil judge must be allowed for declaration resolution: %v", err)
	}

	d := g.Declare()
	if !d.RequiresJudge {
		t.Error("llm_judge must declare that it needs a Judge")
	}

	// Grading with one is still refused, rather than panicking.
	eval, err := g.Grade(t.Context(), judgeCall())
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}

	if eval.Status != evalspec.EvaluationFail || eval.Error.Code != evalspec.CodeInternalError {
		t.Errorf("grading with no judge = %+v, want an internal_error failure", eval.Error)
	}
}

func TestLLMJudgeDeclaration(t *testing.T) {
	g := llmJudge(t, &fakeJudge{}, map[string]any{"rubric": "r"})

	d := g.Declare()
	if !d.RequiresJudge {
		t.Error("requires_judge must be true")
	}

	requires, err := d.EffectiveRequires(map[string]any{"rubric": "r", "use_trajectory": true})
	if err != nil {
		t.Fatalf("EffectiveRequires: %v", err)
	}

	if len(requires) != 3 {
		t.Errorf("requires = %v, want input, output and trajectory", requires)
	}
}
