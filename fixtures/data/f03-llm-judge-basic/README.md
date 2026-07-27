# f03 — llm_judge against recorded Judge responses

`judge-responses.jsonl` holds one recorded reply per `case_id`. Tests replay it
through a fake chat completer, so CI never calls a live model: a fixture that
depended on a real Judge would be neither reproducible nor free.

Two properties are pinned here that are easy to get wrong.

**`case-003` records usage even though it failed.** The Judge was called, read
640 prompt tokens and wrote 32, then declined to conclude. Those tokens were
spent and appear both in the record and in the run total (2360 = 850+870+640).
Dropping usage on failure would make the summary disagree with the bill.

**A `score` of 0 on `case-002` is still a success.** The Judge did its job and
concluded the answer was unfaithful. Contrast `case-003`, which reached no
conclusion at all and therefore has a `null` score that stays out of the
average — `score.count` is 2, not 3.

The credential is referenced by environment variable name only. No fixture in
this repository contains a key, and the CI leak scan asserts the sentinel value
of `EVALEXEC_FIXTURE_KEY` appears in no result directory or log.
