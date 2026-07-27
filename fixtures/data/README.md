# Protocol fixtures

Shared cases every EvalExec implementation must satisfy. They live outside
`testdata/` on purpose: acceptance criterion 21 requires a Python
implementation and a Go one to agree on the same data, so it has to be
reachable without a Go toolchain.

| Case | Shape | Pins |
|---|---|---|
| `f01-exact-match-all-pass` | golden | The happy path, and a legitimately null `output`. |
| `f02-mixed-success-fail` | golden | A score of 0 is a success; a failure has a null score. |
| `f03-llm-judge-basic` | golden | Judge usage is recorded on failures too. |
| `f04-fail-fast-cancelled` | golden | Backfill preserves the line count; exit code stays 0. |
| `f05-interrupt-cancelled` | invariants | An interrupt yields skipped records, never failed ones. |
| `f06-precheck-failures` | exit codes | Ten pre-check failures, including the ordering rule. |

A **golden** case carries `expected/result.json` and `expected/records.jsonl`.
Compare them after normalizing away the values that legitimately vary between
runs — the generated `eval_id`, timestamps and measured durations — never byte
for byte. Go callers can use `evalspec/evalspectest`.

`dataset_sha256` is **not** normalized: it is taken over the raw file bytes, so
every implementation must arrive at the same value or traceability is broken.

`eval_request_sha256` **is** normalized, permanently. It covers the normalized
request, and normalization makes the dataset and output paths absolute — so the
same evaluation run from two different directories yields the same result and
two different request digests. No shared fixture can pin one. The digest is
still reproducible for a given request, which is what traceability requires.

## No case contains a credential

Judge configurations reference `EVALEXEC_FIXTURE_KEY` by name only. CI sets it
to a sentinel value and then asserts that value appears in no result directory
and no log — which would be impossible to check if the sentinel were also
sitting in a fixture file.
