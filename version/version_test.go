package version_test

import (
	"testing"

	"github.com/sequencestream/evalexec/version"
)

// TestStringOmitsPlaceholders covers the four combinations of stamped and
// unstamped build metadata. An unstamped build must not print "none" or
// "unknown" — in a bug report those read as real values.
func TestStringOmitsPlaceholders(t *testing.T) {
	tests := []struct {
		name                             string
		implName, ver, commit, buildDate string
		want                             string
	}{
		{
			name:     "fully stamped",
			implName: "evalexec", ver: "0.1.0", commit: "abc1234", buildDate: "2026-07-27T01:00:00Z",
			want: "evalexec 0.1.0 (abc1234, 2026-07-27T01:00:00Z)",
		},
		{
			name:     "commit only",
			implName: "evalexec", ver: "0.1.0", commit: "abc1234", buildDate: "unknown",
			want: "evalexec 0.1.0 (abc1234)",
		},
		{
			name:     "date only",
			implName: "evalexec", ver: "0.1.0", commit: "none", buildDate: "2026-07-27T01:00:00Z",
			want: "evalexec 0.1.0 (2026-07-27T01:00:00Z)",
		},
		{
			name:     "unstamped defaults",
			implName: "evalexec", ver: "dev", commit: "none", buildDate: "unknown",
			want: "evalexec dev",
		},
		{
			name:     "empty commit and date are treated as unset",
			implName: "evalexec", ver: "dev", commit: "", buildDate: "",
			want: "evalexec dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := stub(tt.implName, tt.ver, tt.commit, tt.buildDate)
			defer restore()

			if got := version.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDefaultsAreNonEmpty guards the invariant that provenance always gets a
// usable implementation name and version, even from an unstamped build.
func TestDefaultsAreNonEmpty(t *testing.T) {
	if version.Name == "" {
		t.Error("Name must never be empty: provenance.implementation.name depends on it")
	}

	if version.Version == "" {
		t.Error("Version must never be empty: provenance.implementation.version depends on it")
	}

	if version.String() == "" {
		t.Error("String() must never be empty")
	}
}

// stub swaps the package-level build identity and returns a function that
// puts the originals back. These tests must not run in parallel.
func stub(name, ver, commit, date string) func() {
	origName, origVer, origCommit, origDate := version.Name, version.Version, version.Commit, version.Date

	version.Name, version.Version, version.Commit, version.Date = name, ver, commit, date

	return func() {
		version.Name, version.Version, version.Commit, version.Date = origName, origVer, origCommit, origDate
	}
}
