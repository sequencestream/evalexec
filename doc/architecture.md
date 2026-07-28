# Architecture

How the pieces fit together: what the program is made of, and in what order it
does things.

## Two deliverables

1. Binary `evalexec` — the atomic CLI.
2. Go library `github.com/sequencestream/evalexec` — import it to reuse the
   protocol types, implement a custom Grader, or embed evaluation in your own
   orchestrator.

## Stability layers

Only what downstream code can import is on the public API surface; everything
else lives under `internal/` and the compiler enforces the boundary:

| Layer | Packages | Promise |
|---|---|---|
| **L1 protocol** | `evalspec`, `fixtures` | Lives with `spec_version`; within `evalexec/v1alpha1` only optional fields are added |
| **L2 Go API** | root `Run`, `grader`, `judge`, `evalerr` | Go compatibility promise from v1.0; interfaces kept deliberately narrow |

An L1 change bumps `spec_version` as well.

## Package map

Public packages carry the concepts; `internal/` carries the machinery.

```
cmd/evalexec  →  internal/cli  →  evalexec.Run
                                     ├── internal/validate    six pre-checks, writes nothing
                                     ├── internal/redact      request snapshot + sha256
                                     ├── grader               interface, registry, declarations
                                     │     ├── internal/grader/builtin     the five built-in Graders
                                     │     └── internal/grader/external    http-json, stdio-jsonl
                                     ├── judge                interface, checker
                                     │     ├── internal/judge/provider     openai-chat, anthropic-messages, …
                                     │     └── internal/judge/transport    HTTP recorder
                                     ├── internal/dataset     JSONL reader, case_id index
                                     ├── internal/runner      dispatch, timeout, stop, backfill
                                     ├── internal/summary     counts, score stats, run status
                                     ├── internal/result      temp dir, publish, logs, checksums
                                     └── internal/exitcode    the only place codes are decided
```

`evalerr` classifies failures; `internal/exitcode` is the single place that maps
a failure to a process exit code, so the mapping cannot drift.

## Run flow

1. **Normalize** — CLI flags and an optional `--request` file merge into one
   `EvalRequest` (flags win). A generated `eval_id` fills in when absent.
2. **Pre-check** — six ordered steps: `arguments`, `output_dir_conflict`,
   `grader_declaration`, `judge_model`, `dataset_parse`, `session_requires`.
   The order is part of the spec: the directory conflict is checked before the
   dataset so a run with both faults reports exit 4, not 2. Nothing is written
   during this phase, so a rejected run leaves no trace.
3. **Build** — construct the Judge and the Grader. Regex patterns and JSON
   Schemas compile here, before the first sample.
4. **Create** — a hidden temporary directory next to the target.
5. **Execute** — `runner` dispatches samples across `concurrency` workers,
   writing one record per dataset row.
6. **Summarize** — `summary` accumulates counts, score stats and usage;
   `result.Validate` re-checks the counting identities before anything is
   published.
7. **Publish** — write `checksums.sha256`, then one `rename`. The result
   directory appears whole or not at all.

## Backfill

Dispatch and completeness are separate concerns. When a run stops early, the
runner still emits one record per row that produced no evaluation — status
`skipped`, `error.reason` naming `fail_fast` or `interrupt`. Rows already handed
to a worker are tracked in an in-flight set so the backfill covers them too.
That is what keeps the line-count identity true on every
exit path, and it is why the second interrupt signal is ignored: abandoning the
backfill would trade a trustworthy partial result for a truncated one.
