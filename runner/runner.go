// Package runner dispatches samples to the Grader and writes the records.
//
// The shape is a pipeline even though this stage dispatches one sample at a
// time: the dispatch loop sends results to a channel, and a single writer
// goroutine owns both records.jsonl and the running totals. Concurrency
// arrives later by replacing the loop with a worker pool, leaving the writer
// and the accumulator untouched — and, more importantly, leaving the tallies
// on one goroutine where they need no lock and no second pass over the file.
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
	"time"

	"github.com/sequencestream/evalexec/dataset"
	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/summary"
)

// StepRun is the step name reported for run-level faults.
const StepRun = "run"

// Clock supplies timestamps, injectable so golden-file tests compare.
type Clock interface {
	Now() time.Time
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
	// Clock supplies record timestamps.
	Clock Clock
	// Diag receives progress notes; nil discards them.
	Diag io.Writer
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

// Run grades every sample in the dataset, writing one record per row.
func Run(ctx context.Context, cfg Config, records io.Writer) (*Outcome, error) {
	if cfg.Diag == nil {
		cfg.Diag = io.Discard
	}

	reader, err := dataset.Open(cfg.DatasetPath)
	if err != nil {
		return nil, evalerr.Wrap(evalerr.KindRuntime, StepRun, err, "cannot re-read the dataset")
	}

	defer func() { _ = reader.Close() }()

	w := newWriter(records)

	// The writer goroutine owns the file and the tallies. Nothing else touches
	// either, so neither needs a lock.
	done := make(chan struct{})
	results := make(chan evalspec.Record, 1)

	go func() {
		defer close(done)

		for rec := range results {
			w.write(&rec)
		}
	}()

	dispatchErr := dispatch(ctx, cfg, reader, results)

	close(results)
	<-done

	if dispatchErr != nil {
		return nil, dispatchErr
	}

	if w.err != nil {
		return nil, evalerr.Wrap(evalerr.KindOutput, StepRun, w.err, "cannot write records")
	}

	return &Outcome{
		Counts:     w.acc.Counts(),
		Evaluation: w.acc.Evaluation(cfg.GraderID, cfg.GraderVersion),
		Usage:      w.acc.Usage(),
	}, nil
}

// dispatch reads the dataset and grades each row in turn.
func dispatch(ctx context.Context, cfg Config, reader *dataset.Reader, results chan<- evalspec.Record) error {
	for {
		session, seq, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			// The dataset validated cleanly moments ago, so a parse failure
			// now means it changed underneath the run — a run-level fault,
			// not a per-sample one.
			return evalerr.Wrap(evalerr.KindRuntime, StepRun, err,
				"the dataset changed while the run was in progress")
		}

		results <- grade(ctx, cfg, session, seq)
	}
}

// grade evaluates one sample and builds its record.
func grade(ctx context.Context, cfg Config, session *evalspec.Session, seq int) evalspec.Record {
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
		eval = classify(err, latency)
	}

	eval.LatencyMS = latency
	finished := evalspec.NewTimestamp(cfg.Clock.Now())

	return evalspec.NewCompletedRecord(cfg.TaskID, cfg.EvalID, session.CaseID, seq, eval, started, finished)
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

// classify turns a Grader error into a failed evaluation.
//
// The distinction between a deadline and a cancellation matters and is checked
// with errors.Is rather than by reading ctx.Err(): a timeout is a completed
// sample whose evaluation failed, while a cancellation means the sample was
// never finished and belongs to the caller's stop path, not here.
func classify(err error, latency int64) evalspec.Evaluation {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return evalspec.NewFailEvaluation(evalspec.CodeTimeout,
			"the grader exceeded its timeout", "evaluation timed out", nil, evalspec.Usage{}, latency)
	default:
		return evalspec.NewFailEvaluation(evalspec.CodeInternalError,
			err.Error(), "the grader failed", nil, evalspec.Usage{}, latency)
	}
}

// writer owns records.jsonl and the running totals.
type writer struct {
	enc *json.Encoder
	acc *summary.Accumulator
	err error
}

func newWriter(w io.Writer) *writer {
	return &writer{enc: json.NewEncoder(w), acc: summary.New()}
}

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

	w.acc.Add(rec)
}
