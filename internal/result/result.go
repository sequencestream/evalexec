// Package result owns the result directory: it is written somewhere else and
// moved into place in one step, so a half-written directory is never
// observable.
//
// The temporary directory lives beside the target rather than in the system
// temp directory, because publication is a rename and a rename is only atomic
// within one filesystem. A caller whose output directory sits on a different
// mount than /tmp would otherwise get a copy — non-atomic, and observable
// mid-flight.
//
// Nothing here is created until every pre-check has passed. A rejected run
// must leave no directory behind, temporary ones included.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise.
package result

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sequencestream/evalexec/evalerr"
)

// StepPublish is the step name reported for output failures.
const StepPublish = "output"

// File names inside a result directory.
const (
	FileResult    = "result.json"
	FileRecords   = "records.jsonl"
	FileChecksums = "checksums.sha256"
	FileErrors    = "errors.jsonl"
	DirLogs       = "logs"
)

// checksummedFiles are the ones covered by checksums.sha256.
//
// Diagnostics are excluded on purpose: errors.jsonl and logs/ are optional and
// may be absent or truncated, so including them would make the checksum file
// unverifiable for exactly the runs that most need diagnosing.
var checksummedFiles = []string{FileResult, FileRecords}

// Dir is a result directory being built.
type Dir struct {
	target string
	tmp    string
	closed bool
}

// Create makes the temporary directory that will become target.
//
// It fails if target already exists and is non-empty; the caller is expected
// to have checked that during the pre-checks, and this is the second line of
// defence against a race between the two.
func Create(target, evalID string) (*Dir, error) {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, evalerr.Wrap(evalerr.KindOutput, StepPublish, err,
			"cannot create the parent of %s", target)
	}

	// Same parent, therefore same filesystem, therefore rename is atomic.
	tmp := filepath.Join(parent, "."+filepath.Base(target)+".tmp-"+sanitize(evalID))

	if err := os.RemoveAll(tmp); err != nil {
		return nil, evalerr.Wrap(evalerr.KindOutput, StepPublish, err,
			"cannot clear the temporary directory %s", tmp)
	}

	if err := os.Mkdir(tmp, 0o755); err != nil {
		return nil, evalerr.Wrap(evalerr.KindOutput, StepPublish, err,
			"cannot create the temporary directory %s", tmp)
	}

	return &Dir{target: target, tmp: tmp}, nil
}

// Path returns the absolute path of a file inside the pending directory.
func (d *Dir) Path(name string) string { return filepath.Join(d.tmp, name) }

// Tmp returns the pending directory path.
func (d *Dir) Tmp() string { return d.tmp }

// Target returns the path this directory will be published to.
func (d *Dir) Target() string { return d.target }

// Create makes a file inside the pending directory.
func (d *Dir) Create(name string) (*os.File, error) {
	f, err := os.Create(d.Path(name))
	if err != nil {
		return nil, evalerr.Wrap(evalerr.KindOutput, StepPublish, err, "cannot create %s", name)
	}

	return f, nil
}

// MkdirAll makes a subdirectory inside the pending directory.
func (d *Dir) MkdirAll(name string) error {
	if err := os.MkdirAll(d.Path(name), 0o755); err != nil {
		return evalerr.Wrap(evalerr.KindOutput, StepPublish, err, "cannot create %s/", name)
	}

	return nil
}

// WriteChecksums writes checksums.sha256 over the stable interface files.
// The checksum file does not cover itself.
func (d *Dir) WriteChecksums() error {
	var b strings.Builder

	names := append([]string(nil), checksummedFiles...)
	sort.Strings(names)

	for _, name := range names {
		data, err := os.ReadFile(d.Path(name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return evalerr.Wrap(evalerr.KindOutput, StepPublish, err, "cannot read %s for checksumming", name)
		}

		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}

	if err := os.WriteFile(d.Path(FileChecksums), []byte(b.String()), 0o644); err != nil {
		return evalerr.Wrap(evalerr.KindOutput, StepPublish, err, "cannot write %s", FileChecksums)
	}

	return nil
}

// Publish moves the pending directory into place in one step. After it
// returns, the directory is complete or it does not exist.
func (d *Dir) Publish() error {
	if d.closed {
		return evalerr.Output(StepPublish, "the result directory was already finalized")
	}

	if err := d.WriteChecksums(); err != nil {
		return err
	}

	if err := os.Rename(d.tmp, d.target); err != nil {
		return evalerr.Wrap(evalerr.KindOutput, StepPublish, err,
			"cannot publish the result directory to %s", d.target)
	}

	d.closed = true

	return nil
}

// Discard removes the pending directory without publishing. It is safe to
// call after Publish, where it does nothing.
func (d *Dir) Discard() error {
	if d.closed {
		return nil
	}

	d.closed = true

	if err := os.RemoveAll(d.tmp); err != nil {
		return evalerr.Wrap(evalerr.KindOutput, StepPublish, err,
			"cannot remove the temporary directory %s", d.tmp)
	}

	return nil
}

// sanitize keeps a caller-supplied eval_id from escaping the parent directory
// or producing an unusable name. Identifiers are opaque strings from the
// caller, so nothing stops one containing a slash.
func sanitize(s string) string {
	if s == "" {
		return "run"
	}

	var b strings.Builder

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}

	return out
}
