# f06 — the pre-checks, one subcase each

Every subcase here must fail **before any Grader or Judge is called**, and must
leave no result directory behind — not even a temporary one. That is the whole
point of the pre-check phase: a run either validates completely and then
executes, or it fails without having spent a token or written a byte.

Because there is no result to compare, each subcase asserts an exit code and a
failing step in `expected/failure.json` rather than a golden result.

| Subcase | Exit | What it pins |
|---|---:|---|
| `duplicate-grader-flag` | 2 | Two `--grader` flags are rejected. The standard flag package silently keeps the last one, so the CLI has to count occurrences itself. |
| `output-dir-not-empty` | **4** | See below. |
| `grader-missing-requires` | 2 | A Grader must declare `requires`; without it EvalExec cannot validate anything on its behalf. |
| `requires-judge-without-model` | 2 | `requires_judge: true` with no `judge_model`. |
| `judge-auth-env-unset` | 2 | The referenced environment variable is empty. |
| `judge-missing-endpoint` | 2 | `openai-chat` with no endpoint. |
| `duplicate-case-id` | 2 | `case_id` must be unique within the file. |
| `malformed-jsonl` | 2 | An unparseable line. |
| `session-missing-required-field` | 2 | A row missing a declared field. |
| `empty-case-id` | 2 | `case_id` present but empty. |

## Why `output-dir-not-empty` is the interesting one

Its dataset is *also* malformed. Both the directory conflict and the dataset
error are real, and the specification fixes which one wins: the check order is
arguments → output directory → Grader declaration and dataset, so the answer is
**4, not 2**. A natural implementation that validates the dataset first would
pass every other subcase here and still be wrong.

## Why `session-missing-required-field` is not about null

`case-002` has no `output` key at all. Compare `f01`'s `case-003`, whose
`output` is explicitly `null` and which passes: the agent produced no final
output, and saying so is different from failing to say anything. A validator
that treats a missing key and a null value alike will pass `f01` and this case
by accident, in opposite directions.

## Credentials

`judge-auth-env-unset` references `EVALEXEC_FIXTURE_DEFINITELY_UNSET_KEY`,
which nothing sets. Failing here rather than letting a Judge client fall back
to some other key in the environment is what keeps the recorded configuration
honest: a result whose provenance names one endpoint but which actually called
another is worse than no result.
