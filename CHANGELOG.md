# 变更记录

本文件记录 L3 组件包的变更（`doc/dev-plan.md` §1.3 的稳定性分层要求）。
L1 协议（`evalspec`、`fixtures`）的变更同时提 `spec_version`；L4（`cli`）不承诺兼容，不记录。

格式参照 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循语义化版本。

## [未发布]

首个版本尚未打 tag。M0–M7 的完整实现过程与设计决策记录在 `doc/design/`。

### 新增

- **协议**（L1，`evalexec/v1alpha1`）：`EvalRequest` / `EvalResult` 两个顶层抽象，
  Session 数据行、逐样本记录、三层状态枚举、计数恒等式。
- 相对 `02-core-spec.md` 的两处 `v1alpha1` 兼容扩展：
  - `usage.judge_cache_read_tokens` 与 `usage.judge_reasoning_tokens`（可选）。
    实测 DeepSeek 有 70% 的输出 token 是思考 token，不单列会让用量与账单对不上。
  - `judge_model.protocol` 新增 `anthropic-messages`；`auth.type` 新增 `none`。
- **CLI**：单入口 `evalexec [flags]`，6 步固定顺序前置校验，退出码 `0/2/3/4/130`。
- **Grader**：内置 `exact_match` / `contains` / `regex` / `json_schema` / `llm_judge`；
  外部协议 `http-json` / `stdio-jsonl`；下游可注册自定义 entry。
- **Judge**：`openai-chat` / `anthropic-messages` / `http-json` / `stdio-jsonl`，
  统一收敛到一个 `Judge` 接口。
- **执行**：并发 worker 池、三级超时、fail-fast、中断、`skipped` 补写、原子发布。
- **契约**：`contract/` 下 5 个参考外部实现（Go × 4 + Python × 1）与协议规格文档。
- **校验**：`contract/verify_fixtures.py`，纯标准库，自带 `--self-test`。

### 已知取舍

以下不是缺陷，是写进文档的边界：

- `--seed` **不透传**给 Judge。aimodel v0.5.0 的 canonical 请求没有 `seed` 字段；
  seed 只记入 `provenance`，不承诺评分逐字复现。
- `llm_judge` 的 `structured_output` **默认关闭**。结构化输出在 OpenAI 兼容端点之间
  不可移植（DeepSeek 对 `json_schema` 请求返回 400），而 EvalExec 不重试。
- `records.jsonl` **不保证行序**，只保证行数与 `sequence` 完备。
- `score.mean` **不保证跨并发度逐位一致**（浮点加法不满足结合律）。
- 调用方提供的 `eval_id` **不校验唯一性** —— EvalExec 没有全局视野可以验证它。
- `eval_request_sha256` 覆盖含绝对路径的规范化请求，因此**天生机器相关**，
  无法在共享 fixture 中钉住。对给定请求它仍然可复现。
