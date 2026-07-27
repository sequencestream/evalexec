# f01 — exact_match, every sample passes

The simplest complete run: a rule Grader, no Judge, every evaluation
succeeding. It pins the happy path of the counting identities
(`total = completed + skipped`, `evaluated = success + fail`) and the shape of
a successful record.

`case-003` is the interesting one: its `output` is explicitly `null` and so is
`reference.expected_output`. The agent produced no final output, the reference
says it should not have, and the two match. A row with no `output` key at all
would instead be a pre-check failure — see `f06`.
