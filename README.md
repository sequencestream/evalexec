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

**开发中。** 按 `doc/dev-plan.md` 的 M0–M7 推进，当前完成到 **M0（工程基线）**。
`evalexec` 目前只响应 `--version`；评估管线从 M2 起逐步可用。

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 工程基线、CI、守卫、aimodel 打通 | ✅ |
| M1 | `evalspec` 协议类型 + 跨语言 fixtures | ⏳ |
| M2 | 参数、6 步前置校验、退出码、原子目录 | ⏳ |
| M3 | 串行执行 + 4 个规则 Grader + 根包 `Run` | ⏳ |
| M4 | aimodel Judge 接入 + `llm_judge` | ⏳ |
| M5 | 并发、超时、fail-fast、中断、`skipped` 补写 | ⏳ |
| M6 | 外部 Grader / Judge 协议互操作 | ⏳ |
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

## 开发

```bash
make build      # 构建 bin/evalexec（版本经 -ldflags 注入）
make test       # go test -race ./...
make lint       # 词根守卫 + 库路径边界守卫 + 依赖面守卫 + golangci-lint
make test-e2e   # 真实模型端到端；需 OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL
```

三条守卫是硬约束，不是建议：

- `lint-terms` —— 评分组件一律称 **Grader**；同义的近似词根一律禁止（具体词表见 `Makefile` 的 `lint-terms`）。公开 API 上写错词根，改名就是破坏性变更。
- `lint-boundary` —— `cmd/` 之外禁止 `os.Exit` / `signal.Notify` / `os.Stderr`。evalexec 会被当作库嵌入宿主进程，这三者分别意味着杀进程、抢信号、污染输出。
- `check-deps` —— 直接依赖控制在 aimodel + 一个 JSON Schema 库以内。

## 文档

| 文档 | 内容 |
|---|---|
| [doc/dev-plan.md](./doc/dev-plan.md) | 分阶段开发规划、技术选型、验收标准映射 |
| [doc/design/](./doc/design/) | 各里程碑的阶段设计与验证报告 |

## License

Apache 2.0
