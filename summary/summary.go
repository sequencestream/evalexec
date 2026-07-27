// Package summary accumulates the run totals as records are written.
//
// Counting happens on the single writer goroutine rather than by re-reading
// the records afterwards. That keeps the tallies race-free without a lock, and
// means a run never has to hold every record in memory to add them up.
//
// The counting identities from the specification are what bind "was this
// sample executed" to "did its evaluation succeed", so that every denominator
// in a result is explicit. They are checked before anything is written: a
// result that fails them is not published at all, because a result whose own
// numbers disagree is worse than a missing one.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise.
package summary

import (
	"maps"

	"github.com/sequencestream/evalexec/evalspec"
)

// Accumulator tallies records as they are written.
//
// It is not safe for concurrent use, by design: exactly one goroutine writes
// records, and it is the one that counts them.
type Accumulator struct {
	counts     evalspec.Counts
	success    int
	fail       int
	failByCode map[evalspec.ErrorCode]int

	scoreCount int
	scoreSum   float64
	scoreMin   float64
	scoreMax   float64

	usage evalspec.Usage
}

// New returns an empty accumulator.
func New() *Accumulator {
	return &Accumulator{failByCode: make(map[evalspec.ErrorCode]int)}
}

// Add folds one record into the totals.
func (a *Accumulator) Add(rec *evalspec.Record) {
	a.counts.Total++

	if rec.Status == evalspec.RecordSkipped {
		a.counts.Skipped++

		return
	}

	a.counts.Completed++

	if rec.Evaluation == nil {
		// The record validator rejects this shape, but counting it as
		// completed-and-nothing-else would silently break the identity
		// evaluated = success + fail. Treat it as a failure so the mismatch
		// surfaces rather than balancing out.
		a.fail++
		a.failByCode[evalspec.CodeInternalError]++

		return
	}

	// Usage is accumulated for failures too. A failed evaluation still burned
	// the tokens it burned, and dropping them would leave the summary
	// disagreeing with the bill.
	a.usage.Add(rec.Evaluation.Usage)

	if rec.Evaluation.Status == evalspec.EvaluationFail {
		a.fail++

		if rec.Evaluation.Error != nil {
			a.failByCode[rec.Evaluation.Error.Code]++
		} else {
			a.failByCode[evalspec.CodeInternalError]++
		}

		return
	}

	a.success++

	// Only a successful evaluation carrying a number contributes to the
	// statistics. A Grader that returns a label and no score is still a
	// success; it simply has nothing to average.
	if rec.Evaluation.Score == nil {
		return
	}

	s := *rec.Evaluation.Score

	if a.scoreCount == 0 {
		a.scoreMin, a.scoreMax = s, s
	} else {
		a.scoreMin = min(a.scoreMin, s)
		a.scoreMax = max(a.scoreMax, s)
	}

	a.scoreCount++
	a.scoreSum += s
}

// Counts returns the sample tallies.
func (a *Accumulator) Counts() evalspec.Counts { return a.counts }

// Usage returns the accumulated Judge token usage.
func (a *Accumulator) Usage() evalspec.Usage { return a.usage }

// Evaluation builds the summary block for a Grader.
//
// graderID and graderVersion come from the request: they are the names the
// caller gave this evaluation, not a property of the built-in entry. The
// pre-check has already confirmed the configuration agrees with the Grader's
// own declaration about what it requires.
func (a *Accumulator) Evaluation(graderID, graderVersion string) evalspec.EvaluationSummary {
	s := evalspec.EvaluationSummary{
		GraderID:      graderID,
		GraderVersion: graderVersion,
		Evaluated:     a.counts.Completed,
		Success:       a.success,
		Fail:          a.fail,
		Score:         a.score(),
	}

	// An absent code is omitted rather than reported as zero, so a consumer
	// reading fail_by_code sees only what actually happened.
	if len(a.failByCode) > 0 {
		s.FailByCode = maps.Clone(a.failByCode)
	}

	return s
}

// score builds the descriptive statistics. With nothing scored, all three are
// null: there is no mean of an empty set, and reporting zero would invent a
// measurement.
func (a *Accumulator) score() evalspec.ScoreStats {
	if a.scoreCount == 0 {
		return evalspec.ScoreStats{Count: 0}
	}

	mean := a.scoreSum / float64(a.scoreCount)
	minimum, maximum := a.scoreMin, a.scoreMax

	return evalspec.ScoreStats{
		Count: a.scoreCount,
		Mean:  &mean,
		Min:   &minimum,
		Max:   &maximum,
	}
}

// Status derives the run status from the tallies and how the run stopped.
//
// A run that processed everything is completed even if every evaluation
// failed: the top-level status describes the run, not the results.
func Status(skipped int, stopped bool, reason evalspec.StopReason) (evalspec.RunStatus, *evalspec.StopReason) {
	if !stopped && skipped == 0 {
		return evalspec.RunCompleted, nil
	}

	r := reason

	return evalspec.RunCancelled, &r
}
