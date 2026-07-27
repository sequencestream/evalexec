# M7 验证报告

对应 `doc/design/M7-release.md` §7。`make all` 全绿（15 个包），`go test -race ./...` 干净，`make test-e2e` 在 DeepSeek 上通过。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| 21 条验收标准全部有对应自动化测试 | ✅ | `doc/acceptance.md`，逐条指向**具体测试函数名** |
| 下游冒烟仓库能编译运行 | ✅ | `examples/consumer`（独立 module），`make test-consumer` 与 CI 步骤 |
| 二进制与库同时可用 | ✅ | `.goreleaser.yaml` + README 双栏；未打 tag，见 §6 |

## 2. 下游冒烟仓库抓到两个真实缺陷

这是本阶段最有价值的产出。一个独立 module、三个用例，第一次运行就暴露了两处只有跨模块调用才会碰到的问题。

### 2.1 库调用方省略 `eval_id` 会得到非法结果

```
consumer: built-in Grader: run: cannot write records:
record 1 (c1) is not valid: eval_id: must not be empty
```

`eval_id` 的生成写在 `cli` 包（L4）的规范化步骤里。命令行路径没问题，**库路径完全没有** —— 而验收标准 5 要求「EvalResult 必须包含非空 `eval_id`」。

根因是把生成看作「参数解析的一部分」，而它实际是「每个结果的必要属性」。修法：

- 生成器上移到根包（`evalexec.IDGenerator` / `UUIDv7Generator`），`cli` 用类型别名指向它，不再有第二份实现；
- `Run` 在开头补齐缺失的 `eval_id`；
- 顺带补上 dev-plan M3 列出但我漏实现的 `WithIDGenerator`。

### 2.2 自定义 Grader 的参数被全盘拒绝

```
grader_declaration: grader "answer_length" parameters are invalid:
unknown parameter "max_runes"; answer_length accepts []
```

`Declaration.Params` 为空时，`checkParams` 拒绝任何参数，错误信息还是 `accepts []`。

M3 的设计文档里我记下过这个风险（「如果你没声明参数名，我们就不管」），但没实现。定稿：

- `Params == nil` ⟹ **不检查**（该 Grader 不管自己的参数）；
- `Params == []string{}` ⟹ 一个都不接受。

这个区分是可表达且诚实的。`TestUndeclaredParamsAreNotPoliced` 四例钉住；`examples/consumer` 则**声明**了它的参数，示范推荐做法。

### 2.3 为什么必须是独立 module

同一 module 内的 `examples/` 只能证明代码编译，证明不了「从外部 import 时该导出的都导出了」。上面第一个缺陷恰好是这一类：一个被当成 L4 内部职责的必要步骤，从模块内部看不出问题。

CI 里是独立一步（`working-directory: examples/consumer`）。`check-deps` 跳过它（有自己的 go.mod）。

## 3. Python 协议校验器

```
self-test: ok (17 violations detected, 1 valid document accepted)
f01-exact-match-all-pass: ok (3 records)
f02-mixed-success-fail: ok (4 records)
f03-llm-judge-basic: ok (3 records)
f04-fail-fast-cancelled: ok (5 records)
f05-interrupt-cancelled: no golden result, skipping
f06-precheck-failures: not a result case, skipping
```

它与 M6 的 `grader-stdio.py` 是两个方向：那个是**被评组件**，这个是**结果消费者**。合起来证明协议在生产端与消费端都不绑定 Go。

**纯标准库**是硬要求 —— 否则「协议不绑定语言」就要附加「只要你装得上这些包」的脚注。

**`--self-test` 检出 17 种违规**并接受一份合法文档。校验器本身可以失败，这一点与 M4 给密钥扫描器加自测、M1 给 fixtures 扫描器加自测是同一个原则：一个从未报过警的检查器与一个失效的检查器无法区分。

自测里最值得留着的三例：`fail` 带 `score`、`fail_by_code` 以 `skipped` 为键、`score.count == 0` 却有 `mean` —— 三条最容易被外部实现搞错的不变量。

## 4. 验收覆盖表：写它的过程发现了预期中的缺口

`M7-release.md` §3 预测「预计会发现覆盖缺口」，并点名了标准 1 与标准 4。实际如此，已补：

| 标准 | 补的测试 |
|---|---|
| 1（无子命令即可完成一次运行） | `TestSingleInvocationCompletesARun` —— 原本只有「不被拒绝」的覆盖（`TestValidRunReachesExecution`），没有「结果真的落地」的覆盖 |
| 4（`task_id` 只校验非空并原样输出） | `TestTaskIDIsEchoedVerbatim`，6 种不透明取值：含空格、含中文、含 JSON、含 `#`、含路径穿越形状；外加 `TestEmptyTaskIDIsRejected` |

标准 4 的取值是刻意挑的：`task_id` 是关联键而不是领域对象，所以测试喂进去的都是「任何试图解释它的代码都会出错」的值，并要求原样返回。

**表里也如实写了一处部分不适用**：标准 5 的「全局唯一」对调用方提供的 `eval_id` **不校验**（dev-plan 开放点 #6 定稿）—— EvalExec 没有全局视野可以验证唯一性，假装验证过反而危险。表里写明这一半是调用方责任，而不是让它看起来是满格的。

指向测试函数名而非阶段，是因为后者在测试被重命名或删除后不会失效，等于没有约束力。

## 5. `errors.jsonl`：M6 遗留，落地一个最小版本

M6 推迟的理由是「没有真实使用反馈时难判断该汇总什么」。本阶段的原则：**只记那些否则会消失的东西**。

| 事件 | 现有落点 | 进 `errors.jsonl` |
|---|---|:--:|
| 子进程崩溃 | `evaluation.error.message` + `logs/` | 否 |
| 连接失败 | `evaluation.error.message` | 否 |
| 日志写入失败 | 仅 `Diag`，库模式下是 `io.Discard` —— 等于丢失 | **是** |
| 补写期间的读取失败 | 运行级 error | **是** |
| 数据集在运行中被改动 | 同上 | **是** |

这让文件小、有明确用途，且不与已有落点重复 —— 一个大部分内容与记录流重复的诊断文件，没人会读。

不计入 `checksums.sha256`；`artifacts.errors` 仅在有内容时出现（与 `logs/` 同规则：空文件会让人以为诊断跑过而一无所获）。

## 6. 发布：配置就绪，不实际发布

`.goreleaser.yaml`：3 系统 × 2 架构，注入 `version.*`，CI 里 `goreleaser check` 验证配置。

**不在本阶段打 tag 或推 release** —— 那是仓库所有者的决定，不是开发阶段的动作。设计文档 §6.1 已如此定稿。

两处值得说明的配置选择：

- `mod_timestamp: {{ .CommitTimestamp }}` 且 `Date` 用 `CommitDate` 而非构建时刻：同一个 commit 的两次构建应当打出同一个版本，否则 `provenance` 就从「标识一个构建」变成了「标识一个时刻」。
- **fixtures 随 release 打包**：其他语言的实现者不必 clone 仓库就能自测。

## 7. README 双栏

库那栏的三段示例**引用** `examples/consumer/` 而不是复制 —— 复制的示例会腐烂，而这个是 CI 跑的。

另加了「`Run` 的几条承诺」一节，把散在各处的保证收成五条：不写任何东西直到校验通过、目录一次出现或完全不出现、行数恒等、失败永不记 0 分、诊断默认 `io.Discard`。最后一条是库形态特有的，容易被忽略。

## 8. aimodel 升级检查清单

`doc/aimodel-upgrade.md`。核心是把 M0 核对过的每个假设列成可重验的表，并**区分「编译能过」与「语义没变」** —— 表里有一半的项目即使编译通过也可能已经变了语义（`Register` 是否仍对重名 panic、`NewClient` 是否仍改写传入 client 的 `Timeout`、环境变量兜底顺序）。

也写明了第 6 步（真实端点）不能省：`httptest.Server` 打的是**我们以为**的 wire 格式，只有真实端点能证明 provider 仍按那个格式说话。

爆炸半径检查：`grep -rl 'vogo/aimodel' --include='*.go' .` 的结果应当只有 `judge/` 下的文件。

## 9. E2E 测试移入 `e2e/`

本阶段按要求把真实模型测试从根包与 `judge/` 收进 `e2e/` 独立包。

收益不只是整洁：`e2e/` 现在有自己的辅助函数集，与主测试套件解耦，也不再需要从根测试包借 `stage` / `readResult` 这类为 fixture 比对设计的助手。顺带发现根测试包的 `testClock` 在 e2e 里毫无意义（活模型的答案本来就不可复现，钉住时间戳只会掩盖真实耗时），已删掉并写明原因。

新增 `TestLLMJudgeUnderConcurrencyAgainstALiveModel`：连接池与逐调用超时只有对着一个真的需要时间应答的服务才算被验证过。

最终实测：

```
TestAimodelReachesTheEndpoint                    PASS  (34/40 completion tokens were thinking)
TestLLMJudgeAgainstALiveModel                    PASS  success=2 fail=1 mean=0.5
TestLLMJudgeUnderConcurrencyAgainstALiveModel    PASS  cache_read=768
```

三条路径每次都走对：忠实 → success score 1，不忠实 → **success score 0**，无证据 → fail/`insufficient_evidence`。第二条是整个状态模型的核心。

## 10. 守卫的第三次修正

`lint-boundary` 这一阶段又命中了 `examples/consumer/main.go`。与 M6 的 `contract/` 同理：它是独立 main 程序（还是独立 module），`os.Exit` 与 `os.Stderr` 正是它该在的地方。

豁免范围现在是 `^cmd/|^contract/|^examples/`，Makefile 注释写明每一处的理由。三次修正指向同一件事：守卫的真实规则是「**库代码**不得」，而按目录表述会在每次新增独立程序时重犯 —— 权威检查是 `.golangci.yml` 里 AST 级的 `forbidigo`，Makefile 的 grep 是无 golangci-lint 时的快速预检。

## 11. 遗留事项（明确不做或需外部条件）

| 项 | 状态 |
|---|---|
| 打 tag 与推 release | 仓库所有者的决定 |
| `anthropic-messages` 的真实端点测试 | 无凭据。构造期已由 `TestEveryProtocolConstructs` 覆盖 |
| `gorelease` 从 advisory 转硬失败 | v1.0 时 |
| `CaseIndex` 的磁盘实现 | 超过百万行数据集时的可选优化，接口已留 |
| 子进程数 = concurrency 的断言 | `Pool.Size()` 已导出，未写测试。行为由 `TestConnectionsAreReusedUnderConcurrency` 间接覆盖 |
| 第三次中断的强制退出路径 | 不可测（与「第一次中断收尾未完成」不可区分），行为写在 `signal.go` 与 README |
