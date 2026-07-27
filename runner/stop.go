package runner

import (
	"slices"
	"sync"

	"github.com/sequencestream/evalexec/evalspec"
)

// stopper records why a run stopped and cancels the workers.
//
// fail-fast and an interrupt share one mechanism because the specification
// gives them one behaviour: stop dispatching, backfill the rest, publish. Only
// the recorded reason differs.
type stopper struct {
	mu     sync.Mutex
	reason evalspec.StopReason
	fired  bool
	cancel func()
}

// stop records the reason and cancels the workers.
//
// The first caller wins. If fail-fast has already fired and an interrupt
// arrives afterwards, the reason stays fail_fast — that is genuinely why the
// run ended early, and overwriting it would misreport the cause.
func (s *stopper) stop(reason evalspec.StopReason) {
	s.mu.Lock()

	if s.fired {
		s.mu.Unlock()

		return
	}

	s.fired, s.reason = true, reason

	s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
}

func (s *stopper) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.fired
}

func (s *stopper) state() (bool, evalspec.StopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.fired, s.reason
}

// pendingRow identifies a row that needs a backfilled record.
type pendingRow struct {
	sequence int
	caseID   string
}

// inflightSet remembers every row handed to a worker, so the backfill knows
// which ones never came back.
//
// Rows already read from the dataset cannot be re-read, so their identifiers
// have to be kept here. The rows never dispatched are still on disk and are
// read during backfill instead — the two sources together cover every line.
type inflightSet struct {
	mu   sync.Mutex
	rows map[int]string
}

func newInflightSet() *inflightSet {
	return &inflightSet{rows: make(map[int]string)}
}

func (s *inflightSet) add(sequence int, caseID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rows[sequence] = caseID
}

// pending returns the dispatched rows that produced no record.
func (s *inflightSet) pending(written map[int]bool) []pendingRow {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []pendingRow

	for seq, caseID := range s.rows {
		if !written[seq] {
			out = append(out, pendingRow{sequence: seq, caseID: caseID})
		}
	}

	return out
}

func sortPending(rows []pendingRow) {
	slices.SortFunc(rows, func(a, b pendingRow) int { return a.sequence - b.sequence })
}
