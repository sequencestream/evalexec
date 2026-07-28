package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/internal/version"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if got, want := strings.TrimSpace(stdout.String()), version.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}

	// stdout stays machine-readable: diagnostics belong on stderr.
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunUnknownFlagIsArgumentError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Exit code 2 is the argument-error code.
	if code := run([]string{"--no-such-flag"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty: parse errors must not pollute stdout", stdout.String())
	}
}

// TestRunWithoutArgsReportsEveryMissingField checks that an empty invocation
// is rejected with the full list of what it lacked, rather than one field at
// a time across eight retries.
func TestRunWithoutArgsReportsEveryMissingField(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}

	for _, want := range []string{"task_id", "dataset.path", "output_dir", "grader.id", "grader.requires"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q:\n%s", want, stderr.String())
		}
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout must stay clean, got %q", stdout.String())
	}
}

// TestLdflagsStampReachesBinary builds the command the way the Makefile does
// and checks that -ldflags actually reaches version.String(). Without this,
// a wrong -X path would silently ship "dev" into provenance.
func TestLdflagsStampReachesBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	const (
		wantVersion = "9.9.9-test"
		wantCommit  = "deadbee"
		wantDate    = "2026-07-27T01:00:00Z"
	)

	bin := filepath.Join(t.TempDir(), "evalexec")
	ldflags := strings.Join([]string{
		"-X github.com/sequencestream/evalexec/internal/version.Version=" + wantVersion,
		"-X github.com/sequencestream/evalexec/internal/version.Commit=" + wantCommit,
		"-X github.com/sequencestream/evalexec/internal/version.Date=" + wantDate,
	}, " ")

	build := exec.CommandContext(t.Context(), "go", "build", "-ldflags", ldflags, "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.CommandContext(t.Context(), bin, "--version").Output()
	if err != nil {
		t.Fatalf("run %s --version: %v", bin, err)
	}

	want := "evalexec " + wantVersion + " (" + wantCommit + ", " + wantDate + ")"
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("--version = %q, want %q", got, want)
	}
}
