# f05 — interrupt mid-run

This case carries `expected/invariants.json` rather than a golden
`result.json`, and the difference is deliberate. Where the interrupt lands
depends on scheduling, so how many samples completed is genuinely not
predictable. Writing a golden file would mean inventing a stopping point and
then testing that the implementation reproduces the invention.

What *is* predictable is asserted instead:

- the exit code is **130**;
- `records.jsonl`, if published, has exactly 8 lines carrying sequences 1..8
  once each — the line-count identity survives an interrupt;
- at least one record is `skipped`, each with `{"code":"skipped","reason":"interrupt"}`;
- **no record is a `fail`**. A sample cancelled mid-flight is `skipped`, never
  a failed evaluation. This is the single easiest thing to get wrong: the
  cancelled Judge call returns a context error, and mapping that to a timeout
  or a failure would misreport work that was never finished as work that was
  finished badly.

The result directory may legitimately be absent: 130 does not promise the
wind-up completed. A caller distinguishes the two by whether the directory
exists — which is why publication is atomic, so a half-written directory is
never observable.
