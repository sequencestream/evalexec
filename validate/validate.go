// Package validate runs the six pre-checks that must all pass before any
// Grader or Judge is called.
//
// The order of the checks is part of the specification, not an implementation
// detail. In particular the output directory is checked before the dataset, so
// that a run with both a directory conflict and a malformed dataset reports the
// directory conflict — exit code 4, not 2. A natural implementation that
// validates its inputs before touching its outputs gets this backwards while
// passing every other check.
//
// Nothing here writes anything. A rejected run leaves no result directory
// behind, not even a temporary one, which is why directory creation happens
// only after all six checks have passed.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise.
package validate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sequencestream/evalexec/dataset"
	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader/declaration"
)

// The six pre-check step names, in the order they run. They are reported in
// diagnostics and asserted by the pre-check fixtures, so they are a stable
// contract rather than free text.
const (
	StepArguments         = "arguments"
	StepOutputDirConflict = "output_dir_conflict"
	StepGraderDeclaration = "grader_declaration"
	StepJudgeModel        = "judge_model"
	StepDatasetParse      = "dataset_parse"
	StepSessionRequires   = "session_requires"
)

// JudgeChecker validates a Judge configuration by whatever means the transport
// requires — typically by constructing the client, so that an unusable
// endpoint fails here rather than on the first call.
//
// It is an interface because the judge package lands later than this one. When
// no checker is supplied, step 4 performs only the structural checks it can do
// on its own.
type JudgeChecker interface {
	Check(spec *evalspec.JudgeModelSpec) error
}

// Options configures a validation pass.
type Options struct {
	// Judge, when set, extends step 4 with a transport-level check.
	Judge JudgeChecker
	// Index records case_ids; nil means a fresh in-memory index.
	Index dataset.CaseIndex
	// Diag receives warnings that do not stop the run; nil discards them.
	Diag io.Writer
}

// Report is what a successful validation learned, so the execution phase does
// not have to rediscover it.
type Report struct {
	// Rows is the dataset line count, which becomes counts.total.
	Rows int
	// Requires is the Grader's effective requirement list, after parameter
	// overrides were taken into account.
	Requires []evalspec.SessionField
	// Declaration is the resolved built-in Grader declaration, or nil for an
	// external protocol.
	Declaration *declaration.Declaration
}

// All runs the six pre-checks in their fixed order and stops at the first
// failure.
func All(req *evalspec.EvalRequest, opts Options) (*Report, error) {
	diag := opts.Diag
	if diag == nil {
		diag = io.Discard
	}

	// Step 1: the request is structurally sound.
	if err := req.Validate(); err != nil {
		return nil, evalerr.Wrap(evalerr.KindArgument, StepArguments, err, "")
	}

	// Step 2: the output directory is free. This must precede the dataset
	// checks: when both fail, the specification requires the directory
	// conflict to win.
	if err := checkOutputDir(req.OutputDir); err != nil {
		return nil, err
	}

	// Step 3: the Grader declares itself completely and consistently.
	report := &Report{}

	decl, requires, err := checkGraderDeclaration(&req.Grader)
	if err != nil {
		return nil, err
	}

	report.Declaration, report.Requires = decl, requires

	// Step 4: a Judge is configured when the Grader says it needs one.
	if err := checkJudgeModel(req, opts.Judge); err != nil {
		return nil, err
	}

	// Steps 5 and 6 share one pass over the dataset.
	rows, err := checkDataset(req, requires, opts.Index)
	if err != nil {
		return nil, err
	}

	report.Rows = rows

	if rows == 0 {
		_, _ = fmt.Fprintf(diag, "evalexec: dataset %s has no rows; the run will produce an empty result\n", req.Dataset.Path)
	}

	return report, nil
}

// checkOutputDir accepts a directory that does not exist or is empty, and
// rejects anything else. There is no --force: silently overwriting a result
// directory would destroy evidence a caller may still need.
func checkOutputDir(dir string) error {
	entries, err := os.ReadDir(dir)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return evalerr.Wrap(evalerr.KindOutput, StepOutputDirConflict, err,
			"cannot inspect output directory %s", dir)
	case len(entries) > 0:
		return evalerr.Output(StepOutputDirConflict,
			"output directory %s exists and is not empty (%d entries); "+
				"refusing to overwrite it, and there is no --force", dir, len(entries))
	default:
		return nil
	}
}

// checkGraderDeclaration verifies the Grader's self-description and resolves
// what it effectively requires.
//
// For a built-in Grader the declaration is fixed, so a configuration that
// disagrees with it is a configuration error — the Grader's own code decides
// what it needs, not the file describing it. For an external Grader the
// declaration comes from the configuration and is taken at its word; querying
// the external process for it would make the pre-check depend on the very
// thing it is meant to validate before contacting.
func checkGraderDeclaration(g *evalspec.GraderSpec) (*declaration.Declaration, []evalspec.SessionField, error) {
	if g.Protocol != evalspec.GraderBuiltin {
		return nil, g.Requires, nil
	}

	decl, ok := declaration.Lookup(g.Entry)
	if !ok {
		return nil, nil, evalerr.Precheck(StepGraderDeclaration,
			"unknown builtin grader entry %q; known entries are %s",
			g.Entry, strings.Join(declaration.Entries(), ", "))
	}

	// The effective requirements may depend on parameters — llm_judge picks up
	// reference or trajectory when asked to use them — so they are derived
	// after any --grader-param override has been folded in.
	requires, err := decl.EffectiveRequires(g.Parameters)
	if err != nil {
		return nil, nil, evalerr.Wrap(evalerr.KindPrecheck, StepGraderDeclaration, err,
			"grader %q parameters are invalid", g.Entry)
	}

	if !sameFields(g.Requires, requires) {
		return nil, nil, evalerr.Precheck(StepGraderDeclaration,
			"grader %q requires %s, but the configuration declares %s",
			g.Entry, formatFields(requires), formatFields(g.Requires))
	}

	if g.RequiresJudge != decl.RequiresJudge {
		return nil, nil, evalerr.Precheck(StepGraderDeclaration,
			"grader %q has requires_judge=%v, but the configuration declares %v",
			g.Entry, decl.RequiresJudge, g.RequiresJudge)
	}

	return &decl, requires, nil
}

// checkJudgeModel verifies the Judge configuration when one is needed.
func checkJudgeModel(req *evalspec.EvalRequest, checker JudgeChecker) error {
	if !req.Grader.RequiresJudge {
		return nil
	}

	spec := req.JudgeModel
	if spec == nil {
		return evalerr.Precheck(StepJudgeModel,
			"grader %q requires a Judge but no judge_model was supplied", req.Grader.ID)
	}

	// An endpoint is mandatory for every transport: the HTTP protocols need a
	// URL and stdio-jsonl needs an executable.
	if spec.Endpoint == "" {
		return evalerr.Precheck(StepJudgeModel,
			"judge_model.endpoint is required for protocol %q", spec.Protocol)
	}

	// The credential must resolve now.
	//
	// Falling through with an empty value would let the client library pick up
	// some other key from the environment, and the run would then call a
	// different service than provenance records. A result whose configuration
	// does not match what actually answered is worse than no result.
	if spec.Auth.Type == evalspec.AuthBearerEnv {
		if os.Getenv(spec.Auth.Env) == "" {
			return evalerr.Precheck(StepJudgeModel,
				"judge_model.auth.env names %s, but that environment variable is empty", spec.Auth.Env)
		}
	}

	if checker == nil {
		return nil
	}

	if err := checker.Check(spec); err != nil {
		return evalerr.Wrap(evalerr.KindPrecheck, StepJudgeModel, err, "judge_model is not usable")
	}

	return nil
}

// checkDataset performs steps 5 and 6 in a single streaming pass: every line
// parses with a unique, non-empty case_id, and every session carries the
// fields the Grader declared.
//
// This is the first of the two passes over the dataset. It keeps only the
// case_id index and the row count, never the rows themselves, so a dataset is
// bounded by disk rather than by memory.
func checkDataset(req *evalspec.EvalRequest, requires []evalspec.SessionField, index dataset.CaseIndex) (int, error) {
	r, err := dataset.Open(req.Dataset.Path)
	if err != nil {
		return 0, evalerr.Wrap(evalerr.KindPrecheck, StepDatasetParse, err, "cannot read dataset")
	}

	defer func() { _ = r.Close() }()

	if index == nil {
		index = dataset.NewMemoryIndex()
	}

	rows := 0

	for {
		s, seq, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return 0, evalerr.Wrap(evalerr.KindPrecheck, StepDatasetParse, err, "dataset is not valid JSONL")
		}

		rows = seq

		if err := index.Add(s.CaseID); err != nil {
			return 0, evalerr.Wrap(evalerr.KindPrecheck, StepDatasetParse, err, "line %d", seq)
		}

		// Step 6, checked on the same pass. Presence is what matters: an
		// explicitly null output means the agent produced none, which still
		// satisfies a requirement for output.
		if missing := s.MissingFields(requires); len(missing) > 0 {
			return 0, evalerr.Precheck(StepSessionRequires,
				"line %d (case %q) is missing the required field %s declared by the grader "+
					"(a field present with a null value would satisfy this; the key is absent)",
				seq, s.CaseID, formatFields(missing))
		}
	}

	return rows, nil
}

// sameFields compares two requirement lists as sets: the declaration order is
// not meaningful.
func sameFields(a, b []evalspec.SessionField) bool {
	if len(a) != len(b) {
		return false
	}

	seen := make(map[evalspec.SessionField]int, len(a))
	for _, f := range a {
		seen[f]++
	}

	for _, f := range b {
		seen[f]--
		if seen[f] < 0 {
			return false
		}
	}

	return true
}

func formatFields(fields []evalspec.SessionField) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = string(f)
	}

	return "[" + strings.Join(parts, " ") + "]"
}
