// Package judge turns a judge_model configuration into a single chat
// completion capability, so that a Grader needing an LLM Judge never sees the
// protocol it is talking to.
//
// It is deliberately the only package in this module that imports
// github.com/vogo/aimodel. Every protocol EvalExec supports resolves here down
// to one Judge, which also confines the blast radius of an aimodel upgrade to
// this package — not a hypothetical concern: aimodel restructured its whole
// API between v0.4.1 and v0.5.0.
//
// # Stability
//
// L2 extension point. From v1.0 it follows the Go compatibility promise.
// Adding a method to the Judge interface is a breaking change, so the
// interface is kept to a single method by design.
package judge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/aimodel/ais"
	"github.com/vogo/aimodel/provider/anthropic"
	"github.com/vogo/aimodel/provider/openai"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/judge/provider/httpjson"
	"github.com/sequencestream/evalexec/judge/provider/stdiojsonl"
	"github.com/sequencestream/evalexec/judge/transport"
)

// Prompt is one question for the Judge.
//
// It is EvalExec's own type rather than aimodel's, so that no Grader ever
// sees a vendor type. That keeps the blast radius of an upstream change to
// this package — which matters: aimodel restructured its entire API between
// v0.4.1 and v0.5.0.
type Prompt struct {
	// System frames the task; User carries the sample.
	System string
	User   string
	// ResponseSchema, when set, asks the provider for structured output
	// matching this JSON Schema. Providers that cannot honour it are expected
	// to ignore it, which is why the prompt also states the required shape.
	ResponseSchema map[string]any
}

// Completion is one answer.
type Completion struct {
	Text  string
	Usage evalspec.Usage
}

// Judge answers one prompt.
//
// One method, deliberately. This is an L2 extension point: from v1.0, adding a
// method here would be a breaking change, so future capability has to arrive
// as fields on Prompt or Completion instead.
type Judge interface {
	Complete(ctx context.Context, p Prompt) (Completion, error)
}

// ErrCancelled reports a call abandoned because the run is stopping.
//
// It is distinct from a timeout on purpose, and the distinction is the single
// easiest thing to get wrong here. A timed-out sample was processed and its
// evaluation failed; a cancelled sample was never finished at all and must be
// recorded as skipped. Conflating them would report work that never happened
// as work that happened badly.
//
// It wraps context.Canceled so that a caller which only knows about the
// standard library — the runner, which must not import this package and pull
// the vendor SDK along with it — can recognize a cancellation with a single
// errors.Is check.
var ErrCancelled = fmt.Errorf("judge: the call was cancelled because the run is stopping: %w", context.Canceled)

// ErrUnsupportedProtocol reports a Judge protocol this build cannot reach.
var ErrUnsupportedProtocol = errors.New("judge: unsupported protocol")

// supportedParameters is the complete set of judge_model.parameters keys.
//
// It is an allow list because the specification requires an unknown key to be
// an error rather than a silent drop: a temperature misspelled as
// "temperatur" that is quietly ignored produces a run which looks fine and
// judged with the wrong settings.
//
// The set is exactly what the canonical chat request models. aimodel v0.5.0
// narrowed its canonical fields to those at least two providers share, which
// is why seed, n, frequency_penalty and the rest are absent — they would have
// nowhere to go.
var supportedParameters = []string{
	"model", "temperature", "max_completion_tokens", "max_tokens",
	"top_p", "top_k", "stop", "reasoning_effort", "parallel_tool_calls",
	"response_format",
}

// client is the default Judge, backed by an aimodel client.
type client struct {
	completer aimodel.ChatCompleter
	model     string
	timeout   time.Duration
	params    parameters
	recorder  *transport.Recorder
	closer    io.Closer
}

// Close releases any resources the transport holds, such as the subprocesses of
// a stdio-jsonl Judge.
func (c *client) Close() error {
	if c.closer == nil {
		return nil
	}

	return c.closer.Close()
}

// New builds a Judge from its configuration.
//
// Construction validates everything it can: the provider, the endpoint, the
// credential and every parameter. That is why it is also wired into the
// pre-check phase — a Judge configuration that cannot work should fail before
// the first sample, not on it.
func New(cfg *evalspec.JudgeModelSpec, concurrency int) (Judge, error) {
	if cfg == nil {
		return nil, errors.New("judge: no judge_model was configured")
	}

	provider, err := providerName(cfg.Protocol)
	if err != nil {
		return nil, err
	}

	params, err := parseParameters(cfg.Parameters)
	if err != nil {
		return nil, err
	}

	if params.model == "" {
		return nil, errors.New(`judge: parameter "model" is required`)
	}

	apiKey, err := credential(cfg.Auth)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	recorder := transport.NewRecorder()

	httpClient, providerOpts, err := transportFor(cfg, provider, concurrency, recorder)
	if err != nil {
		return nil, err
	}

	completer, err := aimodel.NewClient(
		aimodel.WithProvider(provider),
		aimodel.WithProviderOptions(providerOpts),
		// Everything is passed explicitly. aimodel falls back to AI_API_KEY,
		// OPENAI_BASE_URL and friends when a value is missing, and a run whose
		// provenance names one endpoint while actually calling another is
		// worse than a run that failed.
		aimodel.WithAPIKey(apiKey),
		aimodel.WithBaseURL(cfg.Endpoint),
		aimodel.WithHTTPClient(httpClient),
		// A client-level backstop at twice the per-call budget. The real
		// per-call bound is a context deadline; this only catches a transport
		// that never returns.
		aimodel.WithTimeout(2*timeout),
	)
	if err != nil {
		return nil, fmt.Errorf("judge: cannot configure the %s provider: %w", provider, err)
	}

	return &client{
		completer: completer,
		model:     params.model,
		timeout:   timeout,
		params:    params,
		recorder:  recorder,
		closer:    closerOf(httpClient),
	}, nil
}

// transportFor builds the HTTP client and the provider options for a protocol.
//
// The two custom protocols need per-run configuration — an endpoint, a command —
// and it travels through aimodel's provider options rather than through the
// registration, because ais.Register panics on a duplicate name: each provider
// is registered exactly once, at init.
func transportFor(
	cfg *evalspec.JudgeModelSpec,
	provider string,
	concurrency int,
	recorder *transport.Recorder,
) (*http.Client, any, error) {
	switch provider {
	case httpjson.Name:
		key, err := credential(cfg.Auth)
		if err != nil {
			return nil, nil, err
		}

		return newHTTPClient(concurrency, recorder),
			httpjson.Options{Endpoint: cfg.Endpoint, APIKey: key}, nil
	case stdiojsonl.Name:
		// The subprocess transport replaces the network entirely; the recorder
		// still wraps it, so a stdio Judge's exchanges land in logs/ like any
		// other.
		st := stdiojsonl.NewTransport(cfg.Endpoint)

		return &http.Client{Transport: recorder.Wrap(st)},
			stdiojsonl.Options{Command: cfg.Endpoint}, nil
	default:
		// The built-in providers take no options, and passing any would be
		// rejected by their factories.
		return newHTTPClient(concurrency, recorder), nil, nil
	}
}

// closerOf finds a transport that owns resources, so the Judge can release
// them when the run ends.
func closerOf(c *http.Client) io.Closer {
	type closerTransport interface {
		http.RoundTripper
		io.Closer
	}

	var walk func(rt http.RoundTripper) io.Closer

	walk = func(rt http.RoundTripper) io.Closer {
		switch t := rt.(type) {
		case nil:
			return nil
		case closerTransport:
			return t
		case interface{ Unwrap() http.RoundTripper }:
			return walk(t.Unwrap())
		default:
			return nil
		}
	}

	return walk(c.Transport)
}

// providerName maps a protocol to a registered aimodel provider.
func providerName(p evalspec.JudgeProtocol) (string, error) {
	switch p {
	case evalspec.JudgeOpenAIChat:
		return openai.Name, nil
	case evalspec.JudgeAnthropicMessages:
		// Registered by importing the root aimodel package, which client.go
		// does with a blank import of this provider.
		return anthropic.Name, nil
	case evalspec.JudgeHTTPJSON:
		return httpjson.Name, nil
	case evalspec.JudgeStdioJSONL:
		return stdiojsonl.Name, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedProtocol, p)
	}
}

// placeholderKey stands in for a credential when the endpoint needs none.
//
// aimodel rejects an empty API key outright, so a local unauthenticated Judge
// would otherwise be unreachable. The value is deliberately meaningless.
const placeholderKey = "-"

// credential resolves the API key. The environment variable is read here, and
// an empty one is an error rather than a fallback — the pre-check has already
// checked it, and reaching this point with an empty value would mean the
// environment changed mid-run.
func credential(auth evalspec.Auth) (string, error) {
	switch auth.Type {
	case evalspec.AuthNone:
		return placeholderKey, nil
	case evalspec.AuthBearerEnv:
		key := getenv(auth.Env)
		if key == "" {
			return "", fmt.Errorf("judge: environment variable %s is empty", auth.Env)
		}

		return key, nil
	default:
		return "", fmt.Errorf("judge: unsupported auth type %q", auth.Type)
	}
}

// newHTTPClient builds the transport, tuned for the run's concurrency.
//
// A dedicated client per Judge, never a shared one: aimodel.NewClient sets
// Timeout on whatever client it is handed, so two Judges sharing one would
// silently overwrite each other's deadline.
//
// The default MaxIdleConnsPerHost of 2 is the reason for the tuning. Above two
// concurrent samples the pool stops serving and every call negotiates a fresh
// TLS connection, which becomes the dominant cost.
func newHTTPClient(concurrency int, recorder *transport.Recorder) *http.Client {
	if concurrency < 1 {
		concurrency = 1
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: recorder.Wrap(http.DefaultTransport)}
	}

	tr := base.Clone()
	tr.MaxIdleConns = concurrency * 2
	tr.MaxIdleConnsPerHost = concurrency
	tr.MaxConnsPerHost = concurrency

	return &http.Client{Transport: recorder.Wrap(tr)}
}

// Complete asks the Judge one question.
func (c *client) Complete(ctx context.Context, p Prompt) (Completion, error) {
	// The per-call bound. aimodel's own timeout is a client-level backstop and
	// cannot express "this one call has this long".
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := c.buildRequest(p)

	resp, err := c.completer.ChatCompletion(callCtx, req)
	if err != nil {
		return Completion{}, Classify(err)
	}

	if len(resp.Choices) == 0 {
		return Completion{}, fmt.Errorf("judge: %w", ais.ErrEmptyResponse)
	}

	return Completion{
		Text:  resp.Choices[0].Message.Content.Text(),
		Usage: mapUsage(resp.Usage),
	}, nil
}

// Recorder exposes the transport recorder, so the runner can attach the raw
// exchange to a failing sample without this package knowing about result
// directories.
func (c *client) Recorder() *transport.Recorder { return c.recorder }

// buildRequest assembles the chat request from the prompt and the configured
// parameters.
func (c *client) buildRequest(p Prompt) *ais.ChatRequest {
	messages := make([]ais.Message, 0, 2)

	if p.System != "" {
		messages = append(messages, ais.Message{
			Role: ais.RoleSystem, Content: ais.NewTextContent(p.System),
		})
	}

	messages = append(messages, ais.Message{
		Role: ais.RoleUser, Content: ais.NewTextContent(p.User),
	})

	// Model is always set explicitly rather than left to the client default,
	// so aimodel's AI_MODEL fallback can never take effect.
	req := &ais.ChatRequest{Model: c.model, Messages: messages}

	c.params.applyTo(req)

	if p.ResponseSchema != nil && req.ResponseFormat == nil {
		req.ResponseFormat = jsonSchemaFormat(p.ResponseSchema)
	}

	return req
}

// jsonSchemaFormat builds the OpenAI structured-output request shape.
func jsonSchemaFormat(schema map[string]any) any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "evaluation",
			"strict": false,
			"schema": schema,
		},
	}
}

// mapUsage converts aimodel's token counters to EvalExec's.
//
// Cache reads and reasoning tokens are carried across because a reasoning
// Judge routinely spends more tokens thinking than answering: dropping them
// would leave the run's usage disagreeing with the bill.
func mapUsage(u ais.Usage) evalspec.Usage {
	return evalspec.Usage{
		JudgeInputTokens:     u.PromptTokens,
		JudgeOutputTokens:    u.CompletionTokens,
		JudgeCacheReadTokens: u.CacheReadTokens,
		JudgeReasoningTokens: u.ReasoningTokens,
	}
}

// Classify turns a transport or provider error into the kind of failure
// EvalExec records.
//
// The order matters. Cancellation is checked first and with errors.Is rather
// than by reading ctx.Err(), because a cancelled call is not a failed
// evaluation at all — the sample belongs to the run's stop path and is
// recorded as skipped.
func Classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return ErrCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: evalspec.CodeTimeout, Message: "the judge call exceeded its timeout", Err: err}
	}

	var apiErr *ais.APIError
	if errors.As(err, &apiErr) {
		// The status and provider code go into the message; the response body
		// does not. A body can echo the prompt back, and a result document is
		// not the place for that — the raw exchange belongs in logs/.
		msg := fmt.Sprintf("judge returned HTTP %d", apiErr.StatusCode)
		if apiErr.Code != "" {
			msg += " (" + apiErr.Code + ")"
		}

		return &Error{Code: evalspec.CodeJudgeError, Message: msg, Err: err}
	}

	if errors.Is(err, ais.ErrEmptyResponse) {
		return &Error{Code: evalspec.CodeJudgeError, Message: "judge returned no choices", Err: err}
	}

	return &Error{Code: evalspec.CodeJudgeError, Message: "judge call failed: " + err.Error(), Err: err}
}

// Error is a classified Judge failure, carrying the code that will appear in
// the record.
type Error struct {
	Code    evalspec.ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the error code to record for err, and whether err classifies
// as a failed evaluation at all. A cancelled call does not.
func CodeOf(err error) (evalspec.ErrorCode, bool) {
	if errors.Is(err, ErrCancelled) {
		return "", false
	}

	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}

	return evalspec.CodeJudgeError, true
}

// Checker adapts New into the pre-check contract, so an unusable Judge
// configuration fails before the run starts.
type Checker struct {
	Concurrency int
}

// Check reports whether the configuration yields a usable Judge.
func (c Checker) Check(spec *evalspec.JudgeModelSpec) error {
	_, err := New(spec, c.Concurrency)

	return err
}

// SupportedParameters returns the accepted judge_model.parameters keys.
func SupportedParameters() []string { return slices.Clone(supportedParameters) }

// parameters holds the parsed judge_model.parameters.
type parameters struct {
	model             string
	temperature       *float64
	maxCompletion     *int
	maxTokens         *int
	topP              *float64
	topK              *int
	stop              []string
	reasoningEffort   string
	parallelToolCalls *bool
	responseFormat    any
}

func (p parameters) applyTo(req *ais.ChatRequest) {
	req.Temperature = p.temperature
	req.MaxCompletionTokens = p.maxCompletion
	// MaxTokens is deprecated upstream and rejected by reasoning models, but a
	// caller pointing at an older endpoint may still need it. Supporting it is
	// the point; the parameter table documents which one to prefer.
	req.MaxTokens = p.maxTokens //nolint:staticcheck // deliberately supported for older models
	req.TopP = p.topP
	req.TopK = p.topK
	req.Stop = p.stop
	req.ReasoningEffort = p.reasoningEffort
	req.ParallelToolCalls = p.parallelToolCalls
	req.ResponseFormat = p.responseFormat
}

// parseParameters validates and converts the configured parameters.
func parseParameters(in map[string]any) (parameters, error) {
	var out parameters

	known := make(map[string]bool, len(supportedParameters))
	for _, k := range supportedParameters {
		known[k] = true
	}

	for _, k := range slices.Sorted(maps.Keys(in)) {
		if !known[k] {
			return parameters{}, fmt.Errorf(
				"judge: unknown parameter %q; judge_model.parameters accepts %v", k, supportedParameters)
		}
	}

	var err error

	if out.model, err = stringOf(in, "model"); err != nil {
		return parameters{}, err
	}

	if out.temperature, err = floatOf(in, "temperature"); err != nil {
		return parameters{}, err
	}

	if out.maxCompletion, err = intOf(in, "max_completion_tokens"); err != nil {
		return parameters{}, err
	}

	if out.maxTokens, err = intOf(in, "max_tokens"); err != nil {
		return parameters{}, err
	}

	if out.topP, err = floatOf(in, "top_p"); err != nil {
		return parameters{}, err
	}

	if out.topK, err = intOf(in, "top_k"); err != nil {
		return parameters{}, err
	}

	if out.stop, err = stringsOf(in, "stop"); err != nil {
		return parameters{}, err
	}

	if out.reasoningEffort, err = stringOf(in, "reasoning_effort"); err != nil {
		return parameters{}, err
	}

	if out.parallelToolCalls, err = boolOf(in, "parallel_tool_calls"); err != nil {
		return parameters{}, err
	}

	out.responseFormat = in["response_format"]

	return out, nil
}
