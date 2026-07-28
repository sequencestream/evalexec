package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader"
	"github.com/sequencestream/evalexec/subprocess"
)

// StdioJSONL grades by exchanging one JSON line with a subprocess.
//
// Each worker gets its own process, because the protocol is one question at a
// time and sharing one would interleave conversations. The process count
// therefore equals the run's concurrency — documented rather than hidden, since
// it is the caller's machine that has to accommodate it.
type StdioJSONL struct {
	command  string
	requires []evalspec.SessionField
	spec     evalspec.GraderSpec
	pool     *subprocess.Pool
}

// NewStdioJSONL builds a Grader that runs spec.Entry.
//
// Entry is a single executable path and is not passed through a shell. Shell
// parsing would bring quoting, escaping and injection along with it, and buys
// nothing: a Grader that needs arguments reads them from grader.parameters,
// which it already receives, and anything more elaborate is a wrapper script.
func NewStdioJSONL(spec evalspec.GraderSpec, _ int) (grader.Grader, error) {
	if spec.Entry == "" {
		return nil, errors.New("stdio-jsonl grader: entry must be the executable path")
	}

	if err := subprocess.Executable(spec.Entry); err != nil {
		return nil, err
	}

	return &StdioJSONL{
		command:  spec.Entry,
		requires: spec.Requires,
		spec:     declarationFrom(spec),
		pool:     subprocess.NewPool(spec.Entry),
	}, nil
}

// Declare reports what the configuration says this Grader needs.
func (g *StdioJSONL) Declare() grader.Declaration {
	return grader.Declaration{
		Entry:         g.command,
		Requires:      g.requires,
		RequiresJudge: g.spec.RequiresJudge,
	}
}

// Grade sends one call and reads one reply.
func (g *StdioJSONL) Grade(ctx context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error) {
	body, err := json.Marshal(call)
	if err != nil {
		return evalspec.Evaluation{}, fmt.Errorf("stdio-jsonl grader: cannot serialize the call: %w", err)
	}

	proc, err := g.pool.Acquire()
	if err != nil {
		return protocolFailure("no grader process is available", err), nil
	}

	start := time.Now()
	line, callErr := proc.Call(ctx, body)

	// A process whose exchange failed is not reused: after a kill or a crash
	// there is no way to know whether an unread answer is still in the pipe,
	// and reusing it would attribute one sample's verdict to another.
	g.pool.Release(proc, callErr != nil)

	if callErr != nil {
		if errors.Is(callErr, context.Canceled) {
			return evalspec.Evaluation{}, callErr
		}

		if errors.Is(callErr, context.DeadlineExceeded) {
			return timeoutFailure("the grader process did not answer in time"), nil
		}

		return protocolFailure("the grader process failed", callErr), nil
	}

	eval, err := decodeEvaluation(line)
	if err != nil {
		return protocolFailure("the grader process wrote an unusable reply", err), nil
	}

	eval.LatencyMS = time.Since(start).Milliseconds()

	return eval, nil
}

// Close shuts down every process this Grader started.
func (g *StdioJSONL) Close() error { return g.pool.Close() }

// PoolSize reports how many processes were started, so a test can check the
// count against the concurrency.
func (g *StdioJSONL) PoolSize() int { return g.pool.Size() }
