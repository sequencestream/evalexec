# Changelog

This file records changes to the L3 component packages, as the stability
layering requires. A change to the L1 protocol (`evalspec`, `fixtures`) bumps
`spec_version` as well; L4 (`cli`) promises no compatibility and is not
recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
versions follow Semantic Versioning.

## [Unreleased]

No version has been tagged yet.

### Added

- **Protocol** (L1, `evalexec/v1alpha1`): the two top-level abstractions
  `EvalRequest` and `EvalResult`, Session data rows, per-sample records, the
  three status enums and the counting identities.
- Two `v1alpha1`-compatible extensions to the core protocol:
  - `usage.judge_cache_read_tokens` and `usage.judge_reasoning_tokens`, both
    optional. Measured against DeepSeek, 70% of output tokens are reasoning
    tokens; without a separate counter, usage cannot be reconciled with the
    bill.
  - `judge_model.protocol` gains `anthropic-messages`; `auth.type` gains
    `none`.
- **CLI**: a single entry point `evalexec [flags]`, six pre-checks in fixed
  order, exit codes `0/2/3/4/130`.
- **Graders**: built-in `exact_match`, `contains`, `regex`, `json_schema` and
  `llm_judge`; external protocols `http-json` and `stdio-jsonl`; downstream
  programs may register their own entries.
- **Judges**: `openai-chat`, `anthropic-messages`, `http-json` and
  `stdio-jsonl`, all converging on one `Judge` interface.
- **Execution**: a concurrent worker pool, three timeout levels, fail-fast,
  interrupt handling, `skipped` backfill and atomic publication.
- **Releases**: cross-compiled binaries for linux, darwin and windows on amd64
  and arm64, published by a tag push.

### Known trade-offs

None of the following are defects; they are documented boundaries.

- `--seed` is **not forwarded** to the Judge. The canonical chat request has no
  `seed` field; the seed is recorded in `provenance` only, and bit-identical
  scores are not promised.
- `structured_output` for `llm_judge` is **off by default**. Structured output
  is not portable across OpenAI-compatible endpoints (DeepSeek returns 400 for
  a `json_schema` request), and EvalExec does not retry.
- `records.jsonl` guarantees the **line count** and a complete `sequence`, not
  the line order.
- `score.mean` is **not bit-identical across concurrency levels**, because
  float addition is not associative.
- A caller-supplied `eval_id` is **not checked for uniqueness** — EvalExec has
  no global view with which to verify it.
- `eval_request_sha256` covers the normalized request including absolute paths,
  so it is **inherently machine-dependent** and cannot be pinned in a shared
  fixture. It remains reproducible for a given request.
