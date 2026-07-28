// Package dataset reads the agent session rows a run grades.
//
// The dataset is read twice per run, not once: the pre-check phase scans it
// end to end to prove every row parses, carries a unique case_id and satisfies
// the Grader's declared requirements, and only then does execution scan it
// again to actually grade. That is what makes "no Grader or Judge is called
// until the whole dataset validated" true rather than aspirational.
//
// Reading is streamed. Only the case_id index and the running counts are kept
// in memory, so dataset size is bounded by disk rather than by RAM.
//
// # Stability
//
// Internal package: no compatibility promise.
package dataset

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sequencestream/evalexec/evalspec"
)

// maxLineBytes caps a single dataset line at 32 MB. bufio.Scanner would
// otherwise fail with a bare "token too long" on a legitimately large session;
// this way the limit is explicit and reported with the line number.
const maxLineBytes = 32 << 20

// Reader streams sessions from a JSONL file.
type Reader struct {
	f        *os.File
	scanner  *bufio.Scanner
	sequence int
	err      error
}

// Open opens a dataset file for streaming.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("dataset: open %s: %w", path, err)
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	return &Reader{f: f, scanner: sc}, nil
}

// Next returns the next session and its 1-based sequence number. It returns
// io.EOF when the file is exhausted.
//
// Blank lines are skipped rather than counted: a trailing newline is normal in
// a text file and must not become a phantom row that breaks the identity
// between dataset lines and result records.
func (r *Reader) Next() (*evalspec.Session, int, error) {
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}

		r.sequence++

		var s evalspec.Session
		if err := json.Unmarshal(line, &s); err != nil {
			return nil, r.sequence, &ParseError{Line: r.sequence, Err: err}
		}

		return &s, r.sequence, nil
	}

	if err := r.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, r.sequence + 1, &ParseError{
				Line: r.sequence + 1,
				Err:  fmt.Errorf("line exceeds the %d byte limit", maxLineBytes),
			}
		}

		r.err = err

		return nil, r.sequence, fmt.Errorf("dataset: read: %w", err)
	}

	return nil, r.sequence, io.EOF
}

// Count returns how many rows have been returned so far.
func (r *Reader) Count() int { return r.sequence }

// Close releases the underlying file.
func (r *Reader) Close() error {
	if err := r.f.Close(); err != nil {
		return fmt.Errorf("dataset: close: %w", err)
	}

	return nil
}

// trimSpace reports the line with leading and trailing ASCII whitespace
// removed. It avoids a string conversion on the hot path.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}

	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}

	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// ParseError reports a line that could not be decoded, carrying the line
// number so a user can find it in a file with a million rows.
type ParseError struct {
	Line int
	Err  error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// CaseIndex records which case_ids have been seen, so duplicates are caught
// before anything is graded.
//
// It is an interface because a dataset large enough to make an in-memory set
// expensive should be able to swap in a disk-backed index without the
// validator knowing. Only the in-memory implementation exists today.
type CaseIndex interface {
	// Add records an id, returning ErrDuplicateCase if it was already seen.
	Add(id string) error
	// Len returns how many distinct ids were recorded.
	Len() int
}

// ErrDuplicateCase reports a case_id that appeared more than once.
var ErrDuplicateCase = errors.New("duplicate case_id")

// MemoryIndex keeps the case_id set in memory.
type MemoryIndex struct {
	seen map[string]struct{}
}

// NewMemoryIndex returns an empty in-memory index.
func NewMemoryIndex() *MemoryIndex {
	return &MemoryIndex{seen: make(map[string]struct{})}
}

// Add records an id.
func (i *MemoryIndex) Add(id string) error {
	if _, dup := i.seen[id]; dup {
		return fmt.Errorf("%w: %q", ErrDuplicateCase, id)
	}

	i.seen[id] = struct{}{}

	return nil
}

// Len returns the number of distinct ids.
func (i *MemoryIndex) Len() int { return len(i.seen) }
