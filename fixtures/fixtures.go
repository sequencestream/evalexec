// Package fixtures carries the shared protocol test cases that any EvalExec
// implementation must satisfy.
//
// They live in fixtures/ rather than testdata/ deliberately. The acceptance
// criteria require a Python implementation and a Go one to agree on the same
// cases, so the data has to be reachable from outside Go entirely — a stable
// repository path a `git clone` or a release archive can pick up. A testdata
// directory is also treated specially by the Go toolchain, which this data
// should not be: it is a published asset, not a private test aid.
//
// Go callers read them through FS; other languages read the same files from
// the fixtures/data directory in the repository.
//
// # Stability
//
// L1 protocol. These cases share the lifecycle of evalspec.SpecVersion:
// a case may gain optional fields within evalexec/v1alpha1, but changing what
// an existing case asserts requires a new spec version.
package fixtures

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// FS holds every fixture, rooted so that a case's files are at
// "data/<case>/...". Downstream Go code can use it to check its own Grader
// against the shared cases.
//
//go:embed all:data
var FS embed.FS

// Case names. A run-shaped case has request.json, dataset.jsonl and an
// expected/ directory; the pre-check case is shaped differently, see
// PrecheckCase.
const (
	// CaseExactMatchAllPass is the simplest end-to-end run: a rule Grader,
	// every sample succeeding, no Judge involved.
	CaseExactMatchAllPass = "f01-exact-match-all-pass"
	// CaseMixedSuccessFail mixes successes with failures, including an
	// insufficient_evidence failure, to exercise fail_by_code and the rule
	// that a failure is not a zero.
	CaseMixedSuccessFail = "f02-mixed-success-fail"
	// CaseLLMJudgeBasic drives the llm_judge Grader against recorded Judge
	// responses, so CI never depends on a live model.
	CaseLLMJudgeBasic = "f03-llm-judge-basic"
	// CaseFailFastCancelled stops on the first failed evaluation and
	// backfills the rest as skipped.
	CaseFailFastCancelled = "f04-fail-fast-cancelled"
	// CaseInterruptCancelled is interrupted mid-run and must still publish a
	// complete, line-for-line consistent result.
	CaseInterruptCancelled = "f05-interrupt-cancelled"
	// CasePrecheckFailures collects the pre-check failures, each in a
	// subdirectory. These produce no result directory at all, so they assert
	// an exit code rather than a result.
	CasePrecheckFailures = "f06-precheck-failures"
)

// FakeKeyEnv is the environment variable the fixtures reference for Judge
// credentials. Nothing in this repository holds its value: the CI secret-leak
// scan sets it to a sentinel and then asserts that the sentinel appears in no
// result directory and no log.
const FakeKeyEnv = "EVALEXEC_FIXTURE_KEY"

// File names within a run-shaped case.
const (
	FileRequest        = "request.json"
	FileDataset        = "dataset.jsonl"
	FileExpectedResult = "expected/result.json"
	FileExpectedRecord = "expected/records.jsonl"
	// FileJudgeResponses holds recorded Judge replies, one JSON object per
	// line, for cases that involve a Judge.
	FileJudgeResponses = "judge-responses.jsonl"
	// FileExpectedFailure holds the expected exit code and failing step of a
	// pre-check case.
	FileExpectedFailure = "expected/failure.json"
	// FileExpectedInvariants holds the assertions of a case whose exact
	// result is not predictable.
	FileExpectedInvariants = "expected/invariants.json"
)

// goldenCases lists the cases with a fully predictable result, comparable
// against expected/result.json and expected/records.jsonl.
var goldenCases = []string{
	CaseExactMatchAllPass,
	CaseMixedSuccessFail,
	CaseLLMJudgeBasic,
	CaseFailFastCancelled,
}

// GoldenCases returns the cases whose complete result is predictable and can
// be compared against a golden file.
//
// CaseInterruptCancelled is excluded: where an interrupt lands depends on
// scheduling, so how many samples completed genuinely is not predictable.
// Giving it a golden file would mean inventing a stopping point and then
// testing that the implementation reproduces the invention. It carries
// expected/invariants.json instead.
func GoldenCases() []string {
	return append([]string(nil), goldenCases...)
}

// RunCases returns every case shaped as a complete run, golden or not.
func RunCases() []string {
	return append(GoldenCases(), CaseInterruptCancelled)
}

// Read returns one file of one case, e.g.
// Read(CaseExactMatchAllPass, FileDataset).
func Read(caseName, file string) ([]byte, error) {
	data, err := FS.ReadFile(path.Join("data", caseName, file))
	if err != nil {
		return nil, fmt.Errorf("fixtures: read %s/%s: %w", caseName, file, err)
	}

	return data, nil
}

// Dir returns the embedded path of a case, for callers that want to walk it.
func Dir(caseName string) string {
	return path.Join("data", caseName)
}

// PrecheckCases returns the subdirectory names of the pre-check case, sorted.
// Each one is a request that must fail validation before any Grader or Judge
// is called.
func PrecheckCases() ([]string, error) {
	entries, err := fs.ReadDir(FS, Dir(CasePrecheckFailures))
	if err != nil {
		return nil, fmt.Errorf("fixtures: list pre-check cases: %w", err)
	}

	names := make([]string, 0, len(entries))

	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}

	sort.Strings(names)

	return names, nil
}

// ReadPrecheck returns one file of one pre-check subcase.
func ReadPrecheck(subcase, file string) ([]byte, error) {
	return Read(CasePrecheckFailures, path.Join(subcase, file))
}

// ExpectedFailure is the assertion of a pre-check case: no result directory is
// produced, so all that can be checked is how the command failed.
type ExpectedFailure struct {
	// ExitCode is 2 for an argument or validation failure, 4 for an output
	// directory conflict. The distinction matters: the specification fixes the
	// check order so that a directory conflict wins when both apply.
	ExitCode int `json:"exit_code"`
	// Step names the pre-check step expected to fail, for diagnosis.
	Step string `json:"step"`
	// StderrContains is a substring the diagnostic output must include.
	StderrContains string `json:"stderr_contains,omitempty"`
}

// Lines splits JSONL content into non-empty lines.
func Lines(data []byte) []string {
	var out []string

	for l := range strings.SplitSeq(string(data), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}

	return out
}
