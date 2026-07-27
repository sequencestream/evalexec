// Package version carries the build identity of this binary or library:
// the implementation name and the version, commit and build date injected at
// link time. It is the single source for the values EvalExec writes into
// EvalResult.provenance.implementation, so a result can always be traced back
// to the build that produced it.
//
// The values are variables rather than constants because they are set with
// -ldflags -X at build time; see the Makefile. An unstamped build (go test,
// go run, `go install` without flags) reports the placeholder defaults, which
// is why String never returns an empty string.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise, and changes go through the CHANGELOG.
package version

// Build identity, overridden at link time with
// -X github.com/sequencestream/evalexec/version.<Name>=<value>.
var (
	// Name is the implementation name written to provenance.
	Name = "evalexec"
	// Version is the release version, normally `git describe --tags --always --dirty`.
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "none"
	// Date is the build timestamp in RFC 3339 UTC.
	Date = "unknown"
)

// String renders the full build identity, e.g.
// "evalexec 0.1.0 (abc1234, 2026-07-27T01:00:00Z)".
//
// Placeholder fields are omitted rather than printed: an unstamped build
// reports "evalexec dev" instead of padding the line with "none" and
// "unknown", which would read as real values in a bug report.
func String() string {
	head := Name + " " + Version

	commit, date := Commit != "" && Commit != "none", Date != "" && Date != "unknown"

	switch {
	case commit && date:
		return head + " (" + Commit + ", " + Date + ")"
	case commit:
		return head + " (" + Commit + ")"
	case date:
		return head + " (" + Date + ")"
	default:
		return head
	}
}
