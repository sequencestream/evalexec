package evalspec

import (
	"fmt"
	"strings"
)

// ValidationError is one broken invariant, located by a JSON-ish path so the
// message points at a field rather than at a concept.
type ValidationError struct {
	// Path locates the offending field, e.g. "counts.total" or
	// "evaluation.score".
	Path string
	// Message states what is wrong.
	Message string
}

func (e ValidationError) Error() string {
	return e.Path + ": " + e.Message
}

// ValidationErrors aggregates every problem found in one pass.
//
// Validation reports all failures rather than the first, because a caller
// fixing a malformed request wants the whole list, not one item at a time.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "no validation errors"
	}

	parts := make([]string, len(e))
	for i, ve := range e {
		parts[i] = ve.Error()
	}

	return strings.Join(parts, "; ")
}

// OrNil returns nil when there is nothing to report, so callers can write
// `return errs.OrNil()` without a length check. Returning a typed nil slice
// as an error would produce a non-nil error interface — the classic trap.
func (e ValidationErrors) OrNil() error {
	if len(e) == 0 {
		return nil
	}

	return e
}

// add appends a formatted problem.
func (e *ValidationErrors) add(path, format string, args ...any) {
	*e = append(*e, ValidationError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// Validate checks the request's structural invariants. It deliberately does
// not touch the filesystem or the environment: whether the dataset parses and
// whether the credential environment variable is set are pre-check steps that
// need I/O, and they are ordered relative to other checks by the validate
// package.
func (r *EvalRequest) Validate() error {
	var errs ValidationErrors

	if r.SpecVersion != SpecVersion {
		errs.add("spec_version", "must be %q, got %q", SpecVersion, r.SpecVersion)
	}

	if r.TaskID == "" {
		errs.add("task_id", "must not be empty")
	}

	if r.Dataset.Path == "" {
		errs.add("dataset.path", "must not be empty")
	}

	if r.OutputDir == "" {
		errs.add("output_dir", "must not be empty")
	}

	r.Grader.validate(&errs)

	// judge_model is required exactly when the Grader declares it needs one.
	// Supplying one that is not needed is not an error; it is simply unused.
	if r.Grader.RequiresJudge && r.JudgeModel == nil {
		errs.add("judge_model", "is required because grader.requires_judge is true")
	}

	if r.JudgeModel != nil {
		r.JudgeModel.validate(&errs)
	}

	if r.Execution != nil && r.Execution.Concurrency < 0 {
		errs.add("execution.concurrency", "must not be negative, got %d", r.Execution.Concurrency)
	}

	return errs.OrNil()
}

// validate checks the Grader's self-description, which is what lets EvalExec
// pre-validate a run without understanding the Grader's internals.
func (g *GraderSpec) validate(errs *ValidationErrors) {
	if g.ID == "" {
		errs.add("grader.id", "must not be empty")
	}

	if g.Version == "" {
		errs.add("grader.version", "must not be empty")
	}

	if !g.Protocol.IsValid() {
		errs.add("grader.protocol", "must be builtin, http-json or stdio-jsonl, got %q", g.Protocol)
	}

	if g.Entry == "" {
		errs.add("grader.entry", "must not be empty")
	}

	// requires must be present. An empty declaration is legal in principle
	// but a nil one usually means the key was forgotten, and the
	// specification lists requires as mandatory.
	if g.Requires == nil {
		errs.add("grader.requires", "must be declared (use [] for a Grader that needs no session fields)")
	}

	seen := make(map[SessionField]bool, len(g.Requires))

	for i, f := range g.Requires {
		path := fmt.Sprintf("grader.requires[%d]", i)

		if !f.IsValid() {
			errs.add(path, "%q is not a valid session field", f)

			continue
		}

		if seen[f] {
			errs.add(path, "%q is listed more than once", f)
		}

		seen[f] = true
	}
}

// validate checks the Judge configuration's structure. Whether the referenced
// environment variable actually holds a value is a pre-check, not a
// structural property, and belongs to the validate package.
func (j *JudgeModelSpec) validate(errs *ValidationErrors) {
	if !j.Protocol.IsValid() {
		errs.add("judge_model.protocol", "must be openai-chat, anthropic-messages, http-json or stdio-jsonl, got %q", j.Protocol)
	}

	if !j.Auth.Type.IsValid() {
		errs.add("judge_model.auth.type", "must be bearer_env or none, got %q", j.Auth.Type)
	}

	if j.Auth.Type == AuthBearerEnv && j.Auth.Env == "" {
		errs.add("judge_model.auth.env", "must name an environment variable when type is bearer_env")
	}

	// A credential must never be inlined. Catching it here means a misplaced
	// secret fails before it can be written into a request snapshot.
	if j.Auth.Type == AuthNone && j.Auth.Env != "" {
		errs.add("judge_model.auth.env", "must be empty when type is none")
	}

	if j.TimeoutMS < 0 {
		errs.add("judge_model.timeout_ms", "must not be negative, got %d", j.TimeoutMS)
	}
}

// Validate checks one record's invariants: a completed record carries an
// evaluation and timestamps, a skipped one carries neither and explains why.
func (r *Record) Validate() error {
	var errs ValidationErrors

	if r.EvalID == "" {
		errs.add("eval_id", "must not be empty")
	}

	if r.CaseID == "" {
		errs.add("case_id", "must not be empty")
	}

	if r.Sequence < 1 {
		errs.add("sequence", "must be 1-based, got %d", r.Sequence)
	}

	if !r.Status.IsValid() {
		errs.add("status", "must be completed or skipped, got %q", r.Status)

		return errs.OrNil()
	}

	switch r.Status {
	case RecordCompleted:
		r.validateCompleted(&errs)
	case RecordSkipped:
		r.validateSkipped(&errs)
	}

	return errs.OrNil()
}

func (r *Record) validateCompleted(errs *ValidationErrors) {
	if r.Evaluation == nil {
		errs.add("evaluation", "must not be null on a completed record")
	} else {
		r.Evaluation.validate(errs)
	}

	if r.StartedAt == nil {
		errs.add("started_at", "must not be null on a completed record")
	}

	if r.FinishedAt == nil {
		errs.add("finished_at", "must not be null on a completed record")
	}

	if r.Error != nil {
		errs.add("error", "must be null on a completed record")
	}
}

func (r *Record) validateSkipped(errs *ValidationErrors) {
	if r.Evaluation != nil {
		errs.add("evaluation", "must be null on a skipped record")
	}

	if r.StartedAt != nil {
		errs.add("started_at", "must be null on a skipped record")
	}

	if r.FinishedAt != nil {
		errs.add("finished_at", "must be null on a skipped record")
	}

	if r.Error == nil {
		errs.add("error", "must not be null on a skipped record")

		return
	}

	if r.Error.Code != CodeSkipped {
		errs.add("error.code", "must be %q on a skipped record, got %q", CodeSkipped, r.Error.Code)
	}

	if r.Error.Reason != StopFailFast && r.Error.Reason != StopInterrupt {
		errs.add("error.reason", "must be fail_fast or interrupt, got %q", r.Error.Reason)
	}
}

// validate checks the two-valued evaluation status and its consequences: a
// failure has an error and no score, a success has neither constraint
// inverted.
func (e *Evaluation) validate(errs *ValidationErrors) {
	if !e.Status.IsValid() {
		errs.add("evaluation.status", "must be success or fail, got %q", e.Status)

		return
	}

	switch e.Status {
	case EvaluationSuccess:
		if e.Error != nil {
			errs.add("evaluation.error", "must be null on a successful evaluation")
		}
	case EvaluationFail:
		// The core rule: a failure is not a zero.
		if e.Score != nil {
			errs.add("evaluation.score", "must be null on a failed evaluation, got %v (a failure is never counted as a zero)", *e.Score)
		}

		if e.Error == nil {
			errs.add("evaluation.error", "must not be null on a failed evaluation")

			return
		}

		if !e.Error.Code.IsEvaluationFailure() {
			errs.add("evaluation.error.code", "%q cannot classify a failed evaluation", e.Error.Code)
		}
	}
}

// Validate checks the result's invariants, above all the counting identities
// that bind counts to evaluation totals. The result writer calls this
// unconditionally before writing anything, because a downstream caller can
// assemble an EvalResult by hand and this package cannot assume otherwise.
func (r *EvalResult) Validate() error {
	var errs ValidationErrors

	if r.SpecVersion != SpecVersion {
		errs.add("spec_version", "must be %q, got %q", SpecVersion, r.SpecVersion)
	}

	if r.EvalID == "" {
		errs.add("eval_id", "must not be empty")
	}

	if !r.Status.IsValid() {
		errs.add("status", "must be completed, cancelled or failed, got %q", r.Status)
	}

	r.validateStatusBinding(&errs)
	r.validateCounts(&errs)
	r.validateScore(&errs)

	return errs.OrNil()
}

// validateStatusBinding enforces the coupling between the run status, the
// stop reason and the skipped count.
func (r *EvalResult) validateStatusBinding(errs *ValidationErrors) {
	switch r.Status {
	case RunCompleted:
		if r.StopReason != nil {
			errs.add("stop_reason", "must be null when status is completed, got %q", *r.StopReason)
		}

		if r.Counts.Skipped != 0 {
			errs.add("counts.skipped", "must be 0 when status is completed, got %d", r.Counts.Skipped)
		}

		if r.Error != nil {
			errs.add("error", "must be null when status is completed")
		}
	case RunCancelled:
		if r.StopReason == nil {
			errs.add("stop_reason", "must not be null when status is cancelled")
		} else if *r.StopReason != StopFailFast && *r.StopReason != StopInterrupt {
			errs.add("stop_reason", "must be fail_fast or interrupt when status is cancelled, got %q", *r.StopReason)
		}

		if r.Counts.Skipped <= 0 {
			errs.add("counts.skipped", "must be greater than 0 when status is cancelled, got %d", r.Counts.Skipped)
		}
	case RunFailed:
		if r.Error == nil {
			errs.add("error", "must not be null when status is failed")
		}
	}
}

// validateCounts enforces the counting identities of the specification. They
// are what bind the "was it executed" tally to the "did the evaluation
// succeed" tally, so every denominator in the result is explicit.
func (r *EvalResult) validateCounts(errs *ValidationErrors) {
	c, e := r.Counts, r.Evaluation

	for path, n := range map[string]int{
		"counts.total": c.Total, "counts.completed": c.Completed, "counts.skipped": c.Skipped,
		"evaluation.evaluated": e.Evaluated, "evaluation.success": e.Success, "evaluation.fail": e.Fail,
		"evaluation.score.count": e.Score.Count,
	} {
		if n < 0 {
			errs.add(path, "must not be negative, got %d", n)
		}
	}

	if c.Total != c.Completed+c.Skipped {
		errs.add("counts.total", "must equal completed + skipped: %d != %d + %d", c.Total, c.Completed, c.Skipped)
	}

	if e.Evaluated != c.Completed {
		errs.add("evaluation.evaluated", "must equal counts.completed: %d != %d", e.Evaluated, c.Completed)
	}

	if e.Evaluated != e.Success+e.Fail {
		errs.add("evaluation.evaluated", "must equal success + fail: %d != %d + %d", e.Evaluated, e.Success, e.Fail)
	}

	sum := 0

	for code, n := range e.FailByCode {
		if !code.IsEvaluationFailure() {
			errs.add("evaluation.fail_by_code", "%q cannot classify a failed evaluation", code)
		}

		if n < 0 {
			errs.add("evaluation.fail_by_code."+string(code), "must not be negative, got %d", n)
		}

		sum += n
	}

	if e.Fail != sum {
		errs.add("evaluation.fail", "must equal the sum of fail_by_code: %d != %d", e.Fail, sum)
	}

	if e.Score.Count > e.Success {
		errs.add("evaluation.score.count", "must not exceed success: %d > %d", e.Score.Count, e.Success)
	}
}

// validateScore enforces that the score statistics are all-or-nothing: with
// no scored samples there is no mean, minimum or maximum to report.
func (r *EvalResult) validateScore(errs *ValidationErrors) {
	s := r.Evaluation.Score

	if s.Count == 0 {
		if s.Mean != nil {
			errs.add("evaluation.score.mean", "must be null when score.count is 0")
		}

		if s.Min != nil {
			errs.add("evaluation.score.min", "must be null when score.count is 0")
		}

		if s.Max != nil {
			errs.add("evaluation.score.max", "must be null when score.count is 0")
		}

		return
	}

	if s.Mean == nil {
		errs.add("evaluation.score.mean", "must not be null when score.count is %d", s.Count)
	}

	if s.Min == nil {
		errs.add("evaluation.score.min", "must not be null when score.count is %d", s.Count)
	}

	if s.Max == nil {
		errs.add("evaluation.score.max", "must not be null when score.count is %d", s.Count)
	}

	if s.Min != nil && s.Max != nil && *s.Min > *s.Max {
		errs.add("evaluation.score.min", "must not exceed max: %v > %v", *s.Min, *s.Max)
	}

	if s.Mean != nil && s.Min != nil && s.Max != nil && (*s.Mean < *s.Min || *s.Mean > *s.Max) {
		errs.add("evaluation.score.mean", "must lie within [min, max]: %v not in [%v, %v]", *s.Mean, *s.Min, *s.Max)
	}
}
