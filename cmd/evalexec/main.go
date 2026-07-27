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
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sequencestream/evalexec/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the real body so tests can drive it without exiting the process.
// It returns the exit code instead of calling os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evalexec", flag.ContinueOnError)
	fs.SetOutput(stderr)

	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		// flag already reported the error to stderr.
		return 2
	}

	if *showVersion {
		_, _ = fmt.Fprintln(stdout, version.String())

		return 0
	}

	// M0 has no evaluation pipeline yet; M2 replaces this with the real
	// argument parsing and M3 wires in evalexec.Run.
	_, _ = fmt.Fprintln(stderr, "evalexec: not implemented yet; use --version")

	return 2
}
