# Architecture

How the pieces fit together: what the program is made of, and in what order it
does things.

## Two deliverables

1. Binary `evalexec` — the atomic CLI.
2. Go library `github.com/sequencestream/evalexec` — import it to reuse the
   protocol types, implement a custom Grader, or embed evaluation in your own
   orchestrator.

## Stability layers

Because every package is on the public API surface, stability is declared in
layers rather than enforced by the compiler:

| Layer | Packages | Promise |
|---|---|---|
| **L1 protocol** | `evalspec`, `fixtures` | Lives with `spec_version`; within `evalexec/v1alpha1` only optional fields are added |
| **L2 extension** | root `Run`, `grader`, `judge` | Go compatibility promise from v1.0; interfaces kept deliberately narrow |
| **L3 components** | `dataset`, `validate`, `runner`, `summary`, `result`, `exitcode`, `redact`, `version` | May change during v0 |
| **L4 internals** | `cli` | **No compatibility promise**; not for downstream use |

`CHANGELOG.md` records changes to L3; an L1 change bumps `spec_version` as well.

## Package map

```
cmd/evalexec  →  cli  →  evalexec.Run
                            ├── validate    six pre-checks, writes nothing
                            ├── redact      request snapshot + sha256
                            ├── grader      registry, builtin/, external/
                            ├── judge       transports, provider/
                            ├── dataset     JSONL reader, case_id index
                            ├── runner      dispatch, timeout, stop, backfill
                            ├── summary     counts, score stats, run status
                            ├── result      temp dir, publish, logs, checksums
                            └── exitcode    the only place codes are decided
```

`evalerr` classifies failures; `exitcode` is the single place that maps a
failure to a process exit code, so the mapping cannot drift.

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
