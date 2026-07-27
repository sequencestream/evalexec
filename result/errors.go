package result

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/sequencestream/evalexec/evalspec"
)

// ErrorLog records run-level diagnostics that would otherwise be lost.
//
// It deliberately records less than it could. A subprocess crash already
// surfaces in evaluation.error.message and its stderr in logs/; a connection
// failure likewise. Repeating those here would produce a file that mostly
// duplicates the record stream, and a diagnostic nobody reads is worse than no
// diagnostic at all.
//
// What it records is the events with nowhere else to go — a log that could not
// be written, a dataset that changed underneath a run. In library mode the
// diagnostic writer defaults to io.Discard, so without this those events vanish
// entirely.
//
// The file is not covered by checksums.sha256: it is optional and may be absent
// or truncated, and including it would make the checksum file unverifiable for
// exactly the runs that most need diagnosing.
type ErrorLog struct {
	dir     *Dir
	mu      sync.Mutex
	entries []Entry
}

// Entry is one run-level diagnostic.
type Entry struct {
	// At is when it happened.
	At evalspec.Timestamp `json:"at"`
	// Stage names where in the run it happened, e.g. "logs" or "backfill".
	Stage string `json:"stage"`
	// Message is the human-readable detail.
	Message string `json:"message"`
	// CaseID is set when the event belongs to one sample.
	CaseID string `json:"case_id,omitempty"`
}

// NewErrorLog returns a log targeting the pending result directory.
func NewErrorLog(dir *Dir) *ErrorLog {
	return &ErrorLog{dir: dir}
}

// Record adds an entry. It is safe for concurrent use.
func (l *ErrorLog) Record(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, e)
}

// Len reports how many entries were recorded.
func (l *ErrorLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.entries)
}

// Flush writes the entries, or nothing at all when there are none.
//
// An empty file would suggest diagnostics were attempted and came back blank,
// which is a different claim from "nothing went wrong".
func (l *ErrorLog) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) == 0 {
		return nil
	}

	f, err := os.Create(l.dir.Path(FileErrors))
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	for i := range l.entries {
		if err := enc.Encode(&l.entries[i]); err != nil {
			return err
		}
	}

	return nil
}
