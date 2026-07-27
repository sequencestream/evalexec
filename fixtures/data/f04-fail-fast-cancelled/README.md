# f04 — fail-fast stops dispatch, backfill keeps the line count

`case-002` fails, fail-fast is on, and the run stops dispatching. The three
remaining samples are backfilled as `skipped` **in input order**, so
`records.jsonl` still has exactly five lines — one per dataset row. That
identity holds on every stopping path; it is what makes a partial result
trustworthy rather than merely truncated.

Backfilling calls neither the Grader nor the Judge. It does, however, still
read the rest of the dataset, because it needs each remaining `case_id` and
`sequence`. "Stop dispatching" is not "exit immediately".

The exit code is **0**, not a failure code. Fail-fast is a stopping policy the
caller explicitly asked for, so the command did exactly what it was told. That
the result is incomplete is expressed by `status: cancelled` and
`counts.skipped`, not by the exit code.

Note what does *not* trigger fail-fast: `case-001` and the backfilled rows
would score anywhere from 0 to 1 and none of that matters. Only
`evaluation.status = fail` — the evaluation not working — stops the run, because
EvalExec does not interpret scores.

`concurrency` is 1 so the stopping point is deterministic. Under concurrency
the number of completed samples depends on timing; only the line count and the
`sequence` coverage stay fixed.
