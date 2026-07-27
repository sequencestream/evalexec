// Package httpjson speaks EvalExec's own Judge protocol over HTTP.
//
// It is an aimodel provider, so a Judge reached this way is indistinguishable
// from an OpenAI-compatible one everywhere above the transport: the same
// timeout handling, the same usage accounting, the same error classification.
//
// The wire format is deliberately simpler than any vendor's — one reply, a flat
// usage object — because its purpose is to be easy to implement in another
// language, not to be compatible with a particular service. The usage field
// names match EvalResult.usage.judge_model, so an implementer has one fewer
// translation to keep straight.
//
// # Stability
//
// L3 component. The wire contract is documented in contract/ and versioned with
// the specification.
package httpjson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/vogo/aimodel/ais"
)

// Name is the registered provider name.
const Name = "http-json"

func init() {
	ais.Register(Name, New)
}

// Options carries the per-run configuration.
//
// It travels through aimodel's provider options channel rather than being baked
// into the registration, because ais.Register panics on a duplicate name: a
// provider is registered once, and each run's endpoint arrives at construction.
type Options struct {
	// Endpoint is the URL to post to.
	Endpoint string
	// APIKey, when non-empty, is sent as a bearer token.
	APIKey string
}

// New constructs the provider.
func New(cfg ais.Config) (ais.ChatProvider, error) {
	opts, ok := cfg.Options.(Options)
	if !ok {
		if cfg.Options == nil {
			return nil, errors.New("judge/httpjson: provider options are required")
		}

		return nil, fmt.Errorf("judge/httpjson: unexpected provider options of type %T", cfg.Options)
	}

	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = cfg.BaseURL
	}

	if endpoint == "" {
		return nil, ais.ErrNoBaseURL
	}

	key := opts.APIKey
	if key == "" {
		key = cfg.APIKey
	}

	return &provider{endpoint: endpoint, apiKey: key}, nil
}

type provider struct {
	endpoint string
	apiKey   string
}

// Request is the body EvalExec posts to an http-json Judge.
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	// Temperature and the rest are omitted when unset, so an implementation
	// only has to handle what it was actually sent.
	Temperature         *float64 `json:"temperature,omitempty"`
	MaxCompletionTokens *int     `json:"max_completion_tokens,omitempty"`
	TopP                *float64 `json:"top_p,omitempty"`
	TopK                *int     `json:"top_k,omitempty"`
	Stop                []string `json:"stop,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
	ResponseFormat      any      `json:"response_format,omitempty"`
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Response is the body an http-json Judge returns.
type Response struct {
	// Content is the reply text. A Judge returns one answer, not a list of
	// candidates: EvalExec has no use for alternatives.
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
	// Error, when set, reports a failure in the Judge's own terms.
	Error *Error `json:"error,omitempty"`
}

// Usage counts tokens, using the same field names as the result document.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Error is a Judge-reported failure.
type Error struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// NewChatRequest serializes the canonical request into the http-json body.
func (p *provider) NewChatRequest(ctx context.Context, req *ais.ChatRequest) (*http.Request, error) {
	body, err := json.Marshal(toRequest(req))
	if err != nil {
		return nil, fmt.Errorf("judge/httpjson: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("judge/httpjson: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// The placeholder key used for unauthenticated endpoints is not sent: a
	// header saying "-" would be more confusing than no header.
	if p.apiKey != "" && p.apiKey != "-" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	return httpReq, nil
}

func toRequest(req *ais.ChatRequest) Request {
	out := Request{
		Model:               req.Model,
		Temperature:         req.Temperature,
		MaxCompletionTokens: req.MaxCompletionTokens,
		TopP:                req.TopP,
		TopK:                req.TopK,
		Stop:                req.Stop,
		ReasoningEffort:     req.ReasoningEffort,
		ResponseFormat:      req.ResponseFormat,
	}

	out.Messages = make([]Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, Message{Role: string(m.Role), Content: m.Content.Text()})
	}

	return out
}

// ParseChatResponse converts an http-json reply into the canonical shape.
func (p *provider) ParseChatResponse(body io.Reader) (*ais.ChatResponse, error) {
	var resp Response
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("judge/httpjson: decode response: %w", err)
	}

	if resp.Error != nil {
		return nil, &ais.APIError{
			StatusCode: http.StatusOK,
			Code:       resp.Error.Code,
			Message:    resp.Error.Message,
		}
	}

	if resp.Content == "" {
		return nil, ais.ErrEmptyResponse
	}

	return &ais.ChatResponse{
		Choices: []ais.Choice{{
			Index:   0,
			Message: ais.Message{Role: ais.RoleAssistant, Content: ais.NewTextContent(resp.Content)},
		}},
		Usage: ais.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			ReasoningTokens:  resp.Usage.ReasoningTokens,
		},
	}, nil
}

// ParseErrorResponse converts a non-2xx reply into an error.
func (p *provider) ParseErrorResponse(statusCode int, body []byte) error {
	apiErr := &ais.APIError{StatusCode: statusCode}

	var resp Response
	if err := json.Unmarshal(body, &resp); err == nil && resp.Error != nil {
		apiErr.Code = resp.Error.Code
		apiErr.Message = resp.Error.Message
	}

	return apiErr
}

// NewStreamDecoder returns a decoder that reports the stream as finished.
// EvalExec never streams: a Judge verdict is read whole or not at all.
func (p *provider) NewStreamDecoder(io.Reader) ais.StreamDecoder {
	return exhaustedDecoder{}
}

type exhaustedDecoder struct{}

func (exhaustedDecoder) Next() (*ais.StreamChunk, error) { return nil, io.EOF }
