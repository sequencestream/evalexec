package result

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sequencestream/evalexec/internal/judge/transport"
)

// LogWriter writes the raw Judge exchanges of individual samples into logs/.
//
// It lives here rather than in the runner because this package already owns the
// result directory, and it takes transport.Exchange rather than an opaque type
// so the shape of what is written stays checked at compile time. That import is
// safe: judge/transport depends only on the standard library, so nothing about
// the vendor SDK reaches this far.
//
// The directory is created lazily. A run in which nothing failed leaves no
// logs/ at all, which is the right signal — an empty directory suggests
// diagnostics were attempted and came back blank.
type LogWriter struct {
	dir     *Dir
	mu      sync.Mutex
	created bool
}

// NewLogWriter returns a writer targeting the pending result directory.
func NewLogWriter(dir *Dir) *LogWriter {
	return &LogWriter{dir: dir}
}

// Keep writes one sample's exchanges to logs/judge-<case_id>.jsonl.
func (w *LogWriter) Keep(caseID string, exchanges []transport.Exchange) error {
	if len(exchanges) == 0 {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.created {
		if err := w.dir.MkdirAll(DirLogs); err != nil {
			return err
		}

		w.created = true
	}

	name := filepath.Join(DirLogs, "judge-"+sanitize(caseID)+".jsonl")

	f, err := os.OpenFile(w.dir.Path(name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("result: cannot open %s: %w", name, err)
	}

	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	for i := range exchanges {
		if err := enc.Encode(&exchanges[i]); err != nil {
			return fmt.Errorf("result: cannot write %s: %w", name, err)
		}
	}

	return nil
}

// Discard drops a sample's exchanges without writing them.
func (w *LogWriter) Discard(string) {}

// HasLogs reports whether any log file was written, so the result can name the
// directory in its artifacts only when it exists.
func (w *LogWriter) HasLogs() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.created
}

// LogsPath is the artifacts value for the logs directory.
func LogsPath() string { return DirLogs + string(filepath.Separator) }

// discardLogs is the no-op sink used when diagnostics are not wanted.
type discardLogs struct{}

func (discardLogs) Keep(string, []transport.Exchange) error { return nil }
func (discardLogs) Discard(string)                          {}

// DiscardLogs returns a sink that keeps nothing.
func DiscardLogs() interface {
	Keep(string, []transport.Exchange) error
	Discard(string)
} {
	return discardLogs{}
}
