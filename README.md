# EvalExec

One process call = one `EvalRequest` → one Grader → one `EvalResult`.

EvalExec is not an evaluation platform and offers no composed workflows. It
performs a single atomic operation: take one evaluation request, run **one**
Grader over one dataset, write one result directory. It does **not run the
agent under evaluation** — it consumes Session records produced upstream.

```bash
evalexec \
  --task-id customer-service-v1 \
  --dataset ./sessions.jsonl \
  --grader ./relevance-grader.json \
  --judge-model ./judge-model.json \
  --output-dir ./results/relevance
```

Need two Graders? Call it twice. Loops, parallelism, retries, result merging
and quality gates belong to the orchestrator above — and that orchestrator can
import this module directly instead of forking processes repeatedly.

## Features

- **Atomic by design.** One command, one Grader, one result directory. No
  subcommands, no hidden state.
- **Two deliverables from one codebase.** A CLI binary and a Go library sharing
  the same entry point.
- **Five built-in Graders** plus external Graders over HTTP or stdio, in any
  language, and an LLM Judge over OpenAI-compatible, Anthropic Messages or
  EvalExec's own protocols.
- **Errors are never disguised as low scores**, results **survive interruption**
  and are **published atomically** — the three properties the rest of this
  README elaborates.
- **Traceable.** Every result carries dataset and request checksums plus the
  build that produced it.
- **Secrets never touch disk.** Credentials are referenced only by environment
  variable name.

## Install

Download a binary for linux, macOS or Windows (amd64 / arm64) from the
[releases page](https://github.com/sequencestream/evalexec/releases), or build
from source:

```bash
go install github.com/sequencestream/evalexec/cmd/evalexec@latest
```

As a library:

```bash
go get github.com/sequencestream/evalexec
```

## Quick start

A dataset is JSONL, one already-executed agent session per line:

```jsonl
{"case_id":"c1","input":{"question":"order status?"},"output":{"text":"Shipped"},"reference":{"expected_output":{"text":"Shipped"}}}
{"case_id":"c2","input":{"question":"refund?"},"output":{"text":"7 days"},"reference":{"expected_output":{"text":"14 days"}}}
```

A Grader configuration:

```json
{
  "id": "order-status",
  "version": "v1",
  "protocol": "builtin",
  "entry": "exact_match",
  "requires": ["input", "output", "reference"],
  "requires_judge": false
}
```

Run it:

```bash
evalexec --task-id demo \
  --dataset ./sessions.jsonl \
  --grader ./grader.json \
  --output-dir ./results/demo
```

The result directory:

```
results/demo/
  result.json          counts, score statistics, provenance
  records.jsonl        one line per dataset row
  checksums.sha256     covers result.json and records.jsonl
  errors.jsonl         diagnostics, when there were any
  logs/                raw Judge exchanges, when there were any
```

Every field of every document is specified in
[doc/protocol.md](./doc/protocol.md); the full flag list is in
[doc/api.md](./doc/api.md).

## Boundaries

- `task_id` is passed through untouched; tasks are not abstracted.
- Execution and evaluation are separate: EvalExec grades existing Sessions and
  never calls the agent.
- Protocol over SDK: JSON/JSONL and HTTP/stdio bind to no language.
- Execution errors are never disguised as low scores. An evaluation is
  `success` or `fail`; a `fail` carries a reason code and **is not scored
  zero**. Samples that never ran are recorded as `skipped`.
- Scores are not interpreted. `score` / `label` come from the Grader verbatim;
  `min_score` / `max_score` are passed through. Whether a result is good enough
  is decided elsewhere.
- An existing, non-empty output directory is refused. There is **no
  `--force`**.
- **No retries, no rate limiting.** 429 and 5xx are recorded as `judge_error`.
  To retry, re-run the whole evaluation from above.

## Stability layers

The public surface is deliberately small — everything else lives under
`internal/` and is enforced by the compiler. What remains is declared in two
layers: the protocol types are the most stable, the Go API follows from v1.0.
Which package sits where is tabulated in
[doc/architecture.md](./doc/architecture.md#stability-layers).

## Concurrency and stopping

```bash
evalexec --concurrency 8 --fail-fast ...
```

**`records.jsonl` always has exactly as many lines as the dataset** — on a
normal finish, after a fail-fast stop, and after a user interrupt alike. That
identity is the line between "partial but still trustworthy" and "truncated".

| Situation | `status` | `stop_reason` | Exit |
|---|---|---|---:|
| Everything processed (even if all `fail`) | `completed` | null | `0` |
| Fail-fast stop | `cancelled` | `fail_fast` | **`0`** |
| User interrupt | `cancelled` | `interrupt` | `130` |
| Run-level fault | `failed` | — | `3` |

**Fail-fast exits 0**: it is a stopping policy the caller explicitly requested,
so the command did what it was told. Incompleteness is expressed by `status`
and `counts.skipped`, not by the exit code. And **only
`evaluation.status=fail` triggers it** — a low score never does, because
EvalExec does not interpret scores and cannot judge whether a 0 is bad.

**Cancelled samples are `skipped`, not `fail`.** A sample abandoned mid-flight
never finished; recording it as a timeout would report work that never happened
as work done badly.

Interrupt escalation: the first signal stops dispatch and completes the
backfill and publication; the second is **ignored** — the backfill is precisely
what makes a partial result trustworthy; the third abandons the run and
publishes **nothing**, so a caller sees no directory rather than a half-built
one.

Two things deliberately not guaranteed:

- **Line order.** Under concurrency, records are written in completion order.
  Every line carries `sequence`; consumers sort.
- **Bit-identical `score.mean` across concurrency levels.** Float addition is
  not associative and record arrival order varies with concurrency. The
  difference is around 1e-15; use `--concurrency 1` to reproduce exactly.

## Built-in Graders

| `entry` | Comparison | Parameters |
|---|---|---|
| `exact_match` | JSON-semantic equality between `output` and the expected value in `reference` | `reference_path` (default `$.expected_output`) |
| `contains` | `output` text contains **all** expected substrings | `reference_path` (default `$.expected_contains`), `case_sensitive` |
| `regex` | `output` text matches a pattern | `pattern` (required), `case_sensitive` |
| `json_schema` | `output` validates against a JSON Schema | `schema` (required) |
| `llm_judge` | Delegated to an LLM Judge | `rubric` (required), `min_score`, `max_score`, `use_reference`, `use_trajectory`, `structured_output` |

**A mismatch is a successful evaluation scoring 0.** Only "could not conclude"
(no expected value to compare against, a failed Judge call) is a `fail`, and a
`fail` carries no score and does not enter the mean. This distinction is the
foundation of the whole status model.

`pattern` and `schema` are compiled once **during the pre-check phase**: a
misconfiguration should fail before the first sample runs.

## LLM Judge

```json
{
  "protocol": "openai-chat",
  "endpoint": "https://api.deepseek.com",
  "auth": {"type": "bearer_env", "env": "JUDGE_API_KEY"},
  "parameters": {"model": "deepseek-v4-flash", "temperature": 0},
  "timeout_ms": 60000
}
```

`protocol` accepts `openai-chat`, `anthropic-messages`, `http-json` and
`stdio-jsonl`. `parameters` accepts ten keys: `model` (required),
`temperature`, `max_completion_tokens`, `max_tokens`, `top_p`, `top_k`, `stop`,
`reasoning_effort`, `parallel_tool_calls`, `response_format`. **An unknown key
is an argument error**, not a silent drop — a misspelled `temperatur` quietly
ignored would produce a result that looks fine and was graded with the wrong
settings.

Credentials may only be referenced by environment variable name via `auth.env`.
Command-line flags that would carry a secret (`--api-key` and friends) are
rejected with a pointer to `auth.env`. A configuration file that appears to
contain a secret causes the run to be **refused** rather than redacted —
silently scrubbing it would leave you believing the key was handled safely
while it still sits in that file on disk.

Two limits worth knowing:

- **`--seed` is not forwarded to the Judge.** The canonical chat request has no
  `seed` field, so the seed is recorded in `provenance` only and `llm_judge`
  relies on `temperature=0` for stability. **Bit-identical scores are not
  promised.**
- **`structured_output` is off by default.** It is not portable across
  OpenAI-compatible endpoints — DeepSeek returns 400 for a `json_schema`
  request — and since EvalExec does not retry, a rejected request loses the
  whole sample. Agreeing on JSON in the prompt plus tolerant parsing works with
  every provider, so that is the default path; turn it on once you have
  confirmed your endpoint supports it.

## External Graders and Judges

Both can be processes or services written in another language. A Grader speaks
`http-json` or `stdio-jsonl`; a Judge additionally speaks `openai-chat` and
`anthropic-messages`. The wire specification — request and response shapes, and
exactly which misbehaviour becomes which error code — is in
[doc/protocol.md](./doc/protocol.md#external-protocols). The rule an
implementation is most likely to get wrong: a `fail` carries `"score": null`,
never `0`.

`stdio-jsonl` uses **one subprocess per worker (count = `--concurrency`)**: the
protocol is one question at a time, and sharing a process would interleave
conversations. After a timeout or crash the **process tree** is killed, not
just the process — a script that forked its own children would otherwise leave
orphans, and an orphan still holding the pipe is indistinguishable from a
process that has not answered yet.

## Using it as a library

`Run` is the whole interface. Like the command line it is a single atomic call,
rather than asking the caller to assemble validation, dataset reading,
dispatch, summarizing and result writing:

```go
result, err := evalexec.Run(ctx, request)
```

Three runnable examples live in [`examples/consumer/`](./examples/consumer/) (a
**separate module**). This README links to them rather than copying them —
copied examples rot.

### 1. Run one evaluation

```go
req := &evalspec.EvalRequest{
    SpecVersion: evalspec.SpecVersion,
    TaskID:      "consumer-smoke",
    Dataset:     evalspec.Dataset{Path: dataset},
    Grader: evalspec.GraderSpec{
        ID: "order-status", Version: "v1",
        Protocol: evalspec.GraderBuiltin, Entry: "exact_match",
        Requires: []evalspec.SessionField{
            evalspec.FieldInput, evalspec.FieldOutput, evalspec.FieldReference,
        },
    },
    OutputDir: outDir,
}

res, err := evalexec.Run(context.Background(), req)
```

`Run` generates an `eval_id` when one is not supplied, so a caller who does not
care about correlation can leave it out. The functional options — registry,
diagnostic writer, debug logs, clock — are tabulated in
[doc/api.md](./doc/api.md#options).

### 2. Register your own Grader

```go
registry := grader.NewRegistry()

registry.Register("answer_length", func(spec evalspec.GraderSpec, _ grader.Deps) (grader.Grader, error) {
    return &lengthGrader{maxRunes: 20}, nil
})

res, err := evalexec.Run(ctx, req, evalexec.WithGraderRegistry(registry))
```

Run it with `protocol: "builtin"` and your own `entry` — no subprocess needed.
**`builtin` does not mean "one of the five that ship here"**; it means "a
Grader compiled into the binary", downstream registrations included. This does
not widen the `evalexec` binary, which registers only the five built-in
entries: a custom entry is visible only in a binary you built yourself.

### 3. Self-test your Grader with the shared fixtures

```go
data, _ := fixtures.Read(fixtures.CaseMixedSuccessFail, fixtures.FileDataset)

for _, line := range fixtures.Lines(data) {
    var session evalspec.Session
    json.Unmarshal([]byte(line), &session)

    // The same check the host performs, and it asks whether the key is
    // present: a field explicitly set to null counts as present.
    if missing := session.MissingFields(g.Declare().Requires); len(missing) > 0 {
        // ...
    }

    call := evalspec.NewGradeCall("selftest", "consumer", &session, nil)
    eval, _ := g.Grade(ctx, call)
}
```

The fixtures are published for exactly this: the same data EvalExec tests
itself with, reachable through `embed.FS`, so downstream does not need its own
copy.

One promise worth stating here, because it shapes how you embed EvalExec:
diagnostics default to `io.Discard`. A library must not assume it owns the host
process's stderr; use `WithDiagnosticWriter` to direct them somewhere. The rest
of what `Run` guarantees is in [doc/api.md](./doc/api.md#promises); what each
version promises is in [doc/protocol.md](./doc/protocol.md#versioning).

## Development

```bash
make build      # build bin/evalexec (version injected via -ldflags)
make test       # go test -race ./...
make lint       # terminology, library-boundary and dependency guards + golangci-lint
make test-e2e   # against a real model; needs OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL
```

`make lint` includes `lint-secrets`: it produces a real result directory and
scans every file in it, `logs/` included, asserting that no sentinel secret, no
secret-shaped string and no live `Authorization` header survives — and it
separately verifies that **the scanner does fire**. A leak detector that has
never reported anything is indistinguishable from a broken one.

Three guards are hard constraints, not suggestions:

- `lint-terms` — the scoring component is always called **Grader**; synonymous
  word roots are banned (the list is in the `Makefile`). Getting the word root
  wrong on a public API makes renaming it a breaking change.
- `lint-boundary` — no `os.Exit` / `signal.Notify` / `os.Stderr` outside
  `cmd/`. EvalExec is embedded in host processes, where those three mean
  killing the process, stealing its signals, and polluting its output.
- `check-deps` — direct dependencies capped at aimodel plus one JSON Schema
  library.

## Documentation

This README is the explanation; `doc/` is the reference, one document per kind
of reader, and does not repeat what is here.

| Document | For | Contents |
|---|---|---|
| [doc/protocol.md](./doc/protocol.md) | Anyone producing or consuming these documents, in any language | Every document field by field, then the external Grader / Judge wire specification and the versioning promise |
| [doc/api.md](./doc/api.md) | CLI users and Go integrators | Every flag and exit code; the library surface, options and return contract |
| [doc/architecture.md](./doc/architecture.md) | Anyone changing this repository | Stability layers, package map, run flow, backfill |
| [AGENTS.md](./AGENTS.md) | Agents working in this repository | Conventions |

## License

Apache 2.0
