// Package redact produces the request snapshot that goes into a result.
//
// Its job is smaller than the name suggests, because the request types have
// nowhere to put a credential: Auth references an environment variable by name
// and holds no value. What this package actually does is refuse to proceed
// when a credential turns up somewhere it should not, and produce the
// canonical serialization that eval_request_sha256 is taken over.
//
// Finding a secret is a refusal, not a redaction. Quietly stripping one would
// tell the user their credential was handled safely, when in fact they still
// have it written in a configuration file on disk — the leak already happened
// and hiding it from the snapshot only removes the evidence.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise.
package redact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
)

// StepRedact is the step name reported when a credential is found.
const StepRedact = "arguments"

// secretPatterns match credential shapes rather than suggestive words. A bare
// substring search for "sk-" flags the "--task-id" flag, and a check that
// cries wolf is a check that gets switched off.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(sk|pk|api)-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._-]{16,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
}

// Snapshot is the redacted, canonicalized request together with its digest.
type Snapshot struct {
	// JSON is the canonical serialization: keys sorted at every level, no
	// whitespace, HTML escaping off.
	JSON json.RawMessage
	// SHA256 is the hex digest of JSON.
	SHA256 string
}

// Request builds the snapshot recorded in a result.
//
// The digest is taken over the canonical form so that two runs of the same
// request agree on it regardless of how the JSON happened to be written —
// which is what makes it usable for comparing runs at all.
func Request(req *evalspec.EvalRequest) (*Snapshot, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, evalerr.Wrap(evalerr.KindArgument, StepRedact, err, "cannot serialize the request")
	}

	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, evalerr.Wrap(evalerr.KindArgument, StepRedact, err, "cannot re-read the request")
	}

	if err := scan("$", generic); err != nil {
		return nil, err
	}

	canonical, err := Canonical(generic)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(canonical)

	return &Snapshot{JSON: canonical, SHA256: hex.EncodeToString(sum[:])}, nil
}

// Canonical serializes a decoded JSON value with keys sorted at every level
// and no insignificant whitespace.
//
// HTML escaping is disabled because encoding/json rewrites <, > and & into
// <-style escapes by default. That is a browser-safety measure with no
// place here: it would make the digest depend on characters that happen to
// appear in a rubric, and would corrupt a rubric's text on the way back out.
func Canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case map[string]any:
		buf.WriteByte('{')

		for i, k := range slices.Sorted(maps.Keys(t)) {
			if i > 0 {
				buf.WriteByte(',')
			}

			if err := writeScalar(buf, k); err != nil {
				return err
			}

			buf.WriteByte(':')

			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}

		buf.WriteByte('}')

		return nil
	case []any:
		buf.WriteByte('[')

		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}

			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}

		buf.WriteByte(']')

		return nil
	default:
		return writeScalar(buf, v)
	}
}

func writeScalar(buf *bytes.Buffer, v any) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("redact: encode: %w", err)
	}

	// Encode appends a newline that has no place inside a compact document.
	buf.Truncate(buf.Len() - 1)

	return nil
}

// scan walks the decoded request looking for credential shapes, reporting the
// JSON path of anything it finds.
func scan(path string, v any) error {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			if err := scan(path+"."+k, t[k]); err != nil {
				return err
			}
		}

		return nil
	case []any:
		for i, e := range t {
			if err := scan(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}

		return nil
	case string:
		return scanString(path, t)
	default:
		return nil
	}
}

func scanString(path, s string) error {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return evalerr.Argument(StepRedact,
				"%s looks like it contains a credential; reference it by environment variable name "+
					"in judge_model.auth instead of writing it into the configuration", path)
		}
	}

	return nil
}

// FindSecrets reports credential shapes anywhere in data, for the leak scan
// that runs over a published result directory.
func FindSecrets(data []byte) []string {
	var found []string

	for _, re := range secretPatterns {
		for _, m := range re.FindAll(data, -1) {
			found = append(found, string(m))
		}
	}

	return found
}

// ContainsSentinel reports whether data holds the given literal value. The
// leak scan uses it with the fixture credential: a pattern match proves the
// scanner works, but only an exact search proves this particular secret did
// not get out.
func ContainsSentinel(data []byte, sentinel string) bool {
	if sentinel == "" {
		return false
	}

	return strings.Contains(string(data), sentinel)
}
