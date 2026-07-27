# M5 并发、超时、fail-fast、中断与原子写 — 阶段设计

对应 `doc/dev-plan.md` §4 M5（设计稿 S4）。工程复杂度最高的一段：设计稿的一致性约束几乎全落在这里。

一条贯穿全阶段的不变量：**`records.jsonl` 的行数恒等于数据集行数，在每一条停止路径上都成立。** 这是「部分结果仍然可信」与「结果被截断了」的分界。

---

## 1. 交付物

| 包 | 变化 |
|---|---|
| `runner` | 串行循环 → worker 池；fail-fast；中断；`skipped` 补写；`logs/` 接线 |
| `cmd/evalexec` | `signal.Notify`（唯一允许的地方）+ 二次中断处理 |
| 根包 | 传递取消、把 `Outcome.Stopped` 接进 `summary.Status` |
| fixtures | `f04`、`f05` 端到端 |

---

## 2. 并发结构

M3 已经按并发的形状写好了管道，本阶段只换掉派发端：

```text
                    ┌─→ worker 1 ─┐
dataset reader ─→ jobs ─→ worker 2 ─┼─→ results ─→ [单写入 goroutine] ─→ records.jsonl
                    └─→ worker N ─┘                                  └─→ summary
```

- `jobs` 通道容量 = `concurrency`，读取端一边读一边喂，**内存只保留并发窗口**；
- worker 数 = `--concurrency`；
- 写入端与汇总端**完全不动** —— 这是 M3 提前按并发形状写的回报。

### 2.1 行序不保证，行数保证

dev-plan 开放点 #1 定稿：**不保证行序**，只保证行数与 `sequence` 完备。消费方自行按 `sequence` 排序。写进 README。

### 2.2 `mean` 的浮点确定性

M3 遗留。并发下记录到达顺序会变，浮点加法不满足结合律，`mean` 的末位可能随运行变化。

定稿：**不修**，在 README 注明 `mean` 不保证跨并发度逐位一致。理由是修的代价（把所有分数留在内存、按 `sequence` 排序后再累加）换来的是第 15 位小数的稳定性，而 fixtures 的归一化比对本就不比到那一位。

---

## 3. 停止：一个状态机，两个触发源

fail-fast 与中断共用同一套停止与补写逻辑，只有 `StopReason` 不同。这是设计稿明确的（`03` §3「停止后的补写规则（fail-fast 与中断共用）」）。

```go
type stopper struct {
    mu     sync.Mutex
    reason evalspec.StopReason
    fired  bool
    cancel context.CancelFunc
}
// Stop records the first reason and cancels the worker context. Later calls
// are ignored: the first cause is the true one.
```

**第一个触发源赢。** 若 fail-fast 已触发、随后收到 SIGINT，`stop_reason` 仍是 `fail_fast` —— 那才是这次运行提前结束的原因。

### 3.1 fail-fast 只由 `evaluation.status=fail` 触发

分数高低**永不**触发。这是验收标准 12 与 fail-fast 语义的交点：EvalExec 不解释分数，所以它无法知道 0 分算不算「坏」。

判定点必须在**写入 goroutine**里，不在 worker 里 —— 见 §4。

### 3.2 中断：`signal.Notify` 只在 `cmd/`

`runner` 只认 `context.Context` 的取消。库调用方用自己的 ctx 控制中断。`lint-boundary` + `forbidigo` 已经守住这条线。

**二次中断**（dev-plan 开放点 #7）：

| 第几次 | 行为 |
|---|---|
| 1 | 停止派发，走完补写与发布 |
| 2 | **忽略** —— 补写正在进行，打断它会产出行数不一致的目录 |
| 3 | 强制退出，**不发布目录** |

第 3 次用 `os.Exit(130)` 直接退（`cmd/` 内合法）。临时目录留在磁盘上，但因为从未 rename，调用方看到的是「目录不存在」—— 与「未完成」一致。

---

## 4. 在途样本的归属：本阶段最容易错的一处

dev-plan §7 点名：

> fail-fast 触发瞬间某个 worker 可能刚写完 `evaluation`。规则是「已写入 `evaluation` 的算 `completed`，未写入的算 `skipped`」，必须以**写入 channel 的先后**为唯一裁决点，不能靠 worker 自己判断。

因此：

1. worker 完成一个样本 → 把 `Record` 送进 `results` 通道，**不做任何停止判断**；
2. 写入 goroutine 逐条写盘并累加，**然后**检查是否该触发 fail-fast；
3. 触发后取消 worker context；在途 worker 的 Grader 调用返回 `context.Canceled`；
4. worker 拿到取消错误后 **不产出记录**，直接退出。

关键点：**取消导致的样本不进 `results` 通道**。它们会在补写阶段被记为 `skipped`。这样「谁算 completed」由通道顺序单点裁决，没有竞态。

`judge.ErrCancelled`（M4 已就位）正是 worker 用来识别这种情况的信号。M4 的 `llm_judge.Grade` 已经向上传播它而非产出 `fail`。规则 Grader 走 `context.Canceled`，`runner` 用 `errors.Is` 识别。

### 4.1 `runner.classify` 需要一处修改

M3 的 `classify` 把非 `DeadlineExceeded` 的错误全归 `internal_error`。本阶段要在它之前插一条：`errors.Is(err, context.Canceled) || errors.Is(err, judge.ErrCancelled)` → **不产出记录**，返回一个哨兵让 worker 丢弃。

但 `runner` 不能 import `judge`（会让 `judge` 的 aimodel 依赖渗进 `runner`）。解法：`judge.ErrCancelled` 已经包装了…… 不，它是独立哨兵。定稿：**让 `judge.ErrCancelled` 包装 `context.Canceled`**，于是 `runner` 只需判 `context.Canceled` 一条，不必 import `judge`。这是对 M4 的一个小修正，且语义更准确 —— 它本来就是一次取消。

---

## 5. `skipped` 补写

停止后，为所有**未写入 `evaluation`** 的样本按输入顺序补写。

```text
已写入的 sequence 集合 = {已进过 results 通道的}
补写 = 数据集全部 sequence − 已写入
```

实现要点：

- **补写路径仍需读完剩余数据集**，为了拿 `case_id` 与 `sequence`（`03` §3 明确）。「停止派发」不是「立即退出」；
- 补写**不调用** Grader 或 Judge；
- 补写记录形状固定：`status=skipped`、`evaluation=null`、时间字段 `null`、`error={"code":"skipped","reason":...}` —— M1 的 `NewSkippedRecord` 已保证；
- 补写按 `sequence` 升序写出（输入顺序），与并发段的乱序形成对比。这是刻意的：补写没有并发，没理由乱序。

**补写或汇总失败 → 退出码 3。** 那种情况下无法形成可信 EvalResult。

### 5.1 读取端已经消费掉的行怎么办

派发时 reader 已经推进过。停止时 reader 停在某个位置，后面的行还没读。但**已派发未完成**的样本，其 `case_id`/`sequence` 已经在内存里（在途集合），不需要重读。

所以补写数据源有两处：在途集合（内存）+ reader 剩余部分（磁盘）。两者按 `sequence` 合并排序。

---

## 6. 三级 context

```text
全局 ctx（调用方 / SIGINT）
  └─ worker ctx（stopper.cancel）
       └─ grader.timeout_ms
            └─ judge_model.timeout_ms（M4 已实现）
```

M3 实现了 `grader.timeout_ms`；M4 实现了 `judge_model.timeout_ms`。本阶段加中间那层 worker ctx。

---

## 7. `logs/` 接线（M4 遗留）

M4 的 `transport.Recorder` 已能按 `case_id` 缓冲并脱敏，但没接到磁盘。

定稿：**由 `runner` 的写入 goroutine 接线** —— 它已经在管记录落盘，顺手取走对应 exchange 最自然，且 Grader 不必知道结果目录的存在。

```go
type LogSink interface {
    // Keep writes the recorded exchanges for one sample. Called only when the
    // sample failed, or always under --debug.
    Keep(caseID string, exchanges []transport.Exchange) error
    Discard(caseID string)
}
```

`runner` 对 `LogSink` 编程，根包注入一个写 `logs/judge-<case_id>.jsonl` 的实现。`runner` 因此**不需要** import `judge/transport`…… 但 `[]transport.Exchange` 是它的参数类型。折中：`LogSink` 接口放在 `runner`，参数用 `any`？不 —— 那放弃了类型安全。

**定稿**：`LogSink` 定义在 `result` 包（它已经拥有目录），参数类型用 `transport.Exchange`。`runner` import `result`（本已通过根包间接依赖）与 `judge/transport`（纯数据类型，不含 aimodel）。检查：`judge/transport` 只 import 标准库 —— 确认无 aimodel 渗透。

---

## 8. 退出码收口

| 情形 | `status` | `stop_reason` | 退出码 |
|---|---|---|---|
| 全部完成 | `completed` | null | `0` |
| fail-fast 停止 | `cancelled` | `fail_fast` | **`0`** |
| 中断，目录已发布 | `cancelled` | `interrupt` | `130` |
| 中断，目录未发布 | — | — | `130` |
| 补写/汇总失败 | `failed` | — | `3` |
| 输出目录冲突 | — | — | `4` |

`exitcode.FromResult` 已实现前四行；中断路径需要 `cmd/` 在 ctx 被信号取消时返回 `130` 即使 `Run` 返回了 `cancelled` 结果 —— 而 `FromResult` 看 `stop_reason` 就能判断，所以不需要额外分支。

---

## 9. 测试策略

dev-plan M5 列的四类，逐条落地：

### 9.1 并发确定性

可控 Grader：按 `case_id` 决定延迟与结果。比对时把 `records.jsonl` 按 `sequence` 排序后再比 —— 行序不保证是规格的一部分，测试不能假设它。

### 9.2 中断测试用真实子进程

**必须起真实二进制**：`signal.Notify` 只在 `cmd/`，库路径无法测。

```go
cmd := exec.Command(binary, args...)
cmd.Start()
// 等到 records.jsonl 出现且有若干行
cmd.Process.Signal(os.Interrupt)
cmd.Wait()  // 断言退出码 130
```

「等到有若干行」不能靠 `time.Sleep` —— 那会在慢机器上假失败。轮询临时目录里的 `records.jsonl` 行数。

难点：临时目录名含 `eval_id`，测试要能找到它。方案：显式传 `--eval-id`，目录名可预测。

### 9.3 竞态检测与压力

`go test -race`；1000 样本 × concurrency 16，Judge 用本地 `httptest.Server`。

### 9.4 连接复用

断言高并发下 `httptest.Server` 观察到的连接数 ≤ `concurrency`。用 `httptest.Server.Config.ConnState` 计数。

### 9.5 二次中断

子进程连发两次 SIGINT，断言仍然发布目录且行数一致。第三次强制退出的路径**不测** —— 它需要精确的时序控制，且行为是「不发布」，与第一次中断失败的情况不可区分。文档写明。

---

## 10. 验证方式

| 验证项 | 手段 |
|---|---|
| `f04` 端到端 | fail-fast，与黄金文件比对，断言退出码 0 |
| `f05` 端到端 | 真实子进程 + SIGINT，断言 `expected/invariants.json` 的每一条 |
| **任意并发度下行数恒等** | concurrency ∈ {1,2,4,16} × 同一数据集，行数与 sequence 覆盖 |
| 单条失败不终止其余 | 混合数据集，断言 fail 与 success 并存 |
| 中断产生 `skipped` 而非 `fail` | f05 的核心断言 |
| fail-fast 只由 fail 触发 | 全 0 分数据集 + `--fail-fast`，断言跑完全部样本 |
| 在途样本归属 | 可控 Grader 让一个样本在触发瞬间完成，断言它算 `completed` |
| 输出目录不被静默覆盖 | M3 已有，复查 |
| 连接复用 | §9.4 |
| `logs/` 只在 fail 时保留 | 混合数据集，断言只有 fail 样本有日志文件 |
| 密钥不进 `logs/` | 扫描扩展到 `logs/`（M4 的扫描已遍历目录树） |

**DoD**：`f04`、`f05` 通过；任意 `--concurrency` 下行数恒等；单条失败不终止其余样本；输出目录不被静默覆盖。

**覆盖验收标准**：9（含停止路径）、11、16、17、19
