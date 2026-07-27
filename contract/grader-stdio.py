#!/usr/bin/env python3
"""A reference stdio-jsonl Grader in Python.

This file is the evidence behind one of EvalExec's boundary claims: the protocol
is JSON over a pipe, not a Go SDK. It grades the same fixtures as
contract/grader-stdio/main.go and produces semantically equivalent results, in
about sixty lines and with no dependencies.

The contract:

  stdin   one JSON grade call per line
  stdout  one JSON evaluation per line, flushed immediately
  stderr  diagnostics only, collected by the host into logs/

Two rules are worth copying rather than rediscovering:

  * A mismatch is a *successful* evaluation with a score of 0. The Grader
    compared the values and reached a conclusion; that is its job done.
  * Being unable to conclude is a failure with score `null` — never 0. A zero
    is a measurement, and inventing one puts a number nobody took into the
    average.
"""

import json
import sys


def expected_output(reference):
    """Return (value, found) for reference.expected_output."""
    if not isinstance(reference, dict):
        return None, False

    if "expected_output" not in reference:
        return None, False

    return reference["expected_output"], True


def grade(call):
    """Grade one call and return the evaluation object."""
    expected, found = expected_output(call.get("reference"))

    if not found:
        return {
            "status": "fail",
            "score": None,
            "label": None,
            "reason": "there is no expected output to compare against",
            "evidence": [],
            "usage": {"judge_input_tokens": 0, "judge_output_tokens": 0},
            "error": {
                "code": "insufficient_evidence",
                "message": "reference.expected_output is absent",
            },
        }

    actual = call.get("output")
    matched = actual == expected
    label = "match" if matched else "mismatch"

    return {
        "status": "success",
        "score": 1.0 if matched else 0.0,
        "label": label,
        "reason": f"compared output with reference.expected_output: {label}",
        "evidence": [
            {"source": "output", "path": "$", "value": actual},
            {"source": "reference", "path": "$.expected_output", "value": expected},
        ],
        "usage": {"judge_input_tokens": 0, "judge_output_tokens": 0},
        "error": None,
    }


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            call = json.loads(line)
        except json.JSONDecodeError as exc:
            print(f"cannot decode grade call: {exc}", file=sys.stderr)
            # Answer anyway. The host is waiting for exactly one line, and going
            # silent would hang it until the timeout instead of failing fast.
            evaluation = {
                "status": "fail",
                "score": None,
                "label": None,
                "evidence": [],
                "usage": {"judge_input_tokens": 0, "judge_output_tokens": 0},
                "error": {
                    "code": "protocol_error",
                    "message": "the grade call could not be decoded",
                },
            }
        else:
            print(f"grading {call.get('case_id')}", file=sys.stderr)
            evaluation = grade(call)

        # ensure_ascii=False keeps non-Latin text readable in the record; the
        # host reads UTF-8 either way.
        sys.stdout.write(json.dumps(evaluation, ensure_ascii=False) + "\n")
        # Flushing every line is not optional: buffered output leaves the host
        # waiting for an answer that is sitting in this process's memory.
        sys.stdout.flush()


if __name__ == "__main__":
    main()
