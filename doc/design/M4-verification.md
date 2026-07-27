# M4 验证报告

对应 `doc/design/M4-judge.md` §10。`make build && make test && make lint` 全绿；`make test-e2e` 在 DeepSeek 上通过。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| `f03` 端到端通过 | ✅ | `TestLLMJudgeCaseEndToEnd` 回放 `judge-responses.jsonl`，与黄金文件归一化比对 |
| 5 个 `error.code` 各有一个通过的用例 | ✅ | 见 §3 |
| 任何 `fail` 记录的 `score == null` 且未进入 `score.count` | ✅ | `TestUsageIsRecordedOnFailure`、`TestScoreIsDiscardedOnFailure`、f03 黄金文件（`score.count`=2 而 `evaluated`=3） |
| 结果目录与 `logs/` 中不出现假密钥串 | ✅ | `make lint-secrets` → `TestNoSecretReachesTheResultDirectory` |
| `Authorization` 头在日志中已被替换 | ✅ | `transport.redactHeaders`；扫描断言无 `Bearer <sentinel>` |

## 2. 真实模型实测抓到一个设计缺陷

这是本阶段最有价值的产出。设计文档 §7.3 写的是：

> 优先 `ResponseFormat`（OpenAI `json_schema`）；失败回退到「prompt 约定 JSON + 容错解析」。

我照此实现了**解析层**的容错，但把 `response_format` 做成了**无条件发送**。第一次跑 DeepSeek 端到端，三个样本全部 `judge_error`：

```
HTTP 400 (invalid_request_error)
{"error":{"message":"This response_format type is unavailable now", ...}}
```

原方案里的「失败回退」在 EvalExec 中**根本行不通**：回退意味着重试，而「不重试」是明确写在边界里的。一次被拒的请求就是丢掉整个样本。

**定稿**：结构化输出改为 `llm_judge` 的 `structured_output: bool` 参数，**默认关闭**。prompt 里约定 JSON + 容错解析在所有 provider 上都有效，所以它是默认路径；确认端点支持时再显式开启。已回写 dev-plan §2.7 与 README。

修正后的实测结果正是三条路径：

```
faithful     status=success score=1 label=忠实     ← 与工具事实一致
unfaithful   status=success score=0 label=不忠实   ← 答错了，但评估成功
no-evidence  status=fail    score=<nil>            ← insufficient_evidence
usage: input=874 output=782 reasoning=544 cache_read=512
```

第二行是整个状态模型的核心：**答错是成功的评估记 0 分**，只有第三行的「评不出来」才是 `fail` 且不带分数。这条在单测里断言过，但由真实模型走出来更有说服力。

`reasoning=544 / output=782` 与 `cache_read=512` 也说明 M1 新增的两个可选用量字段不是理论需求 —— 70% 的输出 token 是思考 token，不单列就与账单对不上。

## 3. 五种 `error.code` 的覆盖

| code | 构造方式 | 测试 |
|---|---|---|
| `insufficient_evidence` | fake Judge 返回 `{"insufficient_evidence": true}` | `TestVerdictParsing/refusal is a failure` |
| `judge_error` | `httptest.Server` 返回 500 / 429 / 不可解析内容 / 空 choices | `TestErrorCodes` 四例 |
| `timeout` | `httptest.Server` 延迟超过 `timeout_ms` | `TestErrorCodes/slower than the timeout` |
| `protocol_error` | Judge 返回坏 JSON / 无 JSON / 空回复 / 既无 score 又无 label | `TestVerdictParsing` 四例 |
| `internal_error` | Grader panic（M3 的 `runner`）；另加 `llm_judge` 拿到 nil Judge | `TestGraderPanicBecomesOneFailedSample`、`TestNilJudgeIsPermittedForDeclaration` |

## 4. 取消 vs 超时：dev-plan 的头号风险

dev-plan §7 把「`context.Canceled` 被误判为 `timeout`/`fail`」列为头号风险，并指出它只在 M5 的停止路径暴露。本阶段先把分类做对并单独测到：

- `judge.Classify` **先**用 `errors.Is(err, context.Canceled)` 判断，映射到哨兵 `judge.ErrCancelled`；
- `judge.CodeOf` 对它返回 `(_, false)` —— 明确表示「这不算一次失败的评估」；
- `llm_judge.Grade` 遇到它**向上传播 error**，而不是产出 `fail` 记录，让 M5 的 runner 记 `skipped`。

三个测试：`TestCancellationIsNotATimeout`（真实 HTTP 取消）、`TestClassifyDistinguishesCancellationFromDeadline`（单元）、`TestCancellationPropagates`（Grader 层）。

`TestCancellationIsNotATimeout` 初版会挂死 60 秒：handler 阻塞在 `<-r.Context().Done()`，而 `httptest.Server.Close` 等待在途 handler，服务端何时察觉连接关闭取决于传输时序。改为由测试显式释放 handler（`t.Cleanup` 是 LIFO，释放先于 Close）。

## 5. 参数白名单

10 个键，`seed` **不在其中**。`TestParameterAllowList` 六例覆盖：全部支持键、`seed` 被拒、拼错的 `temperatur`、类型错、小数 token 上限、缺 `model`。

小数 token 上限被拒而非截断，是有意的：`max_tokens: 100.5` 是个笔误，静默变成 100 会把它藏起来。

`seed` 被拒的理由写进了错误信息 —— aimodel v0.5.0 把 canonical 字段收窄为「≥2 个 provider 共有」，`seed` 没能留下。静默接受它等于承诺一个不存在的确定性。

## 6. 密钥不泄漏：三道防线

1. **`Auth` 类型装不下密钥** —— M1 的设计，只有 `type` 与 `env`；
2. **`redact` 扫描请求，发现疑似密钥即拒绝运行** —— 不是脱敏。悄悄抹掉会让用户以为密钥被安全处理了，而它仍写在磁盘上那个配置文件里；
3. **`transport.redactHeaders` 在存入缓冲前替换** `Authorization` 与 `X-Api-Key` —— 不是在写出前。一个进了缓冲的密钥已经离开了它该待的唯一地方。

另有一条容易忽略的：`*ais.APIError` 的**响应体原文不进 `error.message`**（`TestErrorMessageOmitsTheResponseBody` 用一个哨兵串验证）。错误响应体可能回显整个 prompt，而 `result.json` 不是它该去的地方；原文只进 `logs/`。

`make lint-secrets` 现在是 `lint` 的一部分，跑两个测试：扫描真实产出的结果目录，以及**验证扫描器确实会触发**。一个从未报过警的泄漏检测器和一个失效的检测器无法区分。

## 7. 偏离设计文档之处

### 7.1 `logs/` 尚未落盘

设计文档 §5 规划了「缓冲 → 样本 fail 时落盘」。`transport.Recorder` 已实现缓冲、脱敏与 `Take`/`Discard`，但**把它接到 `logs/` 目录的那一步没做**：接线需要 `llm_judge` 拿到结果目录的句柄，而 Grader 目前对结果目录一无所知 —— 这是有意的，`Grade` 只返回一个 `Evaluation`。

推到 M5：届时 `runner` 已经在管理并发窗口与记录写入，由它在样本落盘时顺带取走对应的 exchange 更自然。`Recorder` 的接口不需要改。

因此本阶段 `logs/` 恒为空，密钥扫描扫的是 `result.json` 与 `records.jsonl`。扫描代码本身遍历整个目录树，M5 接上后自动覆盖 `logs/`。

### 7.2 `grader.Factory` 加了一个 `Deps` 参数

设计文档没提这一层。`llm_judge` 需要 Judge 实例，而 `Factory` 原本只收 `GraderSpec`。三种改法里选了「加一个 `Deps` 结构参数」：

- 不能把 Judge 塞进 `GraderSpec` —— 那是协议类型（L1），不能装运行期对象；
- 不能给 `Grader` 接口加方法 —— L2，破坏性变更；
- `Deps` 是**结构体**而非额外参数，将来再需要什么可以加字段，不必改动每一个已写好的 factory。

### 7.3 `llm_judge` 接受 nil Judge

因为前置校验的**第 3 步**要构造 Grader 来问它 `Declare()`，而 Judge 配置是**第 4 步**才检查的。在第 3 步拒绝 nil Judge，会把一个 Judge 问题报成 Grader 声明问题 —— 步骤错了，退出路径也错了。

第一版就是这么写的，被 f06 的两个子用例抓到：

```
grader_declaration: grader "llm_judge" cannot be configured: judge: parameter "model" is required
```

定稿：`NewLLMJudge` 允许 nil Judge（文档写明只用于声明解析），`Grade` 对 nil 返回 `internal_error` 失败而非 panic。经过校验的运行永远走不到那里。

顺带发现 f06 的三个 llm_judge 子用例**缺 `rubric`** —— 一个只该在 judge 这一项失败的用例，其余每一项都必须合法。已补。

## 8. 验收标准覆盖

| # | 标准 | 覆盖 |
|---:|---|---|
| 11 | `success`/`fail` 二值，`fail` 带 code 且 `score=null`；被取消的样本记 `skipped` | `TestVerdictParsing` 11 例 + `TestScoreIsDiscardedOnFailure` + 取消三测（`skipped` 的落地在 M5） |
| 12 | 不产生任何达标判定，量表只透传 | `TestScoreIsPassedThroughUnjudged`：Judge 返回 7.5 而 `max_score` 是 1，仍原样记录且判 success |
| 13 | 计数恒等式成立 | f03 黄金文件：`evaluated`=3、`success`=2、`fail`=1、`score.count`=2；`EvalResult.Validate()` 落盘前无条件调用 |
| 18 | 结果含摘要、不含密钥 | §6 三道防线 + `make lint-secrets` |

## 9. 遗留到后续阶段

| 项 | 阶段 |
|---|---|
| `logs/` 落盘接线（`Recorder` 已就绪） | M5 |
| 取消样本记为 `skipped`（分类已就绪） | M5 |
| `http-json` / `stdio-jsonl` Judge 协议 | M6 |
| `anthropic-messages` 已可用但无测试覆盖（无凭据） | M7 |
| 高并发下的连接复用断言 | M5（`newHTTPClient` 的调优代码已就位，并发度目前恒为 1） |
