package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/version"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if got, want := strings.TrimSpace(stdout.String()), version.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}

	// stdout stays machine-readable: diagnostics belong on stderr (dev-plan §1.5).
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunUnknownFlagIsArgumentError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Exit code 2 is the argument-error code (03-cli-and-execution.md §4).
	if code := run([]string{"--no-such-flag"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty: parse errors must not pollute stdout", stdout.String())
	}
}

func TestRunWithoutArgsIsNotImplementedYet(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// M0 has no pipeline. This test is expected to change in M2, when real
	// argument validation takes over.
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}

	if !strings.Contains(stderr.String(), "not implemented") {
		t.Errorf("stderr = %q, want a not-implemented notice", stderr.String())
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
		"-X github.com/sequencestream/evalexec/version.Version=" + wantVersion,
		"-X github.com/sequencestream/evalexec/version.Commit=" + wantCommit,
		"-X github.com/sequencestream/evalexec/version.Date=" + wantDate,
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
