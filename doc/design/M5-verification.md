# M5 验证报告

对应 `doc/design/M5-concurrency.md` §10。`make build && make test && make lint` 全绿；`go test -race ./...` 干净。

**dev-plan 的 v0.1.0 MVP 节点达成** —— `04-mvp-scope.md` 的全部「MVP 必做」已完成。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| `f04` 端到端通过 | ✅ | `TestFailFastCaseEndToEnd` 精确匹配黄金文件（2 completed / 3 skipped） |
| `f05` 端到端通过 | ✅ | `TestInterruptPublishesACompleteResult` 起真实子进程发 SIGINT，逐条断言 `invariants.json` |
| 任意 `--concurrency` 下行数恒等 | ✅ | `TestLineCountIdentityAtEveryConcurrency`（1/2/4/16 × 50 行） |
| 单条失败不终止其余样本 | ✅ | `TestOneFailureDoesNotStopTheOthers` |
| 输出目录不被静默覆盖 | ✅ | M3 已有，复查通过 |

## 2. 两处设计缺陷，都是 f04 的黄金文件抓到的

### 2.1 `summary.Status` 违反自己的不变量

第一次跑 f04 直接失败：

```
the summary does not satisfy the counting identities:
counts.skipped: must be greater than 0 when status is cancelled, got 0
```

原实现按「stopper 是否触发」决定 `status`。但一个在最后一个样本已派发之后才到达的 fail-fast 信号**没有任何东西可停** —— 报 `cancelled` 既误导，又违反「`cancelled` ⟹ `skipped > 0`」。

定稿：**`skipped` 计数说话，不是「是否请求过停止」**。`skipped == 0` ⟹ `completed`。顺带补了一条：`skipped > 0` 但 stopper 未触发 ⟹ `failed` —— 样本无故消失是缺陷，补写它会让行数看起来对而结果静静少了评估。

### 2.2 fail-fast 在小数据集上停不住

改完 §2.1 后，f04 得到 3 completed / 2 skipped，而黄金文件是 2/3。

根因是**流水线跑在判决前面**。fail-fast 的判定点在写入 goroutine（这是 dev-plan §7 要求的：以写入顺序为唯一裁决点），但 worker 会在自己的判决被处理之前**预取下一个样本**。concurrency=1 下：

```
worker 送出 rec2（失败）→ 立刻从 jobs 收到 row3 → 开始评估 row3
                          └─ writer 此时才写 rec2 并触发 stopper
```

于是 row3 被评估了。而 f04 的 README 写着「concurrency 是 1 所以停止点是确定的」。

两步修正：

1. **通道改为无缓冲**（原来按 `concurrency` 缓冲），dispatcher 不能跑在前面；
2. **worker 等自己的记录落盘后再取下一个样本** —— `results` 通道携带一个 ack channel，writer 写完即 close。

第 2 步才是关键。代价是每样本一次 channel 往返，而 Grader 调用比它慢几个数量级。收益是 concurrency=1 的停止点真正确定，规范里那句话成立。

再加一道保险：worker 收到样本后**先查 workerCtx**，已停止就不开始评估 —— 「停止派发新样本」的字面含义。

## 3. 取消 ≠ 失败：dev-plan 的头号风险，本阶段闭环

M4 做好了分类，本阶段落地为 `skipped`：

| 环节 | 实现 |
|---|---|
| `judge.ErrCancelled` **包装 `context.Canceled`** | 本阶段对 M4 的修正。原来是独立哨兵，`runner` 要识别它就得 import `judge`，把 aimodel 依赖渗进来。包装之后 `runner` 只判 `context.Canceled` 一条 |
| `runner.grade` 返回 `(Record, bool)` | 取消时返回 `false`，**不产出记录**。哪些样本算 `completed` 由通道顺序单点裁决，一条"从未完成的工作"的记录会破坏它 |
| 补写 | 在途集合（内存）+ reader 剩余部分（磁盘）合并，按 `sequence` 升序 |

三个测试从不同层面钉住：`TestCancellationProducesSkippedNotFailed`（库级，断言**无任何 fail 记录**）、`TestInterruptPublishesACompleteResult`（进程级，同样断言）、M4 的 `TestClassifyDistinguishesCancellationFromDeadline`（单元）。

## 4. 中断只能用真实子进程测

`signal.Notify` 只在 `cmd/`（`forbidigo` 守着），库级测试测的是 context 取消，是另一回事。

三个子进程测试：

| 测试 | 覆盖 |
|---|---|
| `TestInterruptPublishesACompleteResult` | 退出码 130；发布的目录完整且 `Validate()` 通过；400 行全在；`skipped>0`；**无 fail 记录** |
| `TestSecondInterruptIsIgnored` | 连发两次 SIGINT，仍发布完整目录 |
| `TestInterruptBeforeAnyWorkLeavesNoDirectory` | 信号与启动竞争，断言不留临时目录 |

两处让测试稳定而非"碰巧过"的细节：

- **轮询而非 `sleep`**：等临时目录里的 `records.jsonl` 达到 N 行再发信号。固定 sleep 在负载高的机器上就是一个假失败。临时目录名含 `eval_id`，测试显式传 `--eval-id` 使其可预测。
- **慢 Grader 用回溯正则而非 sleep**：协议里没有"请你慢一点"的 Grader 参数，而会 sleep 的 fake 得编译进被测二进制。`(?:ab)+c` 对一个长的不匹配串回溯，每样本花几毫秒。

`TestSecondInterruptIsIgnored` 初版还断言 stderr 出现「already winding down」，实测 stderr 里只有第一条消息 —— 收尾比第二个信号的处理更快。这是真实竞态（handler 在 `signals` 与 `done` 之间 select），要求这条消息只会让测试变脆而非变严。改为只断言行为（目录完整发布），并写明原因。

## 5. `logs/` 接线（M4 遗留）

由 `runner` 的写入 goroutine 接线 —— 它已在管记录落盘，顺手取走对应 exchange 最自然，Grader 不必知道结果目录存在。

| 规则 | 测试 |
|---|---|
| 只有 `fail` 样本留日志（`--debug` 除外） | `TestLogsAreKeptOnlyForFailures`：6 样本 2 失败 → `logs/` 恰好 2 个文件 |
| 无失败则**不创建** `logs/` 目录 | `TestStressAtHighConcurrency`。空目录会让人以为诊断跑过但一无所获 |
| `artifacts.logs` 仅在有日志时出现 | 同上两个测试 |
| 日志中 `Authorization` 已替换、无密钥 | `TestLogsRedactTheCredential`，且**反向断言 `Bearer ***` 存在** —— 头必须是"在场且被替换"而非"消失"，知道发过凭据是排查鉴权失败的一部分 |

`LogSink` 接口定在 `runner`，实现放 `result`（它拥有目录）。参数类型是 `transport.Exchange`：核对过 `judge/transport` 只 import 标准库，没有 aimodel 渗透。

## 6. 连接复用实测

```
peak connections = 8 for 60 requests at concurrency 8
```

Go 默认 `MaxIdleConnsPerHost` 是 2，不调优的话这一轮会开出几十条连接。`TestConnectionsAreReusedUnderConcurrency` 用 `httptest.Server.Config.ConnState` 数峰值并断言 ≤ concurrency。

## 7. 压力与竞态

`TestStressAtHighConcurrency`：1000 样本 × concurrency 16，Judge 用本地 `httptest.Server`。断言 `total`/`completed`/`success`/`score.count` 全等 1000，Judge 恰好应答 1000 个不同样本，行数恒等，且规模下无密钥泄漏。

`go test -race ./...` 全部包干净。

## 8. 验收标准覆盖

| # | 标准 | 覆盖 |
|---:|---|---|
| 9（含停止路径） | `records.jsonl` 行数恒等于数据集行数 | 4 个并发度 + fail-fast + 中断 + 压力，共 8 处断言；`evalexec.go` 内另有运行期自检 |
| 11 | 被取消的样本记 `skipped` 而不是 `fail` | §3 三个测试 |
| 16 | 顶层 status 三值及其与 `skipped` 的绑定 | §2.1 修正 + `EvalResult.Validate()` 落盘前无条件调用 |
| 17 | fail-fast 返回 `0`；中断返回 `130` 且尽量发布 | `TestFailFastExitsZero`、三个子进程测试 |
| 19 | 输出目录不被静默覆盖 | M3 的 `TestOutputDirectoryIsNotOverwritten` 复查 |

## 9. 偏离设计文档之处

### 9.1 `mean` 的浮点确定性：不修，写进 README

设计文档 §2.2 已定此策。并发下记录到达顺序变化，浮点加法不满足结合律，`mean` 末位可能抖动（1e-15 量级）。修的代价是把所有分数留在内存、按 `sequence` 排序后再累加，换来第 15 位小数的稳定 —— 而 fixtures 的归一化比对本就不比到那一位。README 写明，并指出要逐位复现就用 `--concurrency 1`。

### 9.2 第三次中断不测

设计文档 §9.5 已说明。它需要精确时序控制，而它的行为（不发布目录）与"第一次中断的收尾没来得及完成"不可区分 —— 测不出差别的断言不是断言。行为写在 `signal.go` 的注释与 README 里。

### 9.3 通道从缓冲改无缓冲

设计文档 §2 写的是「`jobs` 通道容量 = `concurrency`」。实测发现缓冲是 §2.2 那个缺陷的一半原因，改为无缓冲 + 逐样本 ack。内存边界不变（仍只保留并发窗口），确定性变强。

## 10. 遗留到后续阶段

| 项 | 阶段 |
|---|---|
| `http-json` / `stdio-jsonl` 的 Grader 与 Judge 协议 | M6 |
| `errors.jsonl`（运行级诊断，可选产出） | M6 |
| Python 跨语言校验脚本 | M7 |
| 21 条验收标准的完整覆盖表 | M7 |
| `anthropic-messages` 无凭据可测 | M7 |
