# API

Reference for the two ways of driving EvalExec: the command line and the Go
library. Writing a Grader or Judge in another language is `protocol.md`.

## CLI

```bash
evalexec \
  --task-id customer-service-v1 \
  --dataset ./sessions.jsonl \
  --grader ./relevance-grader.json \
  --judge-model ./judge-model.json \
  --output-dir ./results/relevance
```

Flags only — no subcommands, no positional arguments.

| Flag | Meaning |
|---|---|
| `--task-id` | Correlation key echoed into the result. Required |
| `--dataset` | JSONL file of agent sessions. Required |
| `--grader` | GraderSpec JSON file. May be given once only |
| `--judge-model` | JudgeModelSpec JSON file; required when the Grader needs a Judge |
| `--output-dir` | Result directory. Must not exist or must be empty |
| `--eval-id` | Globally unique run id; generated when absent |
| `--request` | A complete `EvalRequest` JSON; individual flags override it |
| `--grader-param k=v` | Override one Grader parameter. Repeatable |
| `--judge-param k=v` | Override one Judge parameter. Repeatable |
| `--concurrency N` | Samples evaluated at once. Default 1 |
| `--seed N` | Recorded in provenance; **not** forwarded to the Judge |
| `--fail-fast` | Stop dispatching after the first failed evaluation |
| `--version` | Print version and exit |

Flags that would carry a credential (`--api-key`, `--token`, `--secret`,
`--password`, `--auth-token`, `--bearer`, `--key`, `--apikey`) are rejected with
a pointer to `auth.env`.

### Exit codes

| Code | Meaning |
|---:|---|
| 0 | A trustworthy result was produced. May contain failed evaluations, may be incomplete after a fail-fast stop |
| 2 | A pre-check rejected the run. Nothing was written |
| 3 | A run-level fault left no trustworthy result |
| 4 | The output directory conflicted or could not be written |
| 130 | The user interrupted the run |

An exit code says whether the **command** succeeded, never whether the agent
performed well.

## Go library

```go
result, err := evalexec.Run(ctx, request)
```

`Run` is the whole interface — as atomic as the command line, rather than
asking a caller to assemble validation, dataset reading, dispatch, summarizing
and result writing.

### Options

| Option | Effect |
|---|---|
| `WithGraderRegistry(r)` | Resolve Grader entries from `r` instead of the default registry |
| `WithDiagnosticWriter(w)` | Send progress and warnings to `w`. Default: discarded |
| `WithDebugLogs()` | Retain the raw Judge exchange for every sample, not only failures |
| `WithClock(c)` | Pin the time source, for golden-file tests |
| `WithIDGenerator(g)` | Pin the generated `eval_id` |
| `WithJudgeChecker(j)` | Replace the transport-level Judge pre-check |

### Return contract

- Pre-check failure → `(nil, err)`, nothing written.
- Completed or cancelled → `(result, nil)`. A run in which every evaluation
  failed still succeeded *as a run*.
- Run-level fault → `(result with status "failed", err)`, so the caller has
  both the diagnosis and whatever was learned.

### Promises

- **Nothing is written** until every pre-check passes; a rejected run leaves no
  directory behind, temporary ones included.
- The result directory **appears in one step or not at all** — publication is a
  single `rename`.
- `records.jsonl` has exactly one line per dataset row, **on every exit path**.
- A failed evaluation is **never recorded as a zero score**.
- Diagnostics default to `io.Discard`: a library must not assume it owns the
  host process's stderr.

### The Grader interface

Two methods:

```go
type Grader interface {
    Declare() Declaration
    Grade(ctx context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error)
}
```

`Declare` states what the Grader needs so a run can be validated end to end
before the Grader is ever called. `Grade` returns an `Evaluation` with status
`fail` when the Grader ran but could not conclude — that is a normal return
value, not an error. A returned **error** means the Grader itself broke; the
runner turns it into an `internal_error` failure for that one sample and
carries on.

Register an implementation with `grader.NewRegistry().Register(entry, factory)`
and pass the registry through `WithGraderRegistry`. Runnable examples live in
`examples/consumer/` (a separate module — a cross-module build is what proves
everything downstream needs is exported).
