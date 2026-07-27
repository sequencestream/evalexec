// Package transport records the raw Judge exchange for diagnosis.
//
// aimodel offers no interception point for non-streaming calls, so the
// recording happens one level down, in an http.RoundTripper wrapped around the
// client's transport.
//
// Two rules shape what is kept. Credentials never reach a log: the
// Authorization header is replaced before anything is written. And exchanges
// are buffered rather than written as they happen, because whether a sample is
// worth keeping is only known once its evaluation finishes — a successful run
// of ten thousand samples should not leave ten thousand prompt echoes on disk.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise.
package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// redactedAuthorization replaces a credential in a recorded header.
const redactedAuthorization = "Bearer ***"

// maxBodyBytes caps how much of a request or response body is retained, so one
// enormous exchange cannot exhaust memory.
const maxBodyBytes = 256 << 10

// Exchange is one recorded request and response.
type Exchange struct {
	CaseID     string `json:"case_id,omitempty"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
	DurationMS int64  `json:"duration_ms"`
	// RequestBody and ResponseBody are truncated at maxBodyBytes.
	RequestBody  string `json:"request_body,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
	// Error is set when the transport itself failed, leaving no response.
	Error string `json:"error,omitempty"`
	// Headers holds the request headers with the credential already replaced.
	Headers map[string]string `json:"headers,omitempty"`
}

// caseIDKey is the context key carrying the sample identifier.
type caseIDKey struct{}

// WithCaseID tags a context with the sample a call belongs to.
//
// The identifier travels in the context because a RoundTripper works at the
// HTTP layer and has no other way to learn which sample it is serving. Putting
// it on the Judge interface instead would widen an extension point for the
// sake of diagnostics.
func WithCaseID(ctx context.Context, caseID string) context.Context {
	return context.WithValue(ctx, caseIDKey{}, caseID)
}

// CaseIDFrom reads the sample identifier from a context.
func CaseIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(caseIDKey{}).(string)

	return id
}

// Recorder collects exchanges, keyed by sample.
type Recorder struct {
	mu        sync.Mutex
	exchanges map[string][]Exchange
	enabled   bool
}

// NewRecorder returns an enabled recorder.
func NewRecorder() *Recorder {
	return &Recorder{exchanges: make(map[string][]Exchange), enabled: true}
}

// SetEnabled turns recording on or off.
func (r *Recorder) SetEnabled(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.enabled = on
}

// Wrap returns a RoundTripper that records everything passing through next.
func (r *Recorder) Wrap(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}

	return &recordingTransport{next: next, recorder: r}
}

// Take removes and returns the exchanges recorded for a sample.
//
// Taking rather than reading is what bounds memory: a caller decides whether
// to keep an exchange once the sample's verdict is known, and either way the
// buffer is released.
func (r *Recorder) Take(caseID string) []Exchange {
	r.mu.Lock()
	defer r.mu.Unlock()

	ex := r.exchanges[caseID]
	delete(r.exchanges, caseID)

	return ex
}

// Discard drops the exchanges recorded for a sample.
func (r *Recorder) Discard(caseID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.exchanges, caseID)
}

func (r *Recorder) add(ex Exchange) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.enabled {
		return
	}

	r.exchanges[ex.CaseID] = append(r.exchanges[ex.CaseID], ex)
}

type recordingTransport struct {
	next     http.RoundTripper
	recorder *Recorder
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ex := Exchange{
		CaseID:  CaseIDFrom(req.Context()),
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: redactHeaders(req.Header),
	}

	body, reqBody := captureRequestBody(req)
	ex.RequestBody = body
	req.Body = reqBody

	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	ex.DurationMS = time.Since(start).Milliseconds()

	if err != nil {
		ex.Error = err.Error()
		t.recorder.add(ex)

		return nil, err
	}

	ex.StatusCode = resp.StatusCode
	ex.ResponseBody, resp.Body = captureResponseBody(resp)

	t.recorder.add(ex)

	return resp, nil
}

// redactHeaders copies the headers, replacing the credential.
//
// This happens before anything is stored, not before it is written out. A
// secret that reaches a buffer has already escaped the one place it was
// supposed to live.
func redactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))

	for name, values := range h {
		if len(values) == 0 {
			continue
		}

		if http.CanonicalHeaderKey(name) == "Authorization" {
			out[name] = redactedAuthorization

			continue
		}

		// Anthropic sends its key in a dedicated header rather than in
		// Authorization.
		if http.CanonicalHeaderKey(name) == "X-Api-Key" {
			out[name] = "***"

			continue
		}

		out[name] = values[0]
	}

	return out
}

// captureRequestBody reads the body for recording and returns a replacement
// the transport can still send.
func captureRequestBody(req *http.Request) (string, io.ReadCloser) {
	if req.Body == nil {
		return "", nil
	}

	data, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes))
	_ = req.Body.Close()

	if err != nil {
		return "", io.NopCloser(bytes.NewReader(data))
	}

	return string(data), io.NopCloser(bytes.NewReader(data))
}

// captureResponseBody does the same for the response.
func captureResponseBody(resp *http.Response) (string, io.ReadCloser) {
	if resp.Body == nil {
		return "", nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	_ = resp.Body.Close()

	if err != nil {
		return "", io.NopCloser(bytes.NewReader(data))
	}

	return string(data), io.NopCloser(bytes.NewReader(data))
}
