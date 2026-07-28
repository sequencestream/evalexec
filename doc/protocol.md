# Protocol

Everything needed to produce or consume EvalExec documents, or to implement a
Grader or Judge in any language: the data model first, then the wire
specification for each transport.

`spec_version` is `evalexec/v1alpha1`, carried by every top-level object.

## EvalRequest

Everything one evaluation needs. CLI flags and an optional request file are
normalized into exactly this shape before anything runs.

```json
{
  "spec_version": "evalexec/v1alpha1",
  "eval_id": "0193...",
  "task_id": "customer-service-v1",
  "dataset": {"path": "/abs/sessions.jsonl"},
  "grader": { "...": "GraderSpec" },
  "judge_model": { "...": "JudgeModelSpec" },
  "execution": {"concurrency": 8, "seed": 42, "fail_fast": false},
  "output_dir": "/abs/results/relevance"
}
```

- `eval_id` — optional on input, generated when absent, always present in the
  normalized request and every record.
- `task_id` — a correlation key only. Validated as non-empty, echoed verbatim.
- `output_dir` — must not exist, or must be empty.
- `execution.seed` — recorded in provenance, **not** forwarded to the Judge.

### GraderSpec

| Field | Meaning |
|---|---|
| `id`, `version` | Written into the result verbatim |
| `protocol` | `builtin` / `http-json` / `stdio-jsonl` |
| `entry` | Built-in name, or endpoint URL / executable path |
| `requires` | Session fields this Grader needs |
| `requires_judge` | Whether a `judge_model` must be supplied |
| `parameters` | The Grader's own settings, passed through uninterpreted |
| `timeout_ms` | Bounds one sample's evaluation |

`requires` + `requires_judge` are what let EvalExec fully validate a run
without understanding the Grader — which is how external Graders get the same
pre-checks as built-in ones.

### JudgeModelSpec

| Field | Meaning |
|---|---|
| `protocol` | `openai-chat` / `anthropic-messages` / `http-json` / `stdio-jsonl` |
| `endpoint` | Base URL, or executable for `stdio-jsonl` |
| `auth` | `{"type": "bearer_env", "env": "JUDGE_API_KEY"}` or `{"type": "none"}` |
| `parameters` | Chat request settings; unknown keys are an argument error |
| `timeout_ms` | Bounds one Judge call |

`parameters` accepts ten keys: `model` (required), `temperature`,
`max_completion_tokens`, `max_tokens`, `top_p`, `top_k`, `stop`,
`reasoning_effort`, `parallel_tool_calls`, `response_format`. An unknown key is
rejected rather than dropped — a misspelled `temperatur` silently ignored would
yield a result that looks fine and was graded with the wrong settings.

`Auth` deliberately has nowhere to put a credential; it only names an
environment variable.

## Session

One already-executed agent session; one dataset line. `case_id` is required and
unique within the file. Seven gradeable fields, all optional:

`input`, `output`, `trajectory`, `reference`, `context`, `criteria`, `metadata`

Values are kept as raw JSON so nothing round-trips through a Go type — number
precision, key order and unknown keys survive intact.

**Present-with-null is not absent.** `"output": null` means the agent produced
no final output, and it *satisfies* `requires: ["output"]`. A missing `output`
key does not. Unrecognized keys are ignored for forward compatibility.

`case_id` is not a session field: it is required on every row unconditionally,
so declaring it in `requires` would be legal but meaningless.

## GradeCall

The normalized per-sample request handed to the Grader, identical across the
builtin, `http-json` and `stdio-jsonl` protocols.

```json
{
  "eval_id": "...", "task_id": "...", "case_id": "case-001",
  "input": {}, "output": {}, "trajectory": [], "reference": {},
  "context": {}, "criteria": {}, "metadata": {},
  "parameters": {}
}
```

Only fields actually present in the Session appear. `parameters` is
`grader.parameters` after `--grader-param` overrides.

## Evaluation

The outcome of grading one sample. Always a single object.

```json
{
  "status": "success",
  "score": 1.0,
  "label": "match",
  "reason": "why",
  "evidence": [{"source": "output", "path": "$.messages[0].content", "value": "..."}],
  "usage": {"judge_input_tokens": 0, "judge_output_tokens": 0},
  "latency_ms": 12,
  "error": null
}
```

`status` says whether the **evaluation** succeeded, never whether the agent
performed well:

- `success` — the Grader concluded. A mismatch is a success with `score: 0`.
- `fail` — the Grader reached no usable conclusion. `score` and `label` are
  **null**; `error` is set. A failed evaluation is never counted as a zero and
  never enters the score mean.

`error.code` is one of `insufficient_evidence`, `judge_error`, `timeout`,
`protocol_error`, `internal_error`.

`usage` is filled on failures too — tokens burned by a failure were really
burned. Optional counters (`judge_cache_read_tokens`,
`judge_reasoning_tokens`) are omitted rather than reported as zero, so
rule-based Graders carry no spurious numbers.

## Record

One line of `records.jsonl`, exactly one per dataset row.

```json
{
  "task_id": "...", "eval_id": "...", "case_id": "case-001",
  "sequence": 1,
  "status": "completed",
  "evaluation": { "...": "Evaluation" },
  "started_at": "...", "finished_at": "...",
  "error": null
}
```

| `status` | Meaning |
|---|---|
| `completed` | Reached the Grader; the evaluation may have succeeded or failed |
| `skipped` | Fail-fast or an interrupt stopped the run first |

On a `skipped` record, `evaluation`, `started_at` and `finished_at` are null and
`error` is `{"code": "skipped", "reason": "fail_fast" | "interrupt"}`.

`sequence` is the 1-based dataset position — under concurrency records are
written out of order, so this is what restores input order.

## EvalResult

The top-level result, written as `result.json`.

```json
{
  "spec_version": "evalexec/v1alpha1",
  "eval_id": "...", "task_id": "...",
  "status": "completed",
  "stop_reason": null,
  "request": { "...": "redacted, path-normalized EvalRequest" },
  "artifacts": {"records": "records.jsonl", "errors": "errors.jsonl", "logs": "logs"},
  "counts": {"total": 100, "completed": 100, "skipped": 0},
  "evaluation": {
    "grader_id": "...", "grader_version": "v1",
    "evaluated": 100, "success": 98, "fail": 2,
    "fail_by_code": {"judge_error": 2},
    "score": {"count": 98, "mean": 0.82, "min": 0.0, "max": 1.0}
  },
  "usage": {"judge_model": {"input_tokens": 0, "output_tokens": 0}},
  "provenance": {
    "dataset_sha256": "...", "eval_request_sha256": "...",
    "implementation": {"name": "evalexec", "version": "v0.1.0"}
  },
  "started_at": "...", "finished_at": "...", "duration_ms": 1234,
  "error": null
}
```

| `status` | Meaning |
|---|---|
| `completed` | Every sample was processed. Requires `counts.skipped == 0` |
| `cancelled` | Dispatch stopped early but the backfill and summary finished. Requires `counts.skipped > 0` and a `stop_reason` |
| `failed` | A run-level fault; `error` is mandatory |

`stop_reason` is non-null exactly when `status` is not `completed`:
`fail_fast`, `interrupt`, or `error`.

Identities the result must satisfy, checked before publishing:

- `counts.total` = dataset rows = `records.jsonl` lines
- `counts.total` = `counts.completed` + `counts.skipped`
- `evaluation.evaluated` = `counts.completed` = `success` + `fail`
- `fail` = sum of `fail_by_code` (`skipped` never appears there)
- `score.count` ≤ `success`; `mean`/`min`/`max` are null when `count` is 0

Provenance guarantees the **inputs and configuration** are reproducible, not the
scores — a Judge service can change server-side. `dataset_sha256` is over the
raw file bytes so any implementation in any language computes the same value;
`eval_request_sha256` is over the redacted, canonicalized request JSON stored in
`request`.

## Result directory

```
<output_dir>/
  result.json          the EvalResult
  records.jsonl        one line per dataset row
  checksums.sha256     covers result.json and records.jsonl only
  errors.jsonl         optional diagnostics
  logs/                optional raw Judge exchanges
```

Built in a hidden temporary sibling directory and published with one `rename`.
`errors.jsonl` and `logs/` are excluded from the checksums — they are
diagnostics, not the stable interface — and appear in `artifacts` only when
something was actually written to them.

## External protocols

Graders and Judges may be processes or services written in any language.

| `protocol` | Grader | Judge |
|---|---|---|
| `builtin` | Built-in or downstream-registered | — |
| `openai-chat` | — | Chat Completions-compatible endpoint |
| `anthropic-messages` | — | Anthropic Messages API |
| `http-json` | POST a normalized call, receive an `Evaluation` | POST a simplified request, receive one reply |
| `stdio-jsonl` | One JSON line per call to a subprocess | Same |

Three rules cut across every implementation: a mismatch is a **successful**
evaluation scoring 0; a `fail` carries `"score": null`, never `0`; and a
component that cannot conclude must say so rather than guess.

### Grader over http-json

`grader.entry` is the endpoint URL. The host POSTs a `GradeCall` and expects
200 with an `Evaluation`.

| Host sees | Recorded as |
|---|---|
| Non-2xx | `protocol_error` |
| Not a valid `Evaluation` | `protocol_error` |
| Violates an invariant (e.g. `fail` carrying a score) | `protocol_error` |
| A valid `status: "fail"` | Taken as-is — you may say you cannot conclude |
| Cannot connect | `protocol_error` |
| Exceeds `grader.timeout_ms` | `timeout` |

Error response bodies never reach `result.json` — they can echo the whole call.
They go to `logs/` and `errors.jsonl` only.

### Grader over stdio-jsonl

`grader.entry` is a **single executable path**. There is no shell parsing, so
quoting and injection questions do not arise; pass settings through
`parameters`, or write a wrapper script. Existence and the executable bit are
checked during the pre-check.

```text
stdin   one JSON grade call per line
stdout  one JSON evaluation per line
stderr  diagnostics only, collected into logs/grader-<case_id>.log
```

Four things to get right: flush every line; put only answers on stdout; always
reply, even with a `protocol_error` line for a call you could not parse (silence
makes the host wait for the timeout instead of failing immediately); stderr is
free-form and drained continuously, but only the trailing 64 KB is kept.

**One subprocess per worker (count = `--concurrency`)** — the protocol is
request/response, so a shared process would interleave conversations. After a
timeout or crash the host kills the **process group**, not the process: a script
that forked its own children would otherwise leave orphans, and an orphan still
holding the pipe is indistinguishable from a process that has not answered yet.

### Judge over http-json / stdio-jsonl

Simpler than any vendor's Chat Completions: one reply, flat usage. The payload
is byte-identical between the two transports.

Request:

```json
{
  "model": "judge-model",
  "messages": [{"role": "system", "content": "..."}, {"role": "user", "content": "..."}],
  "temperature": 0,
  "max_completion_tokens": 512
}
```

Sampling parameters appear only when configured. Session fields are wrapped in
tags inside the user message (`<rubric>`, `<input>`, `<output>`,
`<trajectory>`, `<reference>`) rather than nested as JSON — it costs fewer
tokens, and a Session that happens to contain JSON cannot be mistaken for the
envelope around it. `<trajectory>` and `<reference>` appear only when
`use_trajectory` / `use_reference` are on.

Response:

```json
{
  "content": "<the model's reply text>",
  "usage": {"input_tokens": 850, "output_tokens": 80,
            "cache_read_tokens": 0, "reasoning_tokens": 0}
}
```

Field names match `EvalResult.usage.judge_model` (`input_tokens`, not
`prompt_tokens`). Fill in `reasoning_tokens` — a reasoning model routinely
spends more tokens thinking than answering, and omitting it makes usage
irreconcilable with the bill.

On failure: `{"error": {"code": "rate_limited", "message": "..."}}`, recorded as
`judge_error`. No retry.

For `llm_judge`, `content` should hold a JSON object:

```json
{"score": 1, "label": "faithful", "reason": "...",
 "insufficient_evidence": false,
 "evidence": [{"source": "trajectory", "path": "$[0].result.status", "value": "shipping"}]}
```

Parsing is tolerant of code fences and surrounding prose, but **broken JSON is
not repaired** — single quotes and trailing commas stay broken. That is the
model failing to follow the contract, and quietly fixing it would hide a Judge
that needs a better prompt. At least one of `score` / `label` is required;
neither is a `protocol_error`. `score` is recorded verbatim — `min_score` /
`max_score` are scale metadata, neither clamped nor enforced.

## Versioning

| Protocol version | Go module | Promise |
|---|---|---|
| `evalexec/v1alpha1` | `v0.x` | The protocol only gains optional fields; the Go API follows the stability layers, L3/L4 may change |
| `evalexec/v1alpha1` | `v1.x` | The Go API follows the Go compatibility promise; `gorelease` turns from advisory into a hard failure |

Removing a field, changing a type, or changing what a status means = a new
protocol version plus a major bump.
