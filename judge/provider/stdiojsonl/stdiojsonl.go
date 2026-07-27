// Package stdiojsonl speaks EvalExec's Judge protocol to a subprocess.
//
// The payload is identical to the http-json protocol — one request line, one
// response line — so an implementer writes the same JSON handling either way and
// only the transport differs.
//
// The split of responsibilities is what makes this fit aimodel's provider model
// at all: the provider builds a normal *http.Request against a placeholder URL,
// and an injected RoundTripper does the pipe I/O instead of a network call.
// Everything above — timeouts, usage, error classification — stays shared with
// the HTTP protocols.
//
// # Stability
//
// L3 component. The wire contract is documented in contract/ and versioned with
// the specification.
package stdiojsonl

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/vogo/aimodel/ais"

	"github.com/sequencestream/evalexec/judge/provider/httpjson"
	"github.com/sequencestream/evalexec/subprocess"
)

// Name is the registered provider name.
const Name = "stdio-jsonl"

// placeholderURL stands in for an endpoint the request never reaches over the
// network. It has to parse as a URL because the pipeline builds a real
// *http.Request; nothing dials it.
const placeholderURL = "stdio://local/judge"

func init() {
	ais.Register(Name, New)
}

// Options carries the per-run configuration.
type Options struct {
	// Command is the executable to run.
	Command string
}

// New constructs the provider.
func New(cfg ais.Config) (ais.ChatProvider, error) {
	opts, ok := cfg.Options.(Options)
	if !ok {
		if cfg.Options == nil {
			return nil, errors.New("judge/stdiojsonl: provider options are required")
		}

		return nil, fmt.Errorf("judge/stdiojsonl: unexpected provider options of type %T", cfg.Options)
	}

	command := opts.Command
	if command == "" {
		command = cfg.BaseURL
	}

	if command == "" {
		return nil, errors.New("judge/stdiojsonl: a command is required")
	}

	if err := subprocess.Executable(command); err != nil {
		return nil, err
	}

	// The HTTP provider already knows how to serialize and parse this payload;
	// reusing it is what keeps the two transports from drifting apart.
	inner, err := httpjson.New(ais.Config{
		APIKey:  cfg.APIKey,
		BaseURL: placeholderURL,
		Options: httpjson.Options{Endpoint: placeholderURL},
	})
	if err != nil {
		return nil, err
	}

	return &provider{ChatProvider: inner}, nil
}

// provider delegates everything to the HTTP provider; only the transport
// differs, and that is the RoundTripper's job.
type provider struct {
	ais.ChatProvider
}

// Transport exchanges the request body with a subprocess instead of a server.
type Transport struct {
	pool *subprocess.Pool
}

// NewTransport returns a RoundTripper backed by a pool of subprocesses.
//
// One process per worker: the protocol is one question at a time, so sharing a
// process would interleave conversations and attribute one sample's verdict to
// another.
func NewTransport(command string) *Transport {
	return &Transport{pool: subprocess.NewPool(command)}
}

// RoundTrip sends the request body to a subprocess and returns its reply.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}

	proc, err := t.pool.Acquire()
	if err != nil {
		return nil, err
	}

	// The request context is honoured here rather than ignored. A RoundTripper
	// that does not watch it leaves an interrupt waiting on a subprocess that
	// may never answer — the wind-up would hang exactly when it matters.
	line, callErr := proc.Call(req.Context(), body)

	t.pool.Release(proc, callErr != nil)

	if callErr != nil {
		return nil, fmt.Errorf("judge/stdiojsonl: %w", callErr)
	}

	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(line)),
		ContentLength: int64(len(line)),
		Request:       req,
	}, nil
}

// Close shuts every process down.
func (t *Transport) Close() error { return t.pool.Close() }

// PoolSize reports how many processes were started.
func (t *Transport) PoolSize() int { return t.pool.Size() }

func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, errors.New("judge/stdiojsonl: the request has no body")
	}

	defer func() { _ = req.Body.Close() }()

	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("judge/stdiojsonl: read request body: %w", err)
	}

	return data, nil
}

// compile-time check that the transport satisfies the standard interface.
var _ http.RoundTripper = (*Transport)(nil)
