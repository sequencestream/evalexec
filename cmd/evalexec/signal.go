package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sequencestream/evalexec/exitcode"
)

// interruptsBeforeForcedExit is how many signals are honoured before the
// process gives up on winding down cleanly.
const interruptsBeforeForcedExit = 3

// withInterrupts returns a context cancelled by SIGINT or SIGTERM, and a stop
// function to release the handler.
//
// This is the only place in the module that installs a signal handler. Below
// it, stopping is expressed purely as context cancellation — a library that
// grabbed the host process's signals would be taking something that is not
// its to take.
//
// The escalation matters as much as the first signal:
//
//	first    stop dispatching, then backfill and publish
//	second   ignored — the backfill is what makes the result trustworthy, and
//	         interrupting it would leave a directory whose line count does not
//	         match its dataset
//	third    give up and exit without publishing
//
// After a forced exit the temporary directory is left behind, but since it was
// never renamed the caller sees no result directory at all — which is exactly
// what happened.
func withInterrupts(parent context.Context, stderr io.Writer) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)

	signals := make(chan os.Signal, interruptsBeforeForcedExit)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	done := make(chan struct{})

	go func() {
		count := 0

		for {
			select {
			case <-signals:
				count++

				switch {
				case count == 1:
					_, _ = fmt.Fprintln(stderr,
						"evalexec: interrupted; finishing the current samples and publishing a partial result")

					cancel()
				case count < interruptsBeforeForcedExit:
					_, _ = fmt.Fprintf(stderr,
						"evalexec: already winding down; interrupt %d more time(s) to abandon the result\n",
						interruptsBeforeForcedExit-count)
				default:
					_, _ = fmt.Fprintln(stderr,
						"evalexec: abandoning the run; no result directory will be published")

					os.Exit(exitcode.Interrupt)
				}
			case <-done:
				return
			}
		}
	}()

	return ctx, func() {
		signal.Stop(signals)
		close(done)
		cancel()
	}
}
