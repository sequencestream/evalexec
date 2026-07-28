package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/judge"
	"github.com/sequencestream/evalexec/internal/judge/transport"
)

// JudgeFactory builds the Judge an llm_judge Grader will use.
//
// It is injected rather than constructed here so that the Grader can be tested
// against a fake with no HTTP involved, and so that the judge package stays the
// only importer of the vendor SDK.
type JudgeFactory func() (judge.Judge, error)

// NewLLMJudge builds an llm_judge Grader over a given Judge.
//
// It is exported and takes the Judge as an argument because llm_judge is the
// one built-in that needs something a Grader specification alone cannot supply.
//
// A nil Judge is permitted, and means the instance is only being built to ask
// it what it declares. The pre-check does exactly that at step 3, before it has
// reached step 4 where the Judge configuration is checked — so refusing a nil
// one here would report a Judge problem as a Grader-declaration problem, in the
// wrong step and with the wrong exit path. Grading with a nil Judge is caught
// in Grade.
func NewLLMJudge(spec evalspec.GraderSpec, j judge.Judge) (grader.Grader, error) {
	rubric, err := grader.StringParam(spec.Parameters, "rubric", "")
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(rubric) == "" {
		return nil, errors.New(`llm_judge: parameter "rubric" is required`)
	}

	useReference, err := grader.BoolParam(spec.Parameters, "use_reference", false)
	if err != nil {
		return nil, err
	}

	useTrajectory, err := grader.BoolParam(spec.Parameters, "use_trajectory", false)
	if err != nil {
		return nil, err
	}

	structured, err := grader.BoolParam(spec.Parameters, "structured_output", false)
	if err != nil {
		return nil, err
	}

	return &llmJudge{
		judge:         j,
		rubric:        rubric,
		useReference:  useReference,
		useTrajectory: useTrajectory,
		structured:    structured,
	}, nil
}

type llmJudge struct {
	judge         judge.Judge
	rubric        string
	useReference  bool
	useTrajectory bool
	structured    bool
}

func (g *llmJudge) Declare() grader.Declaration {
	d, _ := grader.LookupDeclaration(grader.EntryLLMJudge)

	return d
}

// verdict is the shape the Judge is asked to return.
type verdict struct {
	Score  *float64 `json:"score"`
	Label  *string  `json:"label"`
	Reason string   `json:"reason"`
	// InsufficientEvidence lets the Judge decline rather than guess. A refusal
	// is a failed evaluation, not a low score: it means nothing was measured.
	InsufficientEvidence bool `json:"insufficient_evidence"`
	Evidence             []struct {
		Source string `json:"source"`
		Path   string `json:"path"`
		Value  any    `json:"value"`
	} `json:"evidence"`
}

// responseSchema asks a provider that supports structured output for the shape
// above.
//
// It is sent only when the structured_output parameter says to, and the default
// is off. Structured output is not portable across OpenAI-compatible
// endpoints — DeepSeek answers a json_schema request with
// "This response_format type is unavailable now" and HTTP 400 — and because
// EvalExec does not retry, an unsupported request costs the whole sample.
// Asking in the prompt works everywhere; asking in the protocol works on some
// providers and fails loudly on the rest, so it is opt-in.
var responseSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"score":                 map[string]any{"type": []any{"number", "null"}},
		"label":                 map[string]any{"type": []any{"string", "null"}},
		"reason":                map[string]any{"type": "string"},
		"insufficient_evidence": map[string]any{"type": "boolean"},
		"evidence": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source": map[string]any{"type": "string"},
					"path":   map[string]any{"type": "string"},
				},
			},
		},
	},
	"required": []any{"reason"},
}

// Grade asks the Judge about one sample and records what it said.
func (g *llmJudge) Grade(ctx context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	if g.judge == nil {
		// Unreachable through a validated run: the pre-check refuses to start
		// one whose Grader needs a Judge it could not build.
		return evalspec.NewFailEvaluation(evalspec.CodeInternalError,
			"llm_judge was constructed without a judge",
			"no judge is available", nil, evalspec.Usage{}, 0), nil
	}

	// The sample identifier travels in the context so the transport recorder
	// can attribute the raw exchange to it.
	ctx = transport.WithCaseID(ctx, call.CaseID)

	prompt := judge.Prompt{System: systemPrompt, User: g.userPrompt(call)}
	if g.structured {
		prompt.ResponseSchema = responseSchema
	}

	completion, err := g.judge.Complete(ctx, prompt)
	if err != nil {
		// A cancelled call is not a failed evaluation. It propagates so the
		// runner can record the sample as skipped, which is what actually
		// happened to it.
		if errors.Is(err, judge.ErrCancelled) {
			return evalspec.Evaluation{}, err
		}

		code, _ := judge.CodeOf(err)

		return evalspec.NewFailEvaluation(code, err.Error(),
			"the judge could not be consulted", nil, evalspec.Usage{}, 0), nil
	}

	return g.interpret(completion), nil
}

// interpret turns the Judge's reply into an evaluation.
func (g *llmJudge) interpret(c judge.Completion) evalspec.Evaluation {
	v, err := parseVerdict(c.Text)
	if err != nil {
		// The Judge answered, but not in the agreed shape. That is a protocol
		// error rather than a judgement: nothing was concluded. The raw text
		// is not copied into the message — it can echo the prompt — and lives
		// in logs/ instead.
		return evalspec.NewFailEvaluation(evalspec.CodeProtocolError, err.Error(),
			"the judge's reply did not match the agreed shape", nil, c.Usage, 0)
	}

	if v.InsufficientEvidence {
		return evalspec.NewFailEvaluation(evalspec.CodeInsufficientEvidence,
			"judge declined to conclude", v.Reason, nil, c.Usage, 0)
	}

	if v.Score == nil && v.Label == nil {
		return evalspec.NewFailEvaluation(evalspec.CodeProtocolError,
			"the judge returned neither a score nor a label",
			v.Reason, nil, c.Usage, 0)
	}

	// The score is recorded exactly as given. min_score and max_score are
	// scale metadata that EvalExec passes through: clamping a score to them,
	// or refusing one outside them, would be EvalExec deciding what a result
	// means — which it does not do.
	return evalspec.NewSuccessEvaluation(v.Score, v.Label, v.Reason, v.evidence(), c.Usage, 0)
}

func (v *verdict) evidence() []evalspec.Evidence {
	out := make([]evalspec.Evidence, 0, len(v.Evidence))
	for _, e := range v.Evidence {
		out = append(out, evalspec.Evidence{Source: e.Source, Path: e.Path, Value: e.Value})
	}

	return out
}

// ErrNoJSONObject reports a reply with no JSON object in it at all.
var ErrNoJSONObject = errors.New("the reply contains no JSON object")

// parseVerdict decodes the Judge's reply, tolerating the two ways a model
// commonly wraps JSON it was asked to emit bare.
//
// Code fences and surrounding prose are unwrapped, because they are near
// universal and cost nothing to handle. Malformed JSON — single quotes,
// trailing commas — is not repaired: that is the model failing to follow the
// contract, and quietly fixing it would hide a Judge that needs a better
// prompt behind results that look fine.
func parseVerdict(text string) (*verdict, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil, errors.New("the judge returned an empty reply")
	}

	raw = stripCodeFence(raw)

	obj, err := extractJSONObject(raw)
	if err != nil {
		return nil, err
	}

	var v verdict
	if err := json.Unmarshal([]byte(obj), &v); err != nil {
		return nil, fmt.Errorf("the reply is not valid JSON: %w", err)
	}

	return &v, nil
}

// stripCodeFence removes a surrounding ```json ... ``` wrapper.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}

	// Drop the opening fence and its optional language tag.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}

	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}

	return strings.TrimSpace(s)
}

// extractJSONObject finds the outermost JSON object in a reply that may carry
// explanatory prose around it.
func extractJSONObject(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", ErrNoJSONObject
	}

	depth, inString, escaped := 0, false, false

	for i := start; i < len(s); i++ {
		c := s[i]

		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// Braces inside a string literal are not structure.
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}

	return "", ErrNoJSONObject
}

const systemPrompt = `You grade one agent session against a rubric.

Reply with a single JSON object and nothing else:
{"score": <number or null>, "label": <string or null>, "reason": <string>,
 "insufficient_evidence": <boolean>, "evidence": [{"source": <string>, "path": <string>, "value": <any>}]}

Rules:
- Judge only what the provided fields show. Do not assume anything not present.
- If the provided fields do not let you reach a conclusion, set
  "insufficient_evidence": true and explain why in "reason". Do not guess a
  score in that case — a refusal is more useful than an invented number.
- Cite what you relied on in "evidence".`

// userPrompt assembles the sample.
//
// Fields are wrapped in tags rather than nested in JSON: it costs fewer tokens,
// and a session whose content happens to contain JSON cannot be mistaken for
// the envelope around it.
func (g *llmJudge) userPrompt(call evalspec.GradeCall) string {
	var b strings.Builder

	b.WriteString("<rubric>\n")
	b.WriteString(g.rubric)
	b.WriteString("\n</rubric>\n\n")

	writeField(&b, "input", call.Input)
	writeField(&b, "output", call.Output)

	// Only the fields this Grader declared it requires are included. Sending
	// more would inflate the prompt and let the Judge rely on evidence the
	// pre-check never guaranteed would be there.
	if g.useTrajectory {
		writeField(&b, "trajectory", call.Trajectory)
	}

	if g.useReference {
		writeField(&b, "reference", call.Reference)
	}

	return b.String()
}

func writeField(b *strings.Builder, name string, raw json.RawMessage) {
	b.WriteString("<")
	b.WriteString(name)
	b.WriteString(">\n")

	if len(raw) == 0 {
		b.WriteString("(absent)")
	} else {
		b.Write(raw)
	}

	b.WriteString("\n</")
	b.WriteString(name)
	b.WriteString(">\n\n")
}

func init() {
	grader.Register(grader.EntryLLMJudge, newLLMJudgeFromDeps)
}

// newLLMJudgeFromDeps is the registry factory. The Judge arrives through Deps
// because it depends on the run's judge_model configuration, which a Grader
// specification alone does not carry.
func newLLMJudgeFromDeps(spec evalspec.GraderSpec, deps grader.Deps) (grader.Grader, error) {
	return NewLLMJudge(spec, deps.Judge)
}
