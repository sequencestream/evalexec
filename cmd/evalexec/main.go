// Command evalexec runs exactly one evaluation: it reads one EvalRequest,
// grades a dataset with one Grader, and writes one EvalResult directory.
// There are no subcommands — see doc/dev-plan.md for the boundary this holds.
//
// This file is the only place in the module allowed to call os.Exit, install
// signal handlers, or write to os.Stderr directly; everything below it is a
// library that may be embedded in a host process. `make lint-boundary`
// enforces that.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/sequencestream/evalexec/cli"
	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/exitcode"
	"github.com/sequencestream/evalexec/validate"
	"github.com/sequencestream/evalexec/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the real body so tests can drive it without exiting the process.
// It returns the exit code instead of calling os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	// --version is handled before parsing so it works on its own, without the
	// otherwise mandatory flags.
	if slices.Contains(args, "--version") || slices.Contains(args, "-version") {
		_, _ = fmt.Fprintln(stdout, version.String())

		return exitcode.OK
	}

	req, err := cli.Parse(args, stderr, cli.Options{})
	if err != nil {
		return report(stderr, err)
	}

	if _, err := validate.All(req, validate.Options{Diag: stderr}); err != nil {
		return report(stderr, err)
	}

	// M3 replaces this with a call to evalexec.Run.
	_, _ = fmt.Fprintln(stderr, "evalexec: pre-checks passed; evaluation lands in M3")

	return exitcode.OK
}

// report writes a diagnostic and returns the exit code for err.
//
// The step name is included because it tells a user which of the six ordered
// pre-checks rejected the run, and the ordering itself carries meaning: a
// directory conflict reported before a dataset error is not an arbitrary
// choice.
func report(stderr io.Writer, err error) int {
	// Something has already told the user about this one — the flag package
	// prints its own complaint before returning.
	var e *evalerr.Error
	if errors.As(err, &e) && e.Reported {
		return exitcode.FromError(err)
	}

	_, _ = fmt.Fprintf(stderr, "evalexec: %s\n", err)

	return exitcode.FromError(err)
}
