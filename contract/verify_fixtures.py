#!/usr/bin/env python3
"""Verify EvalExec results against the protocol, independently of Go.

This script is the other half of acceptance criterion 21. contract/grader-stdio.py
proves a *graded component* can be written in another language; this proves a
*result consumer* can — that the counting identities and field semantics of
result.json are readable and checkable without the Go types that produced them.

It uses only the standard library. Requiring a dependency would attach a
footnote to the claim that the protocol is language-neutral.

    python3 contract/verify_fixtures.py fixtures/data
    python3 contract/verify_fixtures.py --self-test

The self-test exists because a checker that has never rejected anything is
indistinguishable from a broken one. It feeds the checker documents that violate
each rule and requires it to complain.
"""

import argparse
import json
import sys
from pathlib import Path

SPEC_VERSION = "evalexec/v1alpha1"

# Error codes that may classify a failed evaluation. "skipped" is deliberately
# absent: a skipped sample was never evaluated, so counting it as a failure
# would break fail == sum(fail_by_code).
FAILURE_CODES = {
    "insufficient_evidence",
    "judge_error",
    "timeout",
    "protocol_error",
    "internal_error",
}


class Problems:
    """Collects every violation rather than stopping at the first.

    Someone fixing an implementation wants the whole list, not one item per run.
    """

    def __init__(self, label):
        self.label = label
        self.items = []

    def check(self, condition, message):
        if not condition:
            self.items.append(message)

    def report(self):
        for item in self.items:
            print(f"{self.label}: {item}", file=sys.stderr)

        return not self.items


def verify(result, records, dataset_lines, label):
    """Check one result and its records against the protocol."""
    p = Problems(label)

    verify_shape(p, result)
    verify_counts(p, result, records, dataset_lines)
    verify_records(p, result, records)
    verify_scores(p, result, records)

    return p


def verify_shape(p, result):
    p.check(result.get("spec_version") == SPEC_VERSION,
            f"spec_version is {result.get('spec_version')!r}, want {SPEC_VERSION!r}")
    p.check(bool(result.get("eval_id")), "eval_id must be non-empty")
    p.check("task_id" in result, "task_id must be present")

    status = result.get("status")
    p.check(status in ("completed", "cancelled", "failed"),
            f"status is {status!r}, want completed, cancelled or failed")

    stop_reason = result.get("stop_reason")
    skipped = result.get("counts", {}).get("skipped", 0)

    if status == "completed":
        # The binding that makes a completed result trustworthy: it processed
        # everything it was given.
        p.check(stop_reason is None, f"stop_reason is {stop_reason!r}, want null when completed")
        p.check(skipped == 0, f"counts.skipped is {skipped}, want 0 when completed")
    elif status == "cancelled":
        p.check(stop_reason in ("fail_fast", "interrupt"),
                f"stop_reason is {stop_reason!r}, want fail_fast or interrupt when cancelled")
        p.check(skipped > 0, f"counts.skipped is {skipped}, want more than 0 when cancelled")
    elif status == "failed":
        p.check(result.get("error") is not None, "error must be present when failed")


def verify_counts(p, result, records, dataset_lines):
    counts = result.get("counts", {})
    evaluation = result.get("evaluation", {})

    total = counts.get("total", 0)
    completed = counts.get("completed", 0)
    skipped = counts.get("skipped", 0)

    p.check(total == completed + skipped,
            f"counts.total {total} != completed {completed} + skipped {skipped}")

    # The identity that separates a partial result from a truncated one.
    p.check(len(records) == total,
            f"records.jsonl has {len(records)} lines, counts.total is {total}")

    if dataset_lines is not None:
        p.check(total == dataset_lines,
                f"counts.total is {total}, the dataset has {dataset_lines} rows")

    evaluated = evaluation.get("evaluated", 0)
    success = evaluation.get("success", 0)
    fail = evaluation.get("fail", 0)

    p.check(evaluated == completed,
            f"evaluation.evaluated {evaluated} != counts.completed {completed}")
    p.check(evaluated == success + fail,
            f"evaluation.evaluated {evaluated} != success {success} + fail {fail}")

    by_code = evaluation.get("fail_by_code") or {}

    p.check(fail == sum(by_code.values()),
            f"evaluation.fail {fail} != sum(fail_by_code) {sum(by_code.values())}")

    for code in by_code:
        p.check(code in FAILURE_CODES,
                f"fail_by_code has {code!r}, which cannot classify a failed evaluation")


def verify_records(p, result, records):
    eval_id = result.get("eval_id")
    task_id = result.get("task_id")
    stop_reason = result.get("stop_reason")

    sequences = []
    counted_completed = 0
    counted_skipped = 0

    for i, rec in enumerate(records, start=1):
        where = f"record {i} ({rec.get('case_id')!r})"

        # Acceptance criterion 9: every record carries the run's eval_id.
        p.check(rec.get("eval_id") == eval_id,
                f"{where} has eval_id {rec.get('eval_id')!r}, want {eval_id!r}")
        p.check(rec.get("task_id") == task_id,
                f"{where} has task_id {rec.get('task_id')!r}, want {task_id!r}")
        p.check(bool(rec.get("case_id")), f"{where} must have a non-empty case_id")

        sequences.append(rec.get("sequence"))

        status = rec.get("status")
        p.check(status in ("completed", "skipped"),
                f"{where} has status {status!r}, want completed or skipped")

        if status == "skipped":
            counted_skipped += 1
            verify_skipped_record(p, where, rec, stop_reason)
        elif status == "completed":
            counted_completed += 1
            verify_completed_record(p, where, rec)

    counts = result.get("counts", {})
    p.check(counted_completed == counts.get("completed"),
            f"counted {counted_completed} completed records, counts says {counts.get('completed')}")
    p.check(counted_skipped == counts.get("skipped"),
            f"counted {counted_skipped} skipped records, counts says {counts.get('skipped')}")

    # Sequences cover 1..n exactly once. Order is not required: under
    # concurrency records are written as they finish, and a consumer sorts.
    expected = set(range(1, len(records) + 1))
    p.check(set(sequences) == expected,
            f"sequences do not cover 1..{len(records)} exactly once: {sorted(sequences)}")


def verify_skipped_record(p, where, rec, stop_reason):
    p.check(rec.get("evaluation") is None, f"{where} is skipped but carries an evaluation")
    p.check(rec.get("started_at") is None, f"{where} is skipped but has a started_at")
    p.check(rec.get("finished_at") is None, f"{where} is skipped but has a finished_at")

    err = rec.get("error")
    if err is None:
        p.check(False, f"{where} is skipped but has no error")
        return

    p.check(err.get("code") == "skipped",
            f"{where} has error.code {err.get('code')!r}, want 'skipped'")
    p.check(err.get("reason") in ("fail_fast", "interrupt"),
            f"{where} has error.reason {err.get('reason')!r}, want fail_fast or interrupt")

    if stop_reason is not None:
        p.check(err.get("reason") == stop_reason,
                f"{where} has error.reason {err.get('reason')!r}, "
                f"but the run's stop_reason is {stop_reason!r}")


def verify_completed_record(p, where, rec):
    evaluation = rec.get("evaluation")
    if evaluation is None:
        p.check(False, f"{where} is completed but has no evaluation")
        return

    p.check(rec.get("error") is None, f"{where} is completed but carries a record-level error")
    p.check(rec.get("started_at") is not None, f"{where} is completed but has no started_at")
    p.check(rec.get("finished_at") is not None, f"{where} is completed but has no finished_at")

    # An evaluation is a single object, never an array.
    p.check(isinstance(evaluation, dict),
            f"{where} has an evaluation of type {type(evaluation).__name__}, want an object")

    status = evaluation.get("status")
    p.check(status in ("success", "fail"),
            f"{where} has evaluation.status {status!r}, want success or fail")

    if status == "fail":
        # The rule the whole status model rests on: a failure is not a zero.
        p.check(evaluation.get("score") is None,
                f"{where} failed but carries score {evaluation.get('score')!r}, want null")

        err = evaluation.get("error")
        if err is None:
            p.check(False, f"{where} failed but has no error")
        else:
            p.check(err.get("code") in FAILURE_CODES,
                    f"{where} has error.code {err.get('code')!r}, "
                    f"which cannot classify a failed evaluation")

    if status == "success":
        p.check(evaluation.get("error") is None,
                f"{where} succeeded but carries an error")


def verify_scores(p, result, records):
    """Check the score statistics against the scores actually recorded."""
    evaluation = result.get("evaluation", {})
    score = evaluation.get("score", {})

    observed = []

    for rec in records:
        ev = rec.get("evaluation")
        if not isinstance(ev, dict):
            continue

        if ev.get("status") != "success":
            continue

        value = ev.get("score")
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            observed.append(float(value))

    count = score.get("count")

    p.check(count == len(observed),
            f"score.count is {count}, but {len(observed)} successful records carry a number")

    p.check(count <= evaluation.get("success", 0),
            f"score.count {count} exceeds success {evaluation.get('success')}")

    if not observed:
        # No measurement means no statistics. Reporting zero would invent one.
        for field in ("mean", "min", "max"):
            p.check(score.get(field) is None,
                    f"score.{field} is {score.get(field)!r}, want null with nothing scored")
        return

    mean = sum(observed) / len(observed)

    p.check(score.get("mean") is not None and abs(score["mean"] - mean) < 1e-9,
            f"score.mean is {score.get('mean')!r}, want {mean!r} "
            f"(the denominator is score.count, not evaluated)")
    p.check(score.get("min") == min(observed),
            f"score.min is {score.get('min')!r}, want {min(observed)!r}")
    p.check(score.get("max") == max(observed),
            f"score.max is {score.get('max')!r}, want {max(observed)!r}")


def read_jsonl(path):
    """Read a JSONL file, ignoring blank lines.

    A trailing newline is normal in a text file and must not become a phantom
    row: that would break the very line-count identity being checked.
    """
    rows = []

    with open(path, encoding="utf-8") as handle:
        for i, line in enumerate(handle, start=1):
            line = line.strip()
            if not line:
                continue

            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError as exc:
                raise SystemExit(f"{path}:{i}: {exc}") from exc

    return rows


def verify_case(case_dir):
    """Verify one fixture case. Returns True when it holds up."""
    expected = case_dir / "expected"

    result_path = expected / "result.json"
    records_path = expected / "records.jsonl"

    if not result_path.exists():
        # Cases whose exact result is not predictable — an interrupted run —
        # carry invariants instead, and are checked by the host's own tests.
        print(f"{case_dir.name}: no golden result, skipping")
        return True

    with open(result_path, encoding="utf-8") as handle:
        result = json.load(handle)

    records = read_jsonl(records_path)

    dataset_path = case_dir / "dataset.jsonl"
    dataset_lines = len(read_jsonl(dataset_path)) if dataset_path.exists() else None

    problems = verify(result, records, dataset_lines, case_dir.name)

    if problems.report():
        print(f"{case_dir.name}: ok ({len(records)} records)")
        return True

    return False


def self_test():
    """Feed the checker documents that break each rule and require complaints."""
    base_result = {
        "spec_version": SPEC_VERSION,
        "eval_id": "eval-1",
        "task_id": "task-1",
        "status": "completed",
        "stop_reason": None,
        "counts": {"total": 2, "completed": 2, "skipped": 0},
        "evaluation": {
            "evaluated": 2, "success": 1, "fail": 1,
            "fail_by_code": {"judge_error": 1},
            "score": {"count": 1, "mean": 1.0, "min": 1.0, "max": 1.0},
        },
    }

    def record(seq, status="completed", evaluation=None, error=None):
        return {
            "task_id": "task-1", "eval_id": "eval-1", "case_id": f"c{seq}",
            "sequence": seq, "status": status, "evaluation": evaluation,
            "started_at": None if status == "skipped" else "2026-07-27T01:00:00Z",
            "finished_at": None if status == "skipped" else "2026-07-27T01:00:00Z",
            "error": error,
        }

    def success(score):
        return {"status": "success", "score": score, "label": "ok", "evidence": [],
                "usage": {"judge_input_tokens": 0, "judge_output_tokens": 0}, "error": None}

    def failure(code="judge_error", score=None):
        return {"status": "fail", "score": score, "label": None, "evidence": [],
                "usage": {"judge_input_tokens": 0, "judge_output_tokens": 0},
                "error": {"code": code}}

    base_records = [record(1, evaluation=success(1.0)), record(2, evaluation=failure())]

    # The valid document must pass, or every negative case below proves nothing.
    if not verify(base_result, base_records, 2, "self-test/valid").report():
        print("self-test: the checker rejected a valid document", file=sys.stderr)
        return False

    cases = []

    def broken(name, mutate_result=None, mutate_records=None, dataset_lines=2):
        result = json.loads(json.dumps(base_result))
        records = json.loads(json.dumps(base_records))

        if mutate_result:
            mutate_result(result)

        if mutate_records:
            mutate_records(records)

        cases.append((name, result, records, dataset_lines))

    broken("total != completed + skipped",
           lambda r: r["counts"].update(total=3))
    broken("record count != total",
           mutate_records=lambda recs: recs.pop())
    broken("dataset row count != total", dataset_lines=3)
    broken("evaluated != success + fail",
           lambda r: r["evaluation"].update(success=2))
    broken("fail != sum(fail_by_code)",
           lambda r: r["evaluation"].update(fail_by_code={"judge_error": 2}))
    broken("fail_by_code keyed by skipped",
           lambda r: r["evaluation"].update(fail_by_code={"skipped": 1}))
    broken("failure carrying a score",
           mutate_records=lambda recs: recs.__setitem__(1, record(2, evaluation=failure(score=0.0))))
    broken("score.count disagrees with the records",
           lambda r: r["evaluation"]["score"].update(count=2))
    broken("statistics present with nothing scored",
           lambda r: r["evaluation"].update(
               success=0, fail=2, fail_by_code={"judge_error": 2},
               score={"count": 0, "mean": 0.5, "min": 0.0, "max": 1.0}),
           lambda recs: recs.__setitem__(0, record(1, evaluation=failure())))
    broken("mean over the wrong denominator",
           lambda r: r["evaluation"]["score"].update(mean=0.5))
    broken("completed with a stop reason",
           lambda r: r.update(stop_reason="fail_fast"))
    broken("cancelled with nothing skipped",
           lambda r: r.update(status="cancelled", stop_reason="fail_fast"))
    broken("record carrying a different eval_id",
           mutate_records=lambda recs: recs[0].update(eval_id="eval-other"))
    broken("duplicate sequence",
           mutate_records=lambda recs: recs[1].update(sequence=1))
    broken("skipped record carrying an evaluation",
           lambda r: r.update(status="cancelled", stop_reason="fail_fast",
                              counts={"total": 2, "completed": 1, "skipped": 1},
                              evaluation={"evaluated": 1, "success": 1, "fail": 0,
                                          "score": {"count": 1, "mean": 1.0,
                                                    "min": 1.0, "max": 1.0}}),
           lambda recs: recs.__setitem__(1, record(
               2, status="skipped", evaluation=success(1.0),
               error={"code": "skipped", "reason": "fail_fast"})))
    broken("skipped record with the wrong error code",
           lambda r: r.update(status="cancelled", stop_reason="fail_fast",
                              counts={"total": 2, "completed": 1, "skipped": 1},
                              evaluation={"evaluated": 1, "success": 1, "fail": 0,
                                          "score": {"count": 1, "mean": 1.0,
                                                    "min": 1.0, "max": 1.0}}),
           lambda recs: recs.__setitem__(1, record(
               2, status="skipped", error={"code": "timeout", "reason": "fail_fast"})))
    broken("wrong spec version",
           lambda r: r.update(spec_version="evalexec/v2"))

    undetected = []

    for name, result, records, dataset_lines in cases:
        problems = verify(result, records, dataset_lines, f"self-test/{name}")
        if not problems.items:
            undetected.append(name)

    for name in undetected:
        print(f"self-test: the checker accepted a broken document: {name}", file=sys.stderr)

    if undetected:
        return False

    print(f"self-test: ok ({len(cases)} violations detected, 1 valid document accepted)")

    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("data", nargs="?", help="the fixtures/data directory")
    parser.add_argument("--self-test", action="store_true",
                        help="check that the checker itself rejects broken documents")
    args = parser.parse_args()

    if args.self_test:
        return 0 if self_test() else 1

    if not args.data:
        parser.error("a data directory is required unless --self-test is given")

    root = Path(args.data)
    if not root.is_dir():
        parser.error(f"{root} is not a directory")

    cases = sorted(d for d in root.iterdir() if d.is_dir())
    if not cases:
        parser.error(f"{root} holds no fixture cases")

    ok = True

    for case_dir in cases:
        if not (case_dir / "expected").is_dir():
            # The pre-check case asserts exit codes, not results.
            print(f"{case_dir.name}: not a result case, skipping")
            continue

        if not verify_case(case_dir):
            ok = False

    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
