// Package evalexec runs exactly one evaluation: one request, one Grader, one
// result.
//
// It does not run the agent being evaluated. Sessions are produced upstream,
// and EvalExec grades what they contain — the final output, the execution
// trajectory, or the agreement between the two.
//
// # Using it as a library
//
// Run is the whole interface. It is deliberately as atomic as the command line
// is, rather than asking a caller to assemble validation, dataset reading,
// dispatch, summarising and result writing themselves:
//
//	result, err := evalexec.Run(ctx, request)
//
// The subpackages exist for extension, not for assembly. The most useful one
// is grader: register an implementation and a run can use it under protocol
// "builtin" with your own entry name, no subprocess involved.
//
// # What Run promises
//
//   - Nothing is written until every pre-check passes. A rejected run leaves
//     no directory behind, temporary ones included.
//   - The result directory appears in one step or not at all.
//   - records.jsonl has exactly one line per dataset row, on every path.
//   - A failed evaluation is never recorded as a zero score.
//
// # Stability
//
// L2 extension point. From v1.0 this follows the Go compatibility promise.
package evalexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	_ "github.com/sequencestream/evalexec/grader/builtin" // register the built-in Graders
	"github.com/sequencestream/evalexec/grader/declaration"
	"github.com/sequencestream/evalexec/judge"
	"github.com/sequencestream/evalexec/judge/transport"
	"github.com/sequencestream/evalexec/redact"
	"github.com/sequencestream/evalexec/result"
	"github.com/sequencestream/evalexec/runner"
	"github.com/sequencestream/evalexec/summary"
	"github.com/sequencestream/evalexec/validate"
	"github.com/sequencestream/evalexec/version"
)

// StepRun is the step name reported for run-level faults.
const StepRun = "run"

// Clock supplies the current time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// config holds the resolved options.
type config struct {
	registry  *grader.Registry
	clock     Clock
	diag      io.Writer
	judge     validate.JudgeChecker
	debugLogs bool
}

// Option adjusts how a run executes.
type Option func(*config)

// WithGraderRegistry supplies the registry to resolve the Grader entry from.
// The default holds the built-in Graders plus anything registered globally.
func WithGraderRegistry(r *grader.Registry) Option {
	return func(c *config) {
		if r != nil {
			c.registry = r
		}
	}
}

// WithClock supplies the time source for record timestamps, so that golden
// file comparisons can be made stable.
func WithClock(clk Clock) Option {
	return func(c *config) {
		if clk != nil {
			c.clock = clk
		}
	}
}

// WithDiagnosticWriter directs progress and warnings somewhere.
//
// The default discards them. A library must not assume it owns the host
// process's stderr, so nothing is written there unless a caller asks.
func WithDiagnosticWriter(w io.Writer) Option {
	return func(c *config) {
		if w != nil {
			c.diag = w
		}
	}
}

// WithDebugLogs retains the raw Judge exchange for every sample, not only for
// failures. It is off by default: a successful run of ten thousand samples
// would otherwise leave ten thousand prompt echoes on disk.
func WithDebugLogs() Option {
	return func(c *config) { c.debugLogs = true }
}

// WithJudgeChecker replaces the transport-level Judge pre-check. The default
// constructs the real client, so an unusable endpoint fails before the first
// call rather than on it.
func WithJudgeChecker(j validate.JudgeChecker) Option {
	return func(c *config) { c.judge = j }
}

// judgeChecker returns the pre-check to use for this request.
func (c *config) judgeChecker(req *evalspec.EvalRequest) validate.JudgeChecker {
	if c.judge != nil {
		return c.judge
	}

	return judge.Checker{Concurrency: concurrencyOf(req)}
}

// buildJudge constructs the Judge when the configuration calls for one. By the
// time it is called the pre-check has already confirmed the configuration is
// usable, so a failure here would mean the environment changed mid-run.
func buildJudge(req *evalspec.EvalRequest) (judge.Judge, error) {
	if req.JudgeModel == nil {
		return nil, nil
	}

	return judge.New(req.JudgeModel, concurrencyOf(req))
}

func concurrencyOf(req *evalspec.EvalRequest) int {
	if req.Execution == nil || req.Execution.Concurrency < 1 {
		return 1
	}

	return req.Execution.Concurrency
}

// graderResolver adapts the registry to the pre-check contract.
//
// It lives here rather than in the grader package so that grader does not have
// to import validate — the validator sits above the registry, and having the
// lower package reach up would invert the layering.
type graderResolver struct {
	registry *grader.Registry
}

func (r *graderResolver) Resolve(spec evalspec.GraderSpec) (declaration.Declaration, error) {
	decl, err := r.registry.Resolve(spec, grader.Deps{})
	if err != nil {
		if errors.Is(err, grader.ErrUnknownEntry) {
			return declaration.Declaration{}, fmt.Errorf("%w: %q", validate.ErrUnknownEntry, spec.Entry)
		}

		return declaration.Declaration{}, err
	}

	return decl, nil
}

// Run performs one evaluation.
//
// On a pre-check failure it returns (nil, err) and nothing has been written.
// On a completed or cancelled run it returns the result and a nil error — a
// run in which every evaluation failed still succeeded as a run. On a
// run-level fault it returns both a result carrying status "failed" and an
// error, so a caller has the diagnosis as well as whatever was learned.
func Run(ctx context.Context, req *evalspec.EvalRequest, opts ...Option) (*evalspec.EvalResult, error) {
	cfg := &config{registry: grader.Default(), clock: systemClock{}, diag: io.Discard}
	for _, o := range opts {
		o(cfg)
	}

	report, err := validate.All(req, validate.Options{
		// Declarations are resolved without a Judge. Step 3 asks a Grader what
		// it needs; whether the Judge it needs is usable is step 4's question,
		// and answering it early would report the failure in the wrong step.
		Grader: &graderResolver{registry: cfg.registry},
		Judge:  cfg.judgeChecker(req),
		Diag:   cfg.diag,
	})
	if err != nil {
		return nil, err
	}

	snapshot, err := redact.Request(req)
	if err != nil {
		return nil, err
	}

	// The Grader is built before the output directory exists, so a bad entry
	// name or an unusable parameter is still a pre-check failure that leaves
	// nothing behind.
	j, err := buildJudge(req)
	if err != nil {
		return nil, evalerr.Wrap(evalerr.KindPrecheck, validate.StepJudgeModel, err, "")
	}

	g, err := cfg.registry.Build(req.Grader, grader.Deps{Judge: j})
	if err != nil {
		return nil, evalerr.Wrap(evalerr.KindPrecheck, validate.StepGraderDeclaration, err, "")
	}

	datasetSum, err := fileSHA256(req.Dataset.Path)
	if err != nil {
		return nil, evalerr.Wrap(evalerr.KindRuntime, StepRun, err, "cannot checksum the dataset")
	}

	// Nothing exists on disk until this point, which is what makes a rejected
	// run leave no trace.
	dir, err := result.Create(req.OutputDir, req.EvalID)
	if err != nil {
		return nil, err
	}

	res, err := execute(ctx, cfg, req, g, j, report, dir, snapshot, datasetSum)
	if err != nil {
		// A run-level fault leaves nothing publishable. Discarding is not
		// tidiness: a partial directory would be indistinguishable from a
		// trustworthy one.
		_ = dir.Discard()

		return res, err
	}

	if err := dir.Publish(); err != nil {
		_ = dir.Discard()

		return res, err
	}

	return res, nil
}

// execute runs the samples and assembles the result inside the pending
// directory.
func execute(
	ctx context.Context,
	cfg *config,
	req *evalspec.EvalRequest,
	g grader.Grader,
	j judge.Judge,
	report *validate.Report,
	dir *result.Dir,
	snapshot *redact.Snapshot,
	datasetSum string,
) (*evalspec.EvalResult, error) {
	started := evalspec.NewTimestamp(cfg.clock.Now())
	startWall := time.Now()

	records, err := dir.Create(result.FileRecords)
	if err != nil {
		return nil, err
	}

	logs := result.NewLogWriter(dir)

	outcome, runErr := runner.Run(ctx, runner.Config{
		EvalID:        req.EvalID,
		TaskID:        req.TaskID,
		DatasetPath:   req.Dataset.Path,
		Grader:        g,
		GraderID:      req.Grader.ID,
		GraderVersion: req.Grader.Version,
		Parameters:    req.Grader.Parameters,
		GraderTimeout: time.Duration(req.Grader.TimeoutMS) * time.Millisecond,
		Concurrency:   concurrencyOf(req),
		FailFast:      req.Execution != nil && req.Execution.FailFast,
		Clock:         cfg.clock,
		Recorder:      recorderOf(j),
		Logs:          logs,
		KeepAllLogs:   cfg.debugLogs,
		Diag:          cfg.diag,
	}, records)

	if closeErr := records.Close(); closeErr != nil && runErr == nil {
		runErr = evalerr.Wrap(evalerr.KindOutput, StepRun, closeErr, "cannot close records")
	}

	if runErr != nil {
		return nil, runErr
	}

	// The line-count identity is checked here rather than trusted. Every
	// stopping path must produce one record per dataset row, and a silent
	// mismatch would make a result look complete when it is not.
	if outcome.Counts.Total != report.Rows {
		return nil, evalerr.Runtime(StepRun,
			"records.jsonl has %d lines but the dataset has %d rows; the result would not be trustworthy",
			outcome.Counts.Total, report.Rows)
	}

	finished := evalspec.NewTimestamp(cfg.clock.Now())
	status, stopReason := summary.Status(outcome.Counts.Skipped, outcome.Stopped, outcome.StopReason)

	res := &evalspec.EvalResult{
		SpecVersion: evalspec.SpecVersion,
		EvalID:      req.EvalID,
		TaskID:      req.TaskID,
		Status:      status,
		StopReason:  stopReason,
		Request:     snapshot.JSON,
		Artifacts:   artifactsOf(logs),
		Counts:      outcome.Counts,
		Evaluation:  outcome.Evaluation,
		Usage: evalspec.ResultUsage{JudgeModel: evalspec.ModelUsage{
			InputTokens:     outcome.Usage.JudgeInputTokens,
			OutputTokens:    outcome.Usage.JudgeOutputTokens,
			CacheReadTokens: outcome.Usage.JudgeCacheReadTokens,
			ReasoningTokens: outcome.Usage.JudgeReasoningTokens,
		}},
		Provenance: evalspec.Provenance{
			DatasetSHA256:     datasetSum,
			EvalRequestSHA256: snapshot.SHA256,
			Implementation:    evalspec.Implementation{Name: version.Name, Version: version.Version},
		},
		StartedAt:  started,
		FinishedAt: finished,
		DurationMS: time.Since(startWall).Milliseconds(),
	}

	// The last line of defence. A result whose own numbers disagree is worse
	// than no result, so it is never written — this package does not assume
	// the accumulator got it right, and a downstream caller assembling a
	// result by hand gets checked the same way.
	if err := res.Validate(); err != nil {
		return failedResult(res, err), evalerr.Wrap(evalerr.KindRuntime, StepRun, err,
			"the summary does not satisfy the counting identities")
	}

	if err := writeJSON(dir, result.FileResult, res); err != nil {
		return res, err
	}

	return res, nil
}

// artifactsOf names the files a run produced. The logs directory is listed
// only when something was written to it: naming an absent directory would send
// a reader looking for diagnostics that do not exist.
func artifactsOf(logs *result.LogWriter) evalspec.Artifacts {
	a := evalspec.Artifacts{Records: result.FileRecords}
	if logs.HasLogs() {
		a.Logs = result.DirLogs
	}

	return a
}

// recorderOf exposes the Judge's transport recorder, when it has one. A custom
// Judge implementation need not, in which case no exchanges are retained.
func recorderOf(j judge.Judge) runner.Recorder {
	r, ok := j.(interface {
		Recorder() *transport.Recorder
	})
	if !ok {
		return nil
	}

	return r.Recorder()
}

// failedResult marks a result as a run-level fault, for the caller that wants
// the partial picture alongside the error.
func failedResult(res *evalspec.EvalResult, cause error) *evalspec.EvalResult {
	if res == nil {
		return nil
	}

	out := *res
	out.Status = evalspec.RunFailed
	out.Error = &evalspec.RunError{Code: "summary_invariant", Message: cause.Error()}

	return &out
}

func writeJSON(dir *result.Dir, name string, v any) error {
	f, err := dir.Create(name)
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return evalerr.Wrap(evalerr.KindOutput, StepRun, err, "cannot write %s", name)
	}

	return nil
}

// fileSHA256 hashes the raw bytes of a file. The dataset digest is taken over
// the file as it is, not over a re-serialization, so any implementation in any
// language arrives at the same value.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer func() { _ = f.Close() }()

	return hashReader(f)
}

// hashReader returns the hex sha256 of everything r yields.
func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
