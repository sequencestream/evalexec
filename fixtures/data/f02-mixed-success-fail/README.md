# f02 — successes, a mismatch, and a genuine failure

This case exists to pin the distinction that the whole status model rests on.

`case-002` is a **success with a score of 0**. The agent gave the wrong answer,
but the evaluation itself worked perfectly: the Grader compared the values and
reached a conclusion. A low score is not a failed evaluation.

`case-003` is a **failure with a null score**. The reference carries no
`expected_output`, so the Grader could reach no conclusion at all. It is
recorded as `fail` with `insufficient_evidence`, and its score is `null` — not
zero. Counting it as a zero would drag the mean down with a number nobody ever
measured, which is why `score.count` (3) is lower than `evaluated` (4).

Note that `case-003` still satisfies the pre-check: `requires` demands a
`reference` key, and the row has one. Whether that reference is *useful* is a
question only the Grader can answer, and it answers it at evaluation time.
