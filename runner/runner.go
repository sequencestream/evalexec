// Package runner dispatches samples to the Grader and writes the records.
//
// One invariant governs everything here: records.jsonl has exactly one line
// per dataset row, on every path out — completion, fail-fast, or interrupt.
// That identity is the difference between a partial result that can still be
// trusted and one that has merely been truncated.
//
// The shape is a worker pool feeding a single writer goroutine. The writer owns
// both the file and the running totals, so neither needs a lock and no second
// pass over the output is required to add anything up.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/sequencestream/evalexec/dataset"
	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/judge/transport"
	"github.com/sequencestream/evalexec/summary"
)

// StepRun is the step name reported for run-level faults.
const StepRun = "run"

// Clock supplies timestamps, injectable so golden-file tests compare.
type Clock interface {
	Now() time.Time
}

// LogSink keeps or drops the raw Judge exchanges recorded for a sample.
//
// The runner decides which, because only it knows how the sample turned out.
// Keeping every exchange would leave a prompt echo on disk for each of ten
// thousand successful samples; keeping none would remove the evidence exactly
// when something needs diagnosing.
type LogSink interface {
	Keep(caseID string, exchanges []transport.Exchange) error
	Discard(caseID string)
}

// Recorder is the subset of the transport recorder the runner needs.
type Recorder interface {
	Take(caseID string) []transport.Exchange
	Discard(caseID string)
}

// Config describes one run.
type Config struct {
	EvalID string
	TaskID string
	// DatasetPath is the file to read; it is scanned a second time here,
	// having already been validated end to end.
	DatasetPath string
	// Grader evaluates each sample.
	Grader grader.Grader
	// GraderID and GraderVersion are the names the caller gave this
	// evaluation; they are echoed verbatim into the summary.
	GraderID      string
	GraderVersion string
	// Parameters are passed to the Grader with every call.
	Parameters map[string]any
	// GraderTimeout bounds one sample's evaluation; zero means no bound.
	GraderTimeout time.Duration
	// Concurrency is how many samples are evaluated at once; below 1 means 1.
	Concurrency int
	// FailFast stops dispatching after the first failed evaluation.
	FailFast bool
	// Clock supplies record timestamps.
	Clock Clock
	// Recorder and Logs, when both set, retain the raw Judge exchanges of
	// failed samples.
	Recorder Recorder
	Logs     LogSink
	// KeepAllLogs retains exchanges for successful samples too.
	KeepAllLogs bool
	// Diag receives progress notes; nil discards them.
	Diag io.Writer
}

func (c Config) concurrency() int {
	if c.Concurrency < 1 {
		return 1
	}

	return c.Concurrency
}

// Outcome is what a run produced, short of the result document itself.
type Outcome struct {
	Counts     evalspec.Counts
	Evaluation evalspec.EvaluationSummary
	Usage      evalspec.Usage
	// Stopped reports whether dispatch ended early.
	Stopped bool
	// StopReason is meaningful only when Stopped is true.
	StopReason evalspec.StopReason
}

// job is one sample on its way to a worker.
type job struct {
	session  *evalspec.Session
	sequence int
}

// resultMsg carries one finished record to the writer, with a channel the
// writer closes once the record is on disk.
type resultMsg struct {
	record evalspec.Record
	ack    chan struct{}
}

// Run grades every sample in the dataset, writing one record per row.
//
// It returns after every row has a record, including on a stopping path: the
// remaining rows are backfilled as skipped before it returns.
func Run(ctx context.Context, cfg Config, records io.Writer) (*Outcome, error) {
	if cfg.Diag == nil {
		cfg.Diag = io.Discard
	}

	reader, err := dataset.Open(cfg.DatasetPath)
	if err != nil {
		return nil, evalerr.Wrap(evalerr.KindRuntime, StepRun, err, "cannot re-read the dataset")
	}

	defer func() { _ = reader.Close() }()

	// Workers run under a context the stopper can cancel independently of the
	// caller's, so fail-fast and an interrupt take the same path.
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	st := &stopper{cancel: cancel}
	w := newWriter(records, cfg, st)

	// Both channels are unbuffered, and a worker waits for its own record to be
	// persisted before taking the next sample.
	//
	// That acknowledgement is what makes the stopping point predictable. Without
	// it a worker prefetches the next sample while its previous verdict is still
	// in the writer's hands, so a fail-fast decision arrives after work has
	// already begun on a sample that should never have started — and at
	// concurrency 1 the specification says the stopping point is deterministic.
	// The cost is one channel round trip per sample, against a Grader call that
	// is orders of magnitude slower.
	jobs := make(chan job)
	results := make(chan resultMsg)

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		for msg := range results {
			w.write(&msg.record)
			close(msg.ack)
		}
	}()

	var workers sync.WaitGroup

	for range cfg.concurrency() {
		workers.Add(1)

		go func() {
			defer workers.Done()

			for j := range jobs {
				// Do not start work after a stop. The sample stays in the
				// in-flight set and is backfilled as skipped, which is the
				// truth: it was never evaluated.
				if workerCtx.Err() != nil {
					continue
				}

				rec, ok := grade(workerCtx, cfg, j.session, j.sequence)
				if !ok {
					// Cancelled mid-evaluation. No record either, for the same
					// reason.
					continue
				}

				ack := make(chan struct{})
				results <- resultMsg{record: rec, ack: ack}

				<-ack
			}
		}()
	}

	inflight := newInflightSet()
	dispatchErr := dispatch(ctx, workerCtx, reader, jobs, inflight, st)

	close(jobs)
	workers.Wait()
	close(results)
	<-writerDone

	if dispatchErr != nil {
		return nil, dispatchErr
	}

	if w.err != nil {
		return nil, evalerr.Wrap(evalerr.KindOutput, StepRun, w.err, "cannot write records")
	}

	// The caller's own context being done means an interrupt reached us, even
	// if dispatch had already stopped for another reason.
	if ctx.Err() != nil {
		st.stop(evalspec.StopInterrupt)
	}

	if err := backfill(cfg, w, reader, inflight, st); err != nil {
		return nil, err
	}

	if w.err != nil {
		return nil, evalerr.Wrap(evalerr.KindOutput, StepRun, w.err, "cannot write backfilled records")
	}

	stopped, reason := st.state()

	return &Outcome{
		Counts:     w.acc.Counts(),
		Evaluation: w.acc.Evaluation(cfg.GraderID, cfg.GraderVersion),
		Usage:      w.acc.Usage(),
		Stopped:    stopped,
		StopReason: reason,
	}, nil
}

// dispatch reads the dataset and feeds the workers until the dataset is
// exhausted or the run stops.
func dispatch(
	ctx, workerCtx context.Context,
	reader *dataset.Reader,
	jobs chan<- job,
	inflight *inflightSet,
	st *stopper,
) error {
	for {
		if st.stopped() {
			return nil
		}

		// An interrupt reaches the caller's context first.
		if ctx.Err() != nil {
			st.stop(evalspec.StopInterrupt)

			return nil
		}

		session, seq, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// The dataset is exhausted, which is how a run normally ends.
				return nil //nolint:nilerr // io.EOF is the end of the input, not a fault
			}

			// The dataset validated cleanly moments ago, so a parse failure
			// now means it changed underneath the run — a run-level fault,
			// not a per-sample one.
			return evalerr.Wrap(evalerr.KindRuntime, StepRun, err,
				"the dataset changed while the run was in progress")
		}

		inflight.add(seq, session.CaseID)

		select {
		case jobs <- job{session: session, sequence: seq}:
		case <-workerCtx.Done():
			// Stopped while waiting for a free worker. This row was counted as
			// in flight and the backfill will pick it up.
			return nil
		}
	}
}

// grade evaluates one sample and builds its record.
//
// The second result is false when the sample was cancelled rather than
// evaluated. A cancelled sample must produce no record here: whether it counts
// as completed is decided by the writer, from the order records arrive, and a
// record for work that never finished would corrupt that.
func grade(ctx context.Context, cfg Config, session *evalspec.Session, seq int) (evalspec.Record, bool) {
	started := evalspec.NewTimestamp(cfg.Clock.Now())
	call := evalspec.NewGradeCall(cfg.EvalID, cfg.TaskID, session, cfg.Parameters)

	callCtx := ctx

	if cfg.GraderTimeout > 0 {
		var cancel context.CancelFunc

		callCtx, cancel = context.WithTimeout(ctx, cfg.GraderTimeout)
		defer cancel()
	}

	start := time.Now()
	eval, err := safeGrade(callCtx, cfg.Grader, call)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		// Cancellation is checked before the deadline, and with errors.Is
		// rather than by reading ctx.Err(). A timed-out sample was processed
		// and its evaluation failed; a cancelled one was never finished at
		// all. Reporting the second as the first would present work that never
		// happened as work that happened badly.
		if errors.Is(err, context.Canceled) {
			return evalspec.Record{}, false
		}

		eval = classify(err, latency)
	}

	eval.LatencyMS = latency
	finished := evalspec.NewTimestamp(cfg.Clock.Now())

	return evalspec.NewCompletedRecord(cfg.TaskID, cfg.EvalID, session.CaseID, seq, eval, started, finished), true
}

// safeGrade calls the Grader, turning a panic into an error.
//
// A Grader is an extension point, so downstream code runs here. Letting a
// panic escape would kill the process — unacceptable for a library embedded in
// someone else's program, and needless even for the binary: one broken Grader
// call should cost one sample, not the run.
func safeGrade(ctx context.Context, g grader.Grader, call evalspec.GradeCall) (eval evalspec.Evaluation, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("grader panicked: %v", r)
		}
	}()

	return g.Grade(ctx, call)
}

// classify turns a Grader error into a failed evaluation. Cancellation is
// handled by the caller and never reaches here.
func classify(err error, latency int64) evalspec.Evaluation {
	if errors.Is(err, context.DeadlineExceeded) {
		return evalspec.NewFailEvaluation(evalspec.CodeTimeout,
			"the grader exceeded its timeout", "evaluation timed out", nil, evalspec.Usage{}, latency)
	}

	return evalspec.NewFailEvaluation(evalspec.CodeInternalError,
		err.Error(), "the grader failed", nil, evalspec.Usage{}, latency)
}

// backfill writes a skipped record for every row that produced no evaluation.
//
// It reads whatever is left of the dataset, because it needs each remaining
// row's case_id and sequence. "Stop dispatching" is not "exit immediately",
// and this is where that distinction becomes concrete: without finishing the
// read there is no way to write the missing lines at all.
func backfill(cfg Config, w *writer, reader *dataset.Reader, inflight *inflightSet, st *stopper) error {
	stopped, reason := st.state()

	pending := inflight.pending(w.written())

	if stopped {
		// Rows never dispatched are still on disk.
		for {
			session, seq, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				return evalerr.Wrap(evalerr.KindRuntime, StepRun, err,
					"cannot finish reading the dataset to backfill skipped records")
			}

			pending = append(pending, pendingRow{sequence: seq, caseID: session.CaseID})
		}
	}

	if len(pending) == 0 {
		return nil
	}

	if !stopped {
		// Rows went missing without the run having stopped. Backfilling them
		// would paper over a defect: the line count would look right while the
		// result silently lost evaluations.
		return evalerr.Runtime(StepRun,
			"%d samples produced no evaluation although the run was not stopped", len(pending))
	}

	// Backfilled rows go out in input order. The concurrent phase may write
	// out of order, but there is no concurrency here and therefore no reason
	// to be disordered.
	sortPending(pending)

	for _, p := range pending {
		rec := evalspec.NewSkippedRecord(cfg.TaskID, cfg.EvalID, p.caseID, p.sequence, reason)
		w.write(&rec)

		if w.err != nil {
			return evalerr.Wrap(evalerr.KindOutput, StepRun, w.err, "cannot backfill skipped records")
		}
	}

	return nil
}

// writer owns records.jsonl and the running totals.
type writer struct {
	enc     *json.Encoder
	acc     *summary.Accumulator
	cfg     Config
	stopper *stopper
	seen    map[int]bool
	err     error
}

func newWriter(w io.Writer, cfg Config, st *stopper) *writer {
	return &writer{
		enc: json.NewEncoder(w), acc: summary.New(),
		cfg: cfg, stopper: st, seen: make(map[int]bool),
	}
}

// written reports which sequences already have a record.
func (w *writer) written() map[int]bool { return w.seen }

func (w *writer) write(rec *evalspec.Record) {
	if w.err != nil {
		return
	}

	// The record is validated before it is written rather than after: an
	// invalid record on disk is already a broken result, and this package
	// cannot assume every Grader built one correctly.
	if err := rec.Validate(); err != nil {
		w.err = fmt.Errorf("record %d (%s) is not valid: %w", rec.Sequence, rec.CaseID, err)

		return
	}

	if err := w.enc.Encode(rec); err != nil {
		w.err = err

		return
	}

	w.seen[rec.Sequence] = true
	w.acc.Add(rec)
	w.retainLogs(rec)

	// Fail-fast is decided here, after the record is on disk, and nowhere else.
	// A worker cannot make this call: at the moment fail-fast fires another
	// worker may have just finished, and whether that sample counts is settled
	// by the order records arrive on this goroutine — the only place with a
	// total order over them.
	//
	// Only a failed evaluation triggers it. A low score never does: EvalExec
	// does not interpret scores, so it has no basis for calling zero bad.
	if w.cfg.FailFast && rec.Status == evalspec.RecordCompleted &&
		rec.Evaluation != nil && rec.Evaluation.Status == evalspec.EvaluationFail {
		w.stopper.stop(evalspec.StopFailFast)
	}
}

// retainLogs keeps or drops the raw Judge exchanges for a sample.
func (w *writer) retainLogs(rec *evalspec.Record) {
	if w.cfg.Recorder == nil || w.cfg.Logs == nil {
		return
	}

	failed := rec.Evaluation != nil && rec.Evaluation.Status == evalspec.EvaluationFail

	if !failed && !w.cfg.KeepAllLogs {
		w.cfg.Recorder.Discard(rec.CaseID)
		w.cfg.Logs.Discard(rec.CaseID)

		return
	}

	exchanges := w.cfg.Recorder.Take(rec.CaseID)
	if len(exchanges) == 0 {
		return
	}

	if err := w.cfg.Logs.Keep(rec.CaseID, exchanges); err != nil {
		// A diagnostic that could not be written is worth mentioning but must
		// not fail the run: the result itself is unaffected.
		_, _ = fmt.Fprintf(w.cfg.Diag, "evalexec: cannot write logs for %s: %v\n", rec.CaseID, err)
	}
}
