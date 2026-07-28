// Package cli turns a command line into a normalized EvalRequest.
//
// There are no subcommands. One invocation carries one request, and the flags
// here are the whole surface: identifiers, the dataset, the single Grader, an
// optional Judge model, and where to write the result.
//
// # Stability
//
// Internal package: no compatibility promise. It exists to serve cmd/evalexec;
// downstream code calls evalexec.Run with an evalspec.EvalRequest instead.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
)

// StepArguments is the pre-check step name reported for argument failures.
const StepArguments = "arguments"

// secretFlags are rejected with a pointed message rather than the flag
// package's generic "not defined".
//
// A credential must be referenced by environment variable name, never passed
// on a command line where it lands in shell history and process listings.
// Saying so explicitly stops a user assuming they merely guessed the flag
// name wrong and going looking for the right one.
var secretFlags = []string{
	"--api-key", "--apikey", "--key", "--token", "--secret",
	"--password", "--auth-token", "--bearer",
}

// Options configures parsing. The zero value uses the system clock and
// generates UUIDv7 identifiers.
type Options struct {
	// IDGenerator supplies an eval_id when none was given.
	IDGenerator IDGenerator
	// WorkingDir resolves relative paths given on the command line; empty
	// means the process working directory.
	WorkingDir string
}

// Parse builds a normalized EvalRequest from command line arguments.
//
// Diagnostics — such as a notice that a flag overrode a value from the request
// file — are written to diag. It is never os.Stderr here: this package may run
// inside a host process that owns its own output.
func Parse(args []string, diag io.Writer, opts Options) (*evalspec.EvalRequest, error) {
	if diag == nil {
		diag = io.Discard
	}

	if err := rejectSecretFlags(args); err != nil {
		return nil, err
	}

	f, err := parseFlags(args, diag)
	if err != nil {
		return nil, err
	}

	req, err := f.buildRequest(diag)
	if err != nil {
		return nil, err
	}

	if err := normalize(req, opts, f.workingDir(opts)); err != nil {
		return nil, err
	}

	return req, nil
}

// rejectSecretFlags reports a credential-bearing flag before the flag package
// can dismiss it as merely unknown.
func rejectSecretFlags(args []string) error {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")

		for _, s := range secretFlags {
			if name == s {
				return evalerr.Argument(StepArguments,
					"%s is not supported: a credential must be referenced by environment variable name "+
						"in judge_model.auth, never passed on the command line", s)
			}
		}
	}

	return nil
}

// flags holds the raw command line values before merging.
type flags struct {
	evalID      onceString
	taskID      onceString
	dataset     onceString
	grader      onceString
	judgeModel  onceString
	outputDir   onceString
	requestPath onceString

	judgeParams  keyValues
	graderParams keyValues

	concurrency int
	seed        int
	seedSet     bool
	failFast    bool
}

// workingDir resolves relative paths against the request file's directory when
// one was given, and against the process working directory otherwise.
//
// A path inside a request file is naturally relative to that file: the file is
// what someone checked into a repository next to their dataset. Resolving it
// against whatever directory the command happened to run from would make the
// same request file mean different things.
func (f *flags) workingDir(opts Options) string {
	if f.requestPath.value != "" {
		return filepath.Dir(f.requestPath.value)
	}

	if opts.WorkingDir != "" {
		return opts.WorkingDir
	}

	wd, err := os.Getwd()
	if err != nil {
		return "."
	}

	return wd
}

func parseFlags(args []string, diag io.Writer) (*flags, error) {
	var f flags

	f.evalID.name = "eval-id"
	f.taskID.name = "task-id"
	f.dataset.name = "dataset"
	f.grader.name = "grader"
	f.judgeModel.name = "judge-model"
	f.outputDir.name = "output-dir"
	f.requestPath.name = "request"

	fs := flag.NewFlagSet("evalexec", flag.ContinueOnError)
	fs.SetOutput(diag)

	fs.Var(&f.evalID, "eval-id", "globally unique id for this evaluation; generated when absent")
	fs.Var(&f.taskID, "task-id", "correlation key echoed into the result")
	fs.Var(&f.dataset, "dataset", "JSONL file of agent sessions")
	fs.Var(&f.grader, "grader", "the single Grader configuration; may only be given once")
	fs.Var(&f.judgeModel, "judge-model", "Judge model configuration; required when the Grader needs a Judge")
	fs.Var(&f.outputDir, "output-dir", "directory to write the result into")
	fs.Var(&f.requestPath, "request", "complete EvalRequest JSON; flags override it")
	fs.Var(&f.judgeParams, "judge-param", "override a Judge parameter as key=value; repeatable")
	fs.Var(&f.graderParams, "grader-param", "override a Grader parameter as key=value; repeatable")

	fs.IntVar(&f.concurrency, "concurrency", 0, "samples evaluated at once (default 1)")
	fs.IntVar(&f.seed, "seed", 0, "recorded in provenance; not forwarded to the Judge")
	fs.BoolVar(&f.failFast, "fail-fast", false, "stop dispatching after the first failed evaluation")

	// A version flag is accepted here too so that `--version` combined with
	// other flags still reports the version rather than a parse error.
	fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		// The flag package has already written its complaint to diag.
		return nil, &evalerr.Error{
			Kind: evalerr.KindArgument, Step: StepArguments, Err: err, Reported: true,
		}
	}

	if rest := fs.Args(); len(rest) > 0 {
		return nil, evalerr.Argument(StepArguments,
			"unexpected positional argument %q: evalexec takes flags only and has no subcommands", rest[0])
	}

	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "seed" {
			f.seedSet = true
		}
	})

	return &f, nil
}

// buildRequest merges the request file with the flags, flags winning.
func (f *flags) buildRequest(diag io.Writer) (*evalspec.EvalRequest, error) {
	req := &evalspec.EvalRequest{}

	if f.requestPath.value != "" {
		data, err := os.ReadFile(f.requestPath.value)
		if err != nil {
			return nil, evalerr.Wrap(evalerr.KindArgument, StepArguments, err,
				"cannot read --request %s", f.requestPath.value)
		}

		if err := json.Unmarshal(data, req); err != nil {
			return nil, evalerr.Wrap(evalerr.KindArgument, StepArguments, err,
				"cannot parse --request %s", f.requestPath.value)
		}
	}

	overrideString(diag, "eval_id", &req.EvalID, f.evalID.value)
	overrideString(diag, "task_id", &req.TaskID, f.taskID.value)
	overrideString(diag, "dataset.path", &req.Dataset.Path, f.dataset.value)
	overrideString(diag, "output_dir", &req.OutputDir, f.outputDir.value)

	if err := f.applyGraderFile(diag, req); err != nil {
		return nil, err
	}

	if err := f.applyJudgeFile(diag, req); err != nil {
		return nil, err
	}

	if err := f.applyExecution(diag, req); err != nil {
		return nil, err
	}

	return req, f.applyParamOverrides(req)
}

func (f *flags) applyGraderFile(diag io.Writer, req *evalspec.EvalRequest) error {
	if f.grader.value == "" {
		return nil
	}

	var g evalspec.GraderSpec
	if err := readJSONFile(f.grader.value, &g); err != nil {
		return evalerr.Wrap(evalerr.KindArgument, StepArguments, err, "cannot load --grader %s", f.grader.value)
	}

	if req.Grader.ID != "" {
		_, _ = fmt.Fprintf(diag, "evalexec: --grader overrides the grader in the request file\n")
	}

	req.Grader = g

	return nil
}

func (f *flags) applyJudgeFile(diag io.Writer, req *evalspec.EvalRequest) error {
	if f.judgeModel.value == "" {
		return nil
	}

	var j evalspec.JudgeModelSpec
	if err := readJSONFile(f.judgeModel.value, &j); err != nil {
		return evalerr.Wrap(evalerr.KindArgument, StepArguments, err,
			"cannot load --judge-model %s", f.judgeModel.value)
	}

	if req.JudgeModel != nil {
		_, _ = fmt.Fprintf(diag, "evalexec: --judge-model overrides the judge_model in the request file\n")
	}

	req.JudgeModel = &j

	return nil
}

func (f *flags) applyExecution(diag io.Writer, req *evalspec.EvalRequest) error {
	if f.concurrency == 0 && !f.seedSet && !f.failFast {
		return nil
	}

	if req.Execution == nil {
		req.Execution = &evalspec.Execution{}
	}

	if f.concurrency != 0 {
		if f.concurrency < 1 {
			return evalerr.Argument(StepArguments, "--concurrency must be at least 1, got %d", f.concurrency)
		}

		if req.Execution.Concurrency != 0 && req.Execution.Concurrency != f.concurrency {
			_, _ = fmt.Fprintf(diag, "evalexec: --concurrency overrides execution.concurrency from the request file\n")
		}

		req.Execution.Concurrency = f.concurrency
	}

	if f.seedSet {
		seed := f.seed
		req.Execution.Seed = &seed
	}

	if f.failFast {
		req.Execution.FailFast = true
	}

	return nil
}

// applyParamOverrides folds --grader-param and --judge-param into the request.
//
// These are applied last, after the files are loaded, so that a parameter
// override can change what the Grader declares it requires — which is why
// requires is derived after this point and not before.
func (f *flags) applyParamOverrides(req *evalspec.EvalRequest) error {
	if len(f.graderParams.pairs) > 0 {
		if req.Grader.Parameters == nil {
			req.Grader.Parameters = map[string]any{}
		}

		for _, p := range f.graderParams.pairs {
			req.Grader.Parameters[p.key] = p.value
		}
	}

	if len(f.judgeParams.pairs) == 0 {
		return nil
	}

	if req.JudgeModel == nil {
		return evalerr.Argument(StepArguments,
			"--judge-param was given but there is no judge_model to apply it to")
	}

	if req.JudgeModel.Parameters == nil {
		req.JudgeModel.Parameters = map[string]any{}
	}

	for _, p := range f.judgeParams.pairs {
		req.JudgeModel.Parameters[p.key] = p.value
	}

	return nil
}

// overrideString applies a flag value over a request-file value, announcing
// the override so a surprised user can see which one won.
func overrideString(diag io.Writer, field string, dst *string, value string) {
	if value == "" {
		return
	}

	if *dst != "" && *dst != value {
		_, _ = fmt.Fprintf(diag, "evalexec: flag overrides %s from the request file (%q -> %q)\n", field, *dst, value)
	}

	*dst = value
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, v)
}

// normalize fills in the defaults and makes paths absolute, so that everything
// downstream — and the request snapshot in the result — sees one canonical
// form regardless of how the run was invoked.
func normalize(req *evalspec.EvalRequest, opts Options, workDir string) error {
	if req.SpecVersion == "" {
		req.SpecVersion = evalspec.SpecVersion
	}

	if req.Dataset.Path != "" {
		req.Dataset.Path = absolute(workDir, req.Dataset.Path)
	}

	if req.OutputDir != "" {
		req.OutputDir = absolute(workDir, req.OutputDir)
	}

	if req.Execution == nil {
		req.Execution = &evalspec.Execution{}
	}

	if req.Execution.Concurrency == 0 {
		req.Execution.Concurrency = 1
	}

	if req.EvalID != "" {
		return nil
	}

	gen := opts.IDGenerator
	if gen == nil {
		gen = UUIDv7Generator{}
	}

	id, err := gen.NewID()
	if err != nil {
		return evalerr.Wrap(evalerr.KindArgument, StepArguments, err, "")
	}

	req.EvalID = id

	return nil
}

func absolute(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}

	return filepath.Clean(filepath.Join(base, p))
}

// onceString is a flag value that rejects a second occurrence.
//
// The standard flag package silently keeps the last value when a flag repeats,
// which for --grader would quietly run a different Grader than the caller
// listed first. Counting occurrences is the only way to reject it, and the
// acceptance criteria require the rejection to happen before any Grader or
// Judge is called.
type onceString struct {
	name  string
	value string
	count int
}

func (o *onceString) String() string { return o.value }

func (o *onceString) Set(v string) error {
	o.count++

	if o.count > 1 {
		return fmt.Errorf("--%s may only be given once (got it %d times)", o.name, o.count)
	}

	o.value = v

	return nil
}

// keyValue is one key=value override with its value already decoded.
type keyValue struct {
	key   string
	value any
}

// keyValues collects repeated key=value flags.
type keyValues struct {
	pairs []keyValue
}

func (k *keyValues) String() string {
	parts := make([]string, len(k.pairs))
	for i, p := range k.pairs {
		parts[i] = fmt.Sprintf("%s=%v", p.key, p.value)
	}

	return strings.Join(parts, ",")
}

func (k *keyValues) Set(v string) error {
	key, raw, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("expected key=value, got %q", v)
	}

	if key == "" {
		return fmt.Errorf("empty key in %q", v)
	}

	value, err := parseScalar(raw)
	if err != nil {
		return err
	}

	k.pairs = append(k.pairs, keyValue{key: key, value: value})

	return nil
}

// ErrComplexValue reports an override whose value is an array or object.
var ErrComplexValue = errors.New("complex values must be written into the request or component file")

// parseScalar decodes an override value as a JSON scalar.
//
// A value that does not parse as JSON at all falls back to a plain string, so
// the common case — model=gpt-4o, rubric=whatever — needs no shell quoting. A
// value that parses into an array or object is rejected rather than accepted:
// the specification confines overrides to scalars, and silently accepting
// structure here would create a second, undocumented way to configure a run.
func parseScalar(raw string) (any, error) {
	var v any

	// Not valid JSON at all, so treat it as a plain string. This is the
	// common case — model=gpt-4o — and requiring shell quoting for it would
	// be a poor trade for the rare quoted-string override.
	if !json.Valid([]byte(raw)) {
		return raw, nil
	}

	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("cannot decode %q: %w", raw, err)
	}

	switch v.(type) {
	case nil, bool, float64, string:
		return v, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrComplexValue, raw)
	}
}
