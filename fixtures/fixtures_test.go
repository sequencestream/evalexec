// Package fixtures_test reads the fixtures the way a downstream consumer
// would — through the embedded filesystem, from outside the package — so that
// a case which is unreachable from outside fails here rather than in someone
// else's repository.
package fixtures_test

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/evalspec/evalspectest"
	"github.com/sequencestream/evalexec/fixtures"
)

func TestAllCasesArePresent(t *testing.T) {
	entries, err := fs.ReadDir(fixtures.FS, "data")
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}

	found := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			found[e.Name()] = true
		}
	}

	want := append(fixtures.RunCases(), fixtures.CasePrecheckFailures)
	for _, c := range want {
		if !found[c] {
			t.Errorf("case %q is missing from the embedded filesystem", c)
		}
	}

	if len(found) != len(want) {
		t.Errorf("embedded %d cases, want %d: %v", len(found), len(want), found)
	}
}

func TestRunCaseFilesArePresent(t *testing.T) {
	for _, c := range fixtures.RunCases() {
		t.Run(c, func(t *testing.T) {
			for _, f := range []string{fixtures.FileRequest, fixtures.FileDataset, "README.md"} {
				if _, err := fixtures.Read(c, f); err != nil {
					t.Errorf("%v", err)
				}
			}
		})
	}

	for _, c := range fixtures.GoldenCases() {
		t.Run(c+"/golden", func(t *testing.T) {
			for _, f := range []string{fixtures.FileExpectedResult, fixtures.FileExpectedRecord} {
				if _, err := fixtures.Read(c, f); err != nil {
					t.Errorf("%v", err)
				}
			}
		})
	}

	if _, err := fixtures.Read(fixtures.CaseInterruptCancelled, fixtures.FileExpectedInvariants); err != nil {
		t.Errorf("the interrupt case must carry invariants instead of a golden result: %v", err)
	}
}

// TestRequestsParseAndValidate checks that every fixture request is a legal
// EvalRequest. A fixture that cannot be parsed would silently stop testing
// anything the day it broke.
func TestRequestsParseAndValidate(t *testing.T) {
	for _, c := range fixtures.RunCases() {
		t.Run(c, func(t *testing.T) {
			data, err := fixtures.Read(c, fixtures.FileRequest)
			if err != nil {
				t.Fatalf("%v", err)
			}

			var req evalspec.EvalRequest
			if err := json.Unmarshal(data, &req); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}

			if err := req.Validate(); err != nil {
				t.Errorf("request is not valid: %v", err)
			}

			if req.EvalID == "" {
				t.Error("fixture requests should pin an eval_id so records can be compared")
			}
		})
	}
}

// TestDatasetsRoundTrip is the M1 acceptance check: every dataset row must
// parse into a Session and marshal back to something semantically equal —
// above all preserving which keys were present.
func TestDatasetsRoundTrip(t *testing.T) {
	for _, c := range fixtures.RunCases() {
		t.Run(c, func(t *testing.T) {
			data, err := fixtures.Read(c, fixtures.FileDataset)
			if err != nil {
				t.Fatalf("%v", err)
			}

			lines := fixtures.Lines(data)
			if len(lines) == 0 {
				t.Fatal("dataset is empty")
			}

			seen := make(map[string]bool, len(lines))

			for i, line := range lines {
				var s evalspec.Session
				if err := json.Unmarshal([]byte(line), &s); err != nil {
					t.Fatalf("line %d: %v", i+1, err)
				}

				if seen[s.CaseID] {
					t.Errorf("line %d: duplicate case_id %q", i+1, s.CaseID)
				}

				seen[s.CaseID] = true

				out, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("line %d: marshal: %v", i+1, err)
				}

				want, err := evalspectest.NormalizeJSON([]byte(line))
				if err != nil {
					t.Fatalf("line %d: %v", i+1, err)
				}

				got, err := evalspectest.NormalizeJSON(out)
				if err != nil {
					t.Fatalf("line %d: %v", i+1, err)
				}

				if diffs := evalspectest.Diff(want, got); len(diffs) != 0 {
					t.Errorf("line %d does not round trip:\n%s", i+1, strings.Join(diffs, "\n"))
				}
			}
		})
	}
}

// TestExpectedRecordsAreValid checks that every golden record satisfies the
// invariants. A golden file describing an impossible record would enshrine a
// bug as the expected behaviour.
func TestExpectedRecordsAreValid(t *testing.T) {
	for _, c := range fixtures.GoldenCases() {
		t.Run(c, func(t *testing.T) {
			data, err := fixtures.Read(c, fixtures.FileExpectedRecord)
			if err != nil {
				t.Fatalf("%v", err)
			}

			lines := fixtures.Lines(data)

			for i, line := range lines {
				var rec evalspec.Record
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("record %d: %v", i+1, err)
				}

				if err := rec.Validate(); err != nil {
					t.Errorf("record %d (%s) is not valid: %v", i+1, rec.CaseID, err)
				}

				if rec.Sequence != i+1 {
					t.Errorf("record %d has sequence %d, want %d", i+1, rec.Sequence, i+1)
				}
			}

			// The line-count identity, asserted on the fixture data itself.
			dataset, err := fixtures.Read(c, fixtures.FileDataset)
			if err != nil {
				t.Fatalf("%v", err)
			}

			if want := len(fixtures.Lines(dataset)); len(lines) != want {
				t.Errorf("records.jsonl has %d lines, dataset has %d: the two must be equal", len(lines), want)
			}
		})
	}
}

// TestExpectedResultsAreValid runs the golden results through the same
// validator the writer uses, so a fixture cannot assert a result that
// violates the counting identities.
func TestExpectedResultsAreValid(t *testing.T) {
	for _, c := range fixtures.GoldenCases() {
		t.Run(c, func(t *testing.T) {
			data, err := fixtures.Read(c, fixtures.FileExpectedResult)
			if err != nil {
				t.Fatalf("%v", err)
			}

			var res evalspec.EvalResult
			if err := json.Unmarshal(data, &res); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if err := res.Validate(); err != nil {
				t.Errorf("expected result is not valid: %v", err)
			}
		})
	}
}

// TestGoldenResultAgreesWithRecords cross-checks the two golden files against
// each other. They are written by hand, so nothing but this test stops them
// from drifting apart.
func TestGoldenResultAgreesWithRecords(t *testing.T) {
	for _, c := range fixtures.GoldenCases() {
		t.Run(c, func(t *testing.T) {
			resultData, err := fixtures.Read(c, fixtures.FileExpectedResult)
			if err != nil {
				t.Fatalf("%v", err)
			}

			recordData, err := fixtures.Read(c, fixtures.FileExpectedRecord)
			if err != nil {
				t.Fatalf("%v", err)
			}

			var res evalspec.EvalResult
			if err := json.Unmarshal(resultData, &res); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}

			var (
				completed, skipped, success, fail int
				scores                            []float64
				usage                             evalspec.Usage
				failByCode                        = map[evalspec.ErrorCode]int{}
			)

			lines := fixtures.Lines(recordData)

			for i, line := range lines {
				var rec evalspec.Record
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("record %d: %v", i+1, err)
				}

				if rec.EvalID != res.EvalID {
					t.Errorf("record %d carries eval_id %q, want %q", i+1, rec.EvalID, res.EvalID)
				}

				if rec.TaskID != res.TaskID {
					t.Errorf("record %d carries task_id %q, want %q", i+1, rec.TaskID, res.TaskID)
				}

				if rec.Status == evalspec.RecordSkipped {
					skipped++

					continue
				}

				completed++

				usage.Add(rec.Evaluation.Usage)

				if rec.Evaluation.Status == evalspec.EvaluationSuccess {
					success++

					if rec.Evaluation.Score != nil {
						scores = append(scores, *rec.Evaluation.Score)
					}

					continue
				}

				fail++
				failByCode[rec.Evaluation.Error.Code]++
			}

			if len(lines) != res.Counts.Total {
				t.Errorf("records.jsonl has %d lines, counts.total is %d", len(lines), res.Counts.Total)
			}

			if completed != res.Counts.Completed {
				t.Errorf("counted %d completed records, counts.completed is %d", completed, res.Counts.Completed)
			}

			if skipped != res.Counts.Skipped {
				t.Errorf("counted %d skipped records, counts.skipped is %d", skipped, res.Counts.Skipped)
			}

			if success != res.Evaluation.Success {
				t.Errorf("counted %d successes, evaluation.success is %d", success, res.Evaluation.Success)
			}

			if fail != res.Evaluation.Fail {
				t.Errorf("counted %d failures, evaluation.fail is %d", fail, res.Evaluation.Fail)
			}

			for code, n := range failByCode {
				if res.Evaluation.FailByCode[code] != n {
					t.Errorf("counted %d %q failures, fail_by_code says %d", n, code, res.Evaluation.FailByCode[code])
				}
			}

			checkScoreStats(t, scores, res.Evaluation.Score)

			if got := res.Usage.JudgeModel.InputTokens; got != usage.JudgeInputTokens {
				t.Errorf("records total %d input tokens, usage.judge_model says %d", usage.JudgeInputTokens, got)
			}

			if got := res.Usage.JudgeModel.OutputTokens; got != usage.JudgeOutputTokens {
				t.Errorf("records total %d output tokens, usage.judge_model says %d", usage.JudgeOutputTokens, got)
			}
		})
	}
}

// checkScoreStats verifies the summary statistics against the scores actually
// present in the records — including that failures contributed nothing.
func checkScoreStats(t *testing.T, scores []float64, got evalspec.ScoreStats) {
	t.Helper()

	if len(scores) != got.Count {
		t.Errorf("records carry %d scores, score.count is %d", len(scores), got.Count)

		return
	}

	if len(scores) == 0 {
		if got.Mean != nil || got.Min != nil || got.Max != nil {
			t.Error("score statistics must all be null when no sample was scored")
		}

		return
	}

	sum, minimum, maximum := 0.0, scores[0], scores[0]

	for _, s := range scores {
		sum += s
		minimum = min(minimum, s)
		maximum = max(maximum, s)
	}

	if got.Mean == nil || *got.Mean != sum/float64(len(scores)) {
		t.Errorf("score.mean = %v, want %v (the denominator is score.count, not evaluated)", got.Mean, sum/float64(len(scores)))
	}

	if got.Min == nil || *got.Min != minimum {
		t.Errorf("score.min = %v, want %v", got.Min, minimum)
	}

	if got.Max == nil || *got.Max != maximum {
		t.Errorf("score.max = %v, want %v", got.Max, maximum)
	}
}

func TestPrecheckCases(t *testing.T) {
	cases, err := fixtures.PrecheckCases()
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(cases) < 8 {
		t.Errorf("got %d pre-check subcases, want at least 8", len(cases))
	}

	sawExitCode4 := false

	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			data, err := fixtures.ReadPrecheck(c, fixtures.FileExpectedFailure)
			if err != nil {
				t.Fatalf("%v", err)
			}

			var want fixtures.ExpectedFailure
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			// A pre-check failure produces no result, so only these two exit
			// codes are meaningful here.
			if want.ExitCode != 2 && want.ExitCode != 4 {
				t.Errorf("exit_code = %d, want 2 or 4", want.ExitCode)
			}

			if want.ExitCode == 4 {
				sawExitCode4 = true
			}

			if want.Step == "" {
				t.Error("step must name the failing pre-check step")
			}

			// Every subcase needs a dataset; some also carry a request file
			// and others an argv file, depending on what they exercise.
			if _, err := fixtures.ReadPrecheck(c, fixtures.FileDataset); err != nil {
				t.Errorf("%v", err)
			}
		})
	}

	// The ordering rule — a directory conflict wins over a dataset error —
	// is only tested if some subcase actually expects exit code 4.
	if !sawExitCode4 {
		t.Error("no subcase expects exit code 4: the check-order rule is untested")
	}
}

// TestNoFixtureContainsACredential asserts the property the CI leak scan
// depends on. The scan sets FakeKeyEnv to a sentinel and looks for it in the
// output; that check is meaningless if the sentinel is also sitting in a
// fixture file.
func TestNoFixtureContainsACredential(t *testing.T) {
	// These patterns match credential *shapes*, not merely suggestive words.
	// A substring search for "sk-" would flag the "--task-id" flag inside an
	// argv fixture, and a scanner that cries wolf gets switched off.
	patterns := map[string]*regexp.Regexp{
		"provider key":  regexp.MustCompile(`\b(sk|pk|api)-[A-Za-z0-9_-]{16,}`),
		"bearer token":  regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]{16,}`),
		"inline secret": regexp.MustCompile(`(?i)"(api_?key|secret|token|password)"\s*:\s*"[^"]{8,}"`),
	}

	err := fs.WalkDir(fixtures.FS, "data", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		content, err := fixtures.FS.ReadFile(p)
		if err != nil {
			return err
		}

		for name, re := range patterns {
			if m := re.Find(content); m != nil {
				t.Errorf("%s looks like it contains a %s (%q): fixtures must reference credentials by environment variable name only", p, name, m)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestCredentialScannerCatchesARealKey verifies the scanner above actually
// fires. A leak detector that has never detected anything is indistinguishable
// from one that cannot.
func TestCredentialScannerCatchesARealKey(t *testing.T) {
	leaks := []string{
		`{"auth":{"type":"bearer","key":"sk-1234567890abcdefghij"}}`,
		`Authorization: Bearer abcdefghij0123456789`,
		`{"api_key": "hunter2-hunter2"}`,
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\b(sk|pk|api)-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]{16,}`),
		regexp.MustCompile(`(?i)"(api_?key|secret|token|password)"\s*:\s*"[^"]{8,}"`),
	}

	for i, leak := range leaks {
		if !patterns[i].MatchString(leak) {
			t.Errorf("pattern %d failed to catch %q", i, leak)
		}
	}

	// And it must not fire on the legitimate shapes the fixtures do use.
	for _, ok := range []string{
		`{"auth":{"type":"bearer_env","env":"EVALEXEC_FIXTURE_KEY"}}`,
		`["--task-id", "t", "--grader", "a.json"]`,
	} {
		for i, re := range patterns {
			if re.MatchString(ok) {
				t.Errorf("pattern %d false-positives on %q", i, ok)
			}
		}
	}
}

// TestReadRejectsUnknownCase checks the error path of the accessor, so a typo
// in a case name surfaces as a clear error rather than as empty data.
func TestReadRejectsUnknownCase(t *testing.T) {
	if _, err := fixtures.Read("f99-nonexistent", fixtures.FileDataset); err == nil {
		t.Error("reading an unknown case must fail")
	}

	if got := fixtures.Dir(fixtures.CaseExactMatchAllPass); got != path.Join("data", fixtures.CaseExactMatchAllPass) {
		t.Errorf("Dir = %q", got)
	}
}
