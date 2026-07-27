# EvalExec

一次进程调用 = 一个 `EvalRequest` → 一个 Grader → 一个 `EvalResult`。

EvalExec 不建设评估平台，也不提供组合工作流。它只完成一个原子操作：接收一个评估请求，用**一个** Grader 跑完一个数据集，输出一个结果目录。它**不执行被评 Agent**，只消费上游已经产生的 Session 记录。

```bash
evalexec \
  --task-id customer-service-v1 \
  --dataset ./sessions.jsonl \
  --grader ./relevance-grader.json \
  --judge-model ./judge-model.json \
  --output-dir ./results/relevance
```

需要两个 Grader？调用两次。循环、并行、重试、结果合并与质量门禁都由上层编排器承担 —— 而编排器可以直接 import 本模块，不必反复 fork 进程。

## 状态

**开发中。** 按 `doc/dev-plan.md` 的 M0–M7 推进，当前完成到 **M6**。

功能面已完整：规则 Grader、LLM Judge、并发与中断、外部 Grader / Judge 协议。
已对 DeepSeek 真实端点做过端到端验证，也验证过同一组 fixture 在 Go 与 Python
外部实现下判决一致。M7 做跨语言协议校验脚本与双形态发布。

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 工程基线、CI、守卫、aimodel 打通 | ✅ |
| M1 | `evalspec` 协议类型 + 跨语言 fixtures | ✅ |
| M2 | 参数、6 步前置校验、退出码、原子目录 | ✅ |
| M3 | 串行执行 + 4 个规则 Grader + 根包 `Run` | ✅ |
| M4 | aimodel Judge 接入 + `llm_judge` | ✅ |
| M5 | 并发、超时、fail-fast、中断、`skipped` 补写 | ✅ |
| M6 | 外部 Grader / Judge 协议互操作 | ✅ |
| M7 | 跨语言一致性验证、二进制与库双发布 | ⏳ |

## 两个交付物

本项目**不使用 `internal/`**，两个交付物平级：

1. **二进制** `evalexec` —— 原子 CLI；
2. **Go 库** `github.com/sequencestream/evalexec` —— 供其他项目 import，复用协议类型、实现自定义 Grader、或把评估执行嵌入自己的编排器。

代价是所有包都在公开 API 面上，因此稳定性分层声明如下：

| 层 | 包 | 承诺 |
|---|---|---|
| **L1 协议** | `evalspec`、`fixtures` | 与 `spec_version` 同生命周期。`evalexec/v1alpha1` 内只增可选字段 |
| **L2 扩展点** | 根包 `Run`、`grader`、`judge` | v1.0 后遵守 Go 兼容性承诺；接口刻意收窄 |
| **L3 组件** | `dataset`、`validate`、`runner`、`summary`、`result`、`exitcode`、`redact`、`version` | v0 期间可变更，v1.0 起遵守兼容性承诺 |
| **L4 实现细节** | `cli` | **不承诺兼容**，不建议下游依赖 |

## 边界

- 一次命令、一个 Grader、一个结果；没有 `run` / `validate` / `gate` 子命令。
- 不抽象 Task，只透传 `task_id`。
- 执行与评估分离：EvalExec 消费已有 Session，不调用被评 Agent。
- 协议优先于 SDK：JSON/JSONL 与 HTTP/stdio 协议不绑定任何语言。
- 执行错误不伪装成低分：评估状态只有 `success` / `fail`，`fail` 携带原因码且**不计零分**；未执行的样本记 `skipped`。
- 不解释分数：`score` / `label` 原样来自 Grader，`min_score` / `max_score` 只透传，是否达标由外部判断。
- 输出目录已存在且非空则拒绝运行，**不提供 `--force`**。
- **不重试、不限流**：429 / 5xx 计为 `judge_error`；需要重试请由上层重跑整个评估。

## 并发与停止

```bash
evalexec --concurrency 8 --fail-fast ...
```

**`records.jsonl` 的行数恒等于数据集行数，在每一条退出路径上都成立** —— 正常跑完、
fail-fast 停止、用户中断都一样。这是"部分结果仍然可信"与"结果被截断了"的分界。

| 情形 | `status` | `stop_reason` | 退出码 |
|---|---|---|---:|
| 全部处理完（即使全部 `fail`） | `completed` | null | `0` |
| fail-fast 停止 | `cancelled` | `fail_fast` | **`0`** |
| 用户中断 | `cancelled` | `interrupt` | `130` |
| 运行级故障 | `failed` | — | `3` |

**fail-fast 返回 0**：它是调用方显式请求的停止策略，命令做了它被要求做的事。
结果不完整由 `status` 与 `counts.skipped` 表达，不由退出码表达。
而且**只有 `evaluation.status=fail` 会触发它** —— 分数高低永不触发，因为 EvalExec
不解释分数，无从判断 0 分算不算"坏"。

**被取消的样本记 `skipped`，不是 `fail`。** 一个中途被放弃的样本从未完成，把它记成
超时或内部错误，等于把"从未发生的工作"报成"做坏了的工作"。

中断的升级规则：第一次停止派发并走完补写与发布；第二次**忽略** —— 补写正是让部分结果
可信的那一步；第三次放弃并**不发布**目录。因为发布是一次 `rename`，调用方永远不会看到
半成品目录，只会看到"目录不存在"。

两条不保证的事：

- **行序不保证。** 并发下记录按完成顺序写出。每行携带 `sequence`，消费方自行排序。
- **`score.mean` 不保证跨并发度逐位一致。** 浮点加法不满足结合律，而记录到达顺序随
  并发度变化。差异在 1e-15 量级；要逐位复现请用 `--concurrency 1`。

## 内置 Grader

| `entry` | 比较方式 | 参数 |
|---|---|---|
| `exact_match` | `output` 与 `reference` 的期望值做 JSON 语义相等 | `reference_path`（默认 `$.expected_output`） |
| `contains` | `output` 文本须包含**全部**期望子串 | `reference_path`（默认 `$.expected_contains`）、`case_sensitive` |
| `regex` | `output` 文本匹配正则 | `pattern`（必填）、`case_sensitive` |
| `json_schema` | `output` 通过 JSON Schema 校验 | `schema`（必填） |
| `llm_judge` | 交由 LLM Judge 评判 | `rubric`（必填）、`min_score`、`max_score`、`use_reference`、`use_trajectory`、`structured_output` |

**「不匹配」是成功的评估，记 0 分**；只有「评不出来」（没有可比对的期望值、Judge 调用失败）才是
`fail`，且 `fail` 不带分数、不计入均值。这条区分是整个状态模型的基础。

`pattern` 与 `schema` 在**前置校验期**就编译一次：配置写错应当在跑第一个样本之前失败。

## LLM Judge

```json
{
  "protocol": "openai-chat",
  "endpoint": "https://api.deepseek.com",
  "auth": {"type": "bearer_env", "env": "JUDGE_API_KEY"},
  "parameters": {"model": "deepseek-v4-flash", "temperature": 0},
  "timeout_ms": 60000
}
```

`protocol` 支持 `openai-chat` 与 `anthropic-messages`（`http-json` / `stdio-jsonl` 在 M6）。
`parameters` 接受 10 个键：`model`（必填）、`temperature`、`max_completion_tokens`、
`max_tokens`、`top_p`、`top_k`、`stop`、`reasoning_effort`、`parallel_tool_calls`、
`response_format`。**未知键报参数错误**，不静默丢弃 —— 一个拼错的 `temperatur` 被悄悄忽略，
会产出一份看起来正常、实际用错设置评出来的结果。

密钥只能由 `auth.env` 引用环境变量名。命令行传密钥的参数（`--api-key` 等）会被明确拒绝并
提示改用 `auth.env`；配置文件里若出现疑似密钥，运行会被**拒绝**而不是脱敏 —— 悄悄抹掉会让
你以为密钥被安全处理了，而它仍然写在磁盘上的那个文件里。

### 三点需要知道的限制

- **`--seed` 不透传给 Judge。** aimodel v0.5.0 的 canonical 请求没有 `seed` 字段。
  seed 只记入 `provenance`，`llm_judge` 靠 `temperature=0` 求稳。**不承诺评分逐字复现。**
- **`structured_output` 默认关闭。** 结构化输出在 OpenAI 兼容端点之间不可移植 ——
  DeepSeek 对 `json_schema` 请求直接返回 400。而 EvalExec 不重试，被拒的请求就是丢掉整个
  样本。prompt 里约定 JSON + 容错解析在所有 provider 上都有效，所以它是默认路径；确认端点
  支持时再开 `structured_output: true`。
- **不重试。** 429 与 5xx 都计为 `judge_error`。需要重试请由上层重跑整个评估。

## 外部 Grader 与 Judge

Grader 与 Judge 都可以是别的语言写的进程或服务：

| `protocol` | Grader | Judge |
|---|---|---|
| `builtin` | 内置或下游注册 | — |
| `openai-chat` | — | Chat Completions 兼容端点 |
| `anthropic-messages` | — | Anthropic Messages API |
| `http-json` | POST 规范化 call，收 `Evaluation` | POST 简化请求，收单条回复 |
| `stdio-jsonl` | 子进程一问一答，每行一个 JSON | 同左 |

协议规格与 5 个参考实现（Go × 4 + **Python** × 1）在 [`contract/`](./contract/)。
Python 版不是示范代码，而是「协议不绑定语言」这条边界的**证据** —— 它与内置 Grader
跑同一组 fixture 并产出一致判决，由 CI 保证。

`stdio-jsonl` 每个 worker 一个子进程（**子进程数 = `--concurrency`**）：协议是一问一答，
共享进程会让对话交错。超时或崩溃后 kill **进程组**而非进程 —— 脚本自己 fork 的子进程
否则会留下孤儿，而一个还握着管道的孤儿与"尚未应答的进程"无法区分。

## 注册自定义 Grader

下游程序可以注册自己的 Grader，用 `protocol: "builtin"` + 自定义 `entry` 直接跑，
不必走子进程：

```go
grader.Register("my_grader", func(spec evalspec.GraderSpec) (grader.Grader, error) {
    return &myGrader{}, nil
})

result, err := evalexec.Run(ctx, request)
```

这不扩大 `evalexec` 二进制的能力面 —— 它只注册内置的五个 entry；自定义 entry 只在
下游自己构建的二进制里可见。

## 开发

```bash
make build      # 构建 bin/evalexec（版本经 -ldflags 注入）
make test       # go test -race ./...
make lint       # 词根守卫 + 库路径边界守卫 + 依赖面守卫 + golangci-lint
make test-e2e   # 真实模型端到端；需 OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL
```

`make lint` 包含 `lint-secrets`：它跑出一个真实结果目录并扫描全部文件（含 `logs/`），
断言不含哨兵密钥、不含任何密钥形态、`Authorization` 已被替换 —— 并另跑一个测试验证
**扫描器确实会触发**。一个从未报过警的泄漏检测器，和一个失效的检测器无法区分。

三条守卫是硬约束，不是建议：

- `lint-terms` —— 评分组件一律称 **Grader**；同义的近似词根一律禁止（具体词表见 `Makefile` 的 `lint-terms`）。公开 API 上写错词根，改名就是破坏性变更。
- `lint-boundary` —— `cmd/` 之外禁止 `os.Exit` / `signal.Notify` / `os.Stderr`。evalexec 会被当作库嵌入宿主进程，这三者分别意味着杀进程、抢信号、污染输出。
- `check-deps` —— 直接依赖控制在 aimodel + 一个 JSON Schema 库以内。

## 文档

| 文档 | 内容 |
|---|---|
| [doc/dev-plan.md](./doc/dev-plan.md) | 分阶段开发规划、技术选型、验收标准映射 |
| [doc/design/](./doc/design/) | 各里程碑的阶段设计与验证报告 |
| [contract/README.md](./contract/README.md) | **外部 Grader / Judge 的协议契约**，含 5 个参考实现 |

## License

Apache 2.0
