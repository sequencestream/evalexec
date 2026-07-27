# M6 验证报告

对应 `doc/design/M6-interop.md` §7。`make build && make test && make lint` 全绿；`go test -race ./...` 干净。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| 同一 fixture 用 `builtin` 与 `http-json` 跑出语义等价的结果 | ✅ | `TestBuiltinAndExternalGradersAgree` |
| 同一 fixture 用 `openai-chat` 与 `http-json` 两种 Judge 协议跑出语义等价的结果 | ✅ | `TestJudgeProtocolsAgree` |
| 契约测试文档化 | ✅ | `contract/README.md` + 5 个参考实现 |

**超出 DoD 的一项**：Grader 侧实测了**四条**路径而非两条 —— `builtin` / `http-json` / `stdio-jsonl`(Go) / `stdio-jsonl`(**Python**)，判决完全一致。Judge 侧三条。

## 2. 这是「协议优先于 SDK」唯一能被证明的地方

在 M6 之前，所有实现都是 Go 的，「协议不绑定语言」只是一句边界声明。`contract/grader-stdio.py` 把它变成事实：约 60 行、零依赖、跑同一组 fixture、判决与 Go 实现逐项一致。

```
--- PASS: TestBuiltinAndExternalGradersAgree/f01-exact-match-all-pass
--- PASS: TestBuiltinAndExternalGradersAgree/f02-mixed-success-fail
--- PASS: TestJudgeProtocolsAgree
```

### 2.1 「语义等价」比什么

比 `status` / `score` / `label` / `error.code`。
**不比** `reason` 与 `evidence` —— 参考实现是独立写的，措辞不同不代表协议不兼容；要求逐字相同就是在测辞藻而非协议。

## 3. 本阶段定稿的几处协议细节

设计文档提出的问题，实现时的答案：

| 问题 | 定稿 | 理由 |
|---|---|---|
| `entry` 是否做 shell 解析 | **不做**，是单个可执行文件路径 | 引号、转义与注入问题一并免除，而这里根本不需要：要传参数从 `parameters` 读，要更复杂写包装脚本 |
| 外部返回违反不变量的 evaluation（`fail` 带 `score`） | **`protocol_error`**，不静默修正 | 悄悄改正会让实现者永远不知道自己写错了 |
| 外部返回合法的 `status=fail` | **原样采用** | 外部 Grader 有权说"我评不出来"，那不是它的错误 |
| 非 2xx 的响应体 | **不进 `result.json`** | 它可能把整个 call 回显出来。原文只进 `logs/` |
| 崩溃/被 kill 的进程是否复用 | **不复用** | kill 之后无从知道管道里是否还留着一个未读的答案，复用会把一个样本的判决记到另一个头上 |
| `http-json` 的 wire 格式 | 比 OpenAI 简单得多：单条回复、扁平 usage，字段名对齐 `usage.judge_model` | 目的是让别的语言容易实现，不是兼容某个厂商。少一层心智翻译 |

## 4. 三处「不这么写就会挂」的实现细节

`subprocess` 包的注释里都写明了，这里复述原因：

### 4.1 stderr 必须由独立 goroutine 持续读

一个写满 stderr 管道缓冲区的进程会**阻塞在写上**，而宿主在等 stdout —— 死锁，谁都不动。

`TestStdioGraderSurvivesChattyStderr` 用一个每样本往 stderr 写约 1 MB 的脚本验证：3 个样本全部完成。

### 4.2 取消要 kill **进程组**而非进程

脚本自己 fork 出的子进程（Python 包装、shell 管道）否则会留下孤儿，而一个还握着管道的孤儿与"尚未应答的进程"无法区分。`Setpgid` + `kill(-pgid)`。

### 4.3 一行 JSON 可能远超 64 KB

`bufio` 默认窗口是 64 KB，而带 `evidence` 的 `Evaluation` 不小。上限设为与 `dataset` 一致的 32 MB，两侧对齐。

## 5. `exec.Command` 而非 `CommandContext`：一处刻意违反 lint 的地方

`noctx` linter 要求用 `exec.CommandContext`。这里**不能用**：进程是池化的、跨样本存活，把它的生命周期绑到某一个样本的 context 上会让它在第一个样本之后就被杀掉。

取消是**逐调用**处理的（`Call` 里起一个 goroutine 等 `ctx.Done()` 然后 kill 进程组），语义更准确。加了带理由的 `//nolint:noctx`。

## 6. 边界守卫的一次修正

`lint-boundary` 命中了 `contract/` 下的 5 个参考实现（`os.Exit`、`os.Stderr`）。

但那些是**独立的 main 程序** —— stderr 与 `os.Exit` 正是它们该在的地方。守卫原本的表述是「`cmd/` 之外禁止」，而真实规则是「**库代码**不得」。

修正：豁免范围从 `^cmd/` 扩为 `^cmd/|^contract/`，并把理由写进 Makefile 与 `.golangci.yml` 的注释 —— `cmd/` 是二进制，`contract/` 是参考外部组件，两者按定义都是独立进程。

## 7. 前置校验对外部 Grader 同样生效

`TestExternalGraderStillFacesThePreChecks`：一个缺 `output` 字段的数据集 + `http-json` Grader → 退出码 2，且**不产生结果目录**。

这能成立的关键是**声明来自配置文件而非向外部进程查询**。反过来说也有代价，`contract/README.md` 里如实写明了：一个连不上的 Grader 会产生一堆 `protocol_error`，而不是一次干净的前置失败 —— 所以 `requires` 要如实填。

## 8. 失败模式覆盖

`TestExternalGraderFailureModes` 五例：

| 情形 | 记为 |
|---|---|
| 非 2xx | `protocol_error` |
| 响应不是 `Evaluation` | `protocol_error` |
| 响应根本不是 JSON | `protocol_error` |
| `fail` 带 `score`（最易犯的不变量错误） | `protocol_error` |
| 合法的 `fail` | `insufficient_evidence` —— **原样采用** |

另有 `TestStdioGraderCrashIsAProtocolError`（进程死掉 → 两个样本都记 `protocol_error`，行数仍恒等）与 `TestStdioGraderIsNotExecutable`（不可执行 / 不存在的命令 → 前置失败）。

## 9. 验收标准覆盖

| # | 标准 | 覆盖 |
|---:|---|---|
| 21（协议侧） | Python 与 Go 通过同一组 fixtures | `TestBuiltinAndExternalGradersAgree` 的 Python 分支。完整的跨语言校验（含 `result.json` 恒等式）在 M7 |

## 10. 偏离设计文档之处

### 10.1 外部 Grader 不走注册表

设计文档 §1 把 `grader/httpjson` 与 `grader/stdiojsonl` 列为独立包。实现时合并为一个 `grader/external` 包，且**不注册进 registry**：注册表按 `entry` 名索引，而外部协议的 `entry` 是 URL 或路径 —— 它们是**传输方式**而不是具名实现，没有可供注册表索引的东西。根包按 `protocol` 直接构造。

对应地 `graderResolver.Resolve` 对非 `builtin` 协议直接返回 `ErrUnknownEntry`，让 `validate` 回落到「采用配置里的声明」这条路径。

### 10.2 `errors.jsonl` 未实现

设计文档 §4 规划了运行级诊断文件。**本阶段未做**，推到 M7。

理由：它需要的信息目前都已经到了别处 —— 子进程崩溃进 `evaluation.error.message`，stderr 尾部进 `logs/`，连接失败同样进 `error.message`。`errors.jsonl` 的增量价值是「把散落各处的运行级事件汇总到一个文件」，而在没有真实使用反馈之前，很难判断该汇总什么。`02` §7 本身也把它列为**可选**产出。

已在 M7 的遗留清单里。

### 10.3 第三个 Judge 协议名映射

`anthropic-messages` 在 M4 就已可用（aimodel 的 provider 由根包自动注册）。本阶段补了 `TestEveryProtocolConstructs`，确认四个协议名都能解析到 provider —— 之前有一个测试断言 `http-json` 会返回「M6 才有」的错误，现在那个断言本身成了假的，已改写。

## 11. 遗留到 M7

| 项 | 说明 |
|---|---|
| `errors.jsonl` | §10.2 |
| Python 参考校验脚本（读 `expected/result.json` 断言恒等式） | 与 §2 的 Python Grader 不同：那个是被评组件，这个是协议校验器 |
| 21 条验收标准的完整覆盖表 | |
| `anthropic-messages` 的真实端点测试 | 无凭据 |
| 子进程数 = concurrency 的断言 | `Pool.Size()` 已导出，测试未写 |
