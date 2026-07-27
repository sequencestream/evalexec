package result_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/result"
)

func TestPublishIsAtomic(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out")

	d, err := result.Create(target, "eval-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Before publication the target does not exist: a consumer polling for it
	// can never observe a half-written directory.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("the target must not exist before publication (stat err = %v)", err)
	}

	if err := os.WriteFile(d.Path(result.FileResult), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("write result: %v", err)
	}

	if err := os.WriteFile(d.Path(result.FileRecords), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write records: %v", err)
	}

	if err := d.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	for _, name := range []string{result.FileResult, result.FileRecords, result.FileChecksums} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Errorf("%s missing after publication: %v", name, err)
		}
	}

	if _, err := os.Stat(d.Tmp()); !os.IsNotExist(err) {
		t.Errorf("the temporary directory must be gone after publication (stat err = %v)", err)
	}
}

// TestTemporaryDirectoryIsBesideTheTarget pins the reason the temporary
// directory is not in /tmp: rename is only atomic within one filesystem, and a
// cross-device rename would degrade into an observable copy.
func TestTemporaryDirectoryIsBesideTheTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "out")

	d, err := result.Create(target, "eval-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got, want := filepath.Dir(d.Tmp()), filepath.Dir(target); got != want {
		t.Errorf("temporary directory is in %q, want it beside the target in %q", got, want)
	}
}

func TestDiscardLeavesNothing(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out")

	d, err := result.Create(target, "eval-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := os.WriteFile(d.Path(result.FileResult), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := d.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("Discard left %d entries behind: %v", len(entries), entries)
	}

	// Discard after Publish is a no-op, not a deletion of the published
	// result.
	d2, err := result.Create(target, "eval-2")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := os.WriteFile(d2.Path(result.FileResult), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := d2.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := d2.Discard(); err != nil {
		t.Fatalf("Discard after Publish: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Errorf("Discard after Publish must not remove the published result: %v", err)
	}
}

// TestChecksumsCoverOnlyStableFiles pins the coverage decision: diagnostics
// are excluded, because they may be absent or truncated and would make the
// checksum file unverifiable for exactly the runs that need diagnosing.
func TestChecksumsCoverOnlyStableFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out")

	d, err := result.Create(target, "eval-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for name, content := range map[string]string{
		result.FileResult:  `{"ok":true}`,
		result.FileRecords: "{}\n",
		result.FileErrors:  `{"diagnostic":true}`,
	} {
		if err := os.WriteFile(d.Path(name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if err := d.MkdirAll(result.DirLogs); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}

	if err := d.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(target, result.FileChecksums))
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}

	covered := map[string]bool{}

	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			t.Fatalf("malformed checksum line %q", sc.Text())
		}

		if len(fields[0]) != 64 {
			t.Errorf("expected a hex sha256, got %q", fields[0])
		}

		covered[fields[1]] = true
	}

	for _, want := range []string{result.FileResult, result.FileRecords} {
		if !covered[want] {
			t.Errorf("%s is not covered by checksums", want)
		}
	}

	for _, unwanted := range []string{result.FileErrors, result.FileChecksums, result.DirLogs} {
		if covered[unwanted] {
			t.Errorf("%s must not be covered by checksums", unwanted)
		}
	}
}

// TestEvalIDIsSanitizedIntoTheTempName guards against a caller-supplied
// identifier escaping the parent directory. Identifiers are opaque strings, so
// nothing stops one containing a slash.
func TestEvalIDIsSanitizedIntoTheTempName(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out")

	d, err := result.Create(target, "../../etc/passwd")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := filepath.Dir(d.Tmp()); got != root {
		t.Errorf("temporary directory escaped to %q, want it under %q", got, root)
	}

	if strings.Contains(filepath.Base(d.Tmp()), "/") || strings.Contains(filepath.Base(d.Tmp()), "..") {
		t.Errorf("temporary directory name %q was not sanitized", filepath.Base(d.Tmp()))
	}
}

func TestPublishTwiceIsRejected(t *testing.T) {
	root := t.TempDir()

	d, err := result.Create(filepath.Join(root, "out"), "eval-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := os.WriteFile(d.Path(result.FileResult), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := d.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := d.Publish(); err == nil {
		t.Error("publishing twice must be rejected")
	}
}
