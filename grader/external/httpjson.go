package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/grader/declaration"
)

// maxResponseBytes caps an external Grader's response at 32 MB, matching the
// dataset line limit: an Evaluation carrying evidence is not small.
const maxResponseBytes = 32 << 20

// HTTPJSON grades by posting the normalized call to a remote service.
type HTTPJSON struct {
	endpoint string
	requires []evalspec.SessionField
	spec     evalspec.GraderSpec
	client   *http.Client
}

// NewHTTPJSON builds a Grader that posts to spec.Entry.
func NewHTTPJSON(spec evalspec.GraderSpec, concurrency int) (grader.Grader, error) {
	if spec.Entry == "" {
		return nil, errors.New("http-json grader: entry must be the endpoint URL")
	}

	if concurrency < 1 {
		concurrency = 1
	}

	// The same connection-pool reasoning as for the Judge: Go's default of two
	// idle connections per host stops serving above two concurrent samples, and
	// every call then pays for a fresh handshake.
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("http-json grader: unexpected default transport")
	}

	pooled := tr.Clone()
	pooled.MaxIdleConns = concurrency * 2
	pooled.MaxIdleConnsPerHost = concurrency
	pooled.MaxConnsPerHost = concurrency

	return &HTTPJSON{
		endpoint: spec.Entry,
		requires: spec.Requires,
		spec:     declarationFrom(spec),
		client:   &http.Client{Transport: pooled},
	}, nil
}

// Declare reports what the configuration says this Grader needs.
func (g *HTTPJSON) Declare() grader.Declaration {
	return declaration.Declaration{
		Entry:         g.endpoint,
		Requires:      g.requires,
		RequiresJudge: g.spec.RequiresJudge,
	}
}

// Grade posts one call and interprets the reply.
func (g *HTTPJSON) Grade(ctx context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	body, err := json.Marshal(call)
	if err != nil {
		return evalspec.Evaluation{}, fmt.Errorf("http-json grader: cannot serialize the call: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(body))
	if err != nil {
		return evalspec.Evaluation{}, fmt.Errorf("http-json grader: cannot build the request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	start := time.Now()

	resp, err := g.client.Do(req)
	if err != nil {
		// Cancellation propagates so the sample is recorded as skipped rather
		// than as a Grader that misbehaved.
		if errors.Is(err, context.Canceled) {
			return evalspec.Evaluation{}, err
		}

		if errors.Is(err, context.DeadlineExceeded) {
			return timeoutFailure("the grader did not answer in time"), nil
		}

		return protocolFailure("the grader could not be reached", err), nil
	}

	defer func() { _ = resp.Body.Close() }()

	eval, err := g.interpret(resp)
	if err != nil {
		return protocolFailure("the grader's response could not be used", err), nil
	}

	eval.LatencyMS = time.Since(start).Milliseconds()

	return eval, nil
}

func (g *HTTPJSON) interpret(resp *http.Response) (evalspec.Evaluation, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return evalspec.Evaluation{}, fmt.Errorf("cannot read the response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status is reported; the body is not. A Grader's error page can
		// echo the whole call back, and a result document is not the place for
		// that — errors.jsonl and logs/ are.
		return evalspec.Evaluation{}, fmt.Errorf("the grader returned HTTP %d", resp.StatusCode)
	}

	return decodeEvaluation(data)
}
