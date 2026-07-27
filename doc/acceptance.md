# 验收标准覆盖表

`04-mvp-scope.md` §4 的 21 条验收标准，逐条映射到**具体测试函数**。

指向测试函数而不是指向阶段（"由 M3 覆盖"）是有意的：后者在测试被重命名或删掉之后不会失效，等于没有约束力。这张表在 `make test` 之后应当能逐条查证。

写这张表时发现了两处缺口（标准 1 与标准 4 只有间接覆盖），已补测试。有一条标准**部分不适用**，见 §标准 5 的说明 —— 表里如实写明，而不是让它看起来是满格的。

---

| # | 标准 | 覆盖的测试 |
|---:|---|---|
| 1 | `evalexec [flags]` 无子命令即可完成一次运行 | `TestSingleInvocationCompletesARun`、`TestPositionalArgumentsRejected`、`TestValidRunReachesExecution` |
| 2 | 每次必须且只能指定一个 Grader | `TestGraderFlagRejectsRepetition`、`TestSingleValueFlagsRejectRepetition` |
| 3 | 传入两个 `--grader` 时在 Grader 或 Judge 调用前失败 | `TestPrecheckFixtures/duplicate-grader-flag`（退出码 2 且断言无结果目录） |
| 4 | `task_id` 只校验非空并原样出现在 EvalResult | `TestTaskIDIsEchoedVerbatim`（6 种不透明取值，含含空格、含 JSON、含路径穿越形状）、`TestEmptyTaskIDIsRejected` |
| 5 | EvalResult 必须包含非空且**全局唯一**的 `eval_id` | 非空：`TestGeneratedEvalIDIsUUIDv7`、`TestSingleInvocationCompletesARun`。唯一性见下方说明 |
| 6 | 未提供 `eval_id` 时自动生成，并写入所有结果记录 | `TestGeneratedEvalIDIsUUIDv7`、`TestSuppliedEvalIDIsKept`、`TestInjectedIDGenerator`；写入所有记录由 `TestCheckEvalIDConsistency` + 每个端到端测试调用它 |
| 7 | 所有有效输入可规范化为一个 EvalRequest | `TestNormalizationDefaults`、`TestFlagsOverrideRequestFile`、`TestPathsResolveAgainstRequestFile`、`TestParamOverrideScalarParsing`、`TestRequestsParseAndValidate` |
| 8 | 每次调用只产生一个 EvalResult | `TestTwoRunsAreIndependent`、`TestOutputDirectoryIsNotOverwritten` |
| 9 | 每个样本恰有一条明细并携带相同 `eval_id`；行数恒等于数据集行数 | `TestLineCountIdentityAtEveryConcurrency`（并发 1/2/4/16）、`TestFailFastCaseEndToEnd`、`TestInterruptPublishesACompleteResult`、`TestStressAtHighConcurrency`、`TestCancellationProducesSkippedNotFailed`、`TestFailFastStopsDispatch`；`eval_id` 一致性由 `TestCheckEvalIDConsistency` |
| 10 | 明细的 `evaluation` 是单对象，仅 `skipped` 时为 `null`，任何情况下不是数组 | 类型层面：`Record.Evaluation` 是 `*Evaluation` 而非 slice。行为：`TestRecordValidateRejectsBrokenInvariants`（9 个负向用例）、`TestNewSkippedRecordShape`、`TestExpectedRecordsAreValid` |
| 11 | `evaluation.status` 只取 `success`/`fail`；`fail` 带 `error.code` 且 `score` 为 `null`；被取消的样本记 `skipped` | 二值：`TestEnumIsValid/EvaluationStatus`。`fail` 无分数：`TestNewFailEvaluationHasNoScore`、`TestFailureIsNeverAZero`、`TestScoreIsDiscardedOnFailure`。取消记 `skipped`：`TestCancellationProducesSkippedNotFailed`、`TestCancellationIsNotATimeout`、`TestClassifyDistinguishesCancellationFromDeadline`、`TestCancellationPropagates`、`TestInterruptPublishesACompleteResult` |
| 12 | 不产生任何达标判定；`min_score`/`max_score` 只透传不参与计算 | `TestScoreIsPassedThroughUnjudged`（Judge 返回 7.5 而 `max_score` 是 1，仍原样记录且判 success）、`TestMismatchIsSuccessNotFailure` |
| 13 | 汇总满足计数恒等式，均值分母为 `score.count` | `TestEvalResultCountIdentities`（7 个负向）、`TestIdentitiesHold`、`TestFailuresContributeNoScore`、`TestScorelessSuccessCountsAsSuccess`、`TestScoreRange`、`TestGoldenResultAgreesWithRecords`；落盘前无条件调用 `EvalResult.Validate()` |
| 14 | Grader 配置必须声明 `requires` 与 `requires_judge`，缺失或非法时在首次评估调用前失败 | `TestStepFailures`（3 例）、`TestEvalRequestValidate`（3 例）、`TestLLMJudgeDynamicRequires`（7 例）、`TestUndeclaredParamsAreNotPoliced` |
| 15 | 无效 JSONL、重复 case ID、缺 `requires` 字段的 Session、`requires_judge=true` 却缺 Judge，均在首次评估调用前失败 | `TestStepFailures`（4 例）、`TestPrecheckFixtures` 的 `malformed-jsonl` / `duplicate-case-id` / `empty-case-id` / `session-missing-required-field` / `requires-judge-without-model` / `judge-auth-env-unset` / `judge-missing-endpoint` |
| 16 | 顶层 `status` 只取三值；`completed` 时 `skipped=0`，`cancelled` 时 `skipped>0` 且 `stop_reason` 非空 | `TestEvalResultStatusBinding`（6 例）、`TestStatusBinding`、`TestFailFastCaseEndToEnd`、`TestInterruptPublishesACompleteResult` |
| 17 | fail-fast 停止后仍写出可信 EvalResult 并返回退出码 `0`；中断返回 `130` 并尽量发布 | `TestFailFastExitsZero`、`TestFromResultFailFastIsSuccess`、`TestFailFastCaseEndToEnd`、`TestInterruptPublishesACompleteResult`、`TestSecondInterruptIsIgnored`、`TestCancelledRunStillPublishes` |
| 18 | 结果包含数据集、请求和实现摘要，但不包含密钥 | 摘要：`TestDigestIsStableAcrossRuns`、`TestTwoRunsAreIndependent`、`TestLdflagsStampReachesBinary`。无密钥：`TestNoSecretReachesTheResultDirectory`、`TestLogsRedactTheCredential`、`TestCredentialInConfigurationIsRefused`、`TestErrorMessageOmitsTheResponseBody`、`TestSecretFlagsAreRejected`、`TestNoFixtureContainsACredential`，以及 e2e 的 `assertNoCredentialInOutput`（真实密钥） |
| 19 | 输出目录已存在时不会被静默覆盖 | `TestOutputDirectoryIsNotOverwritten`、`TestStepFailures/output directory not empty`、`TestPrecheckFixtures/output-dir-not-empty`；无 `--force` |
| 20 | 外部连续调用两次会产生两个完全独立的结果目录和 `eval_id` | `TestTwoRunsAreIndependent`（并断言 `dataset_sha256` 相同、`eval_request_sha256` 不同） |
| 21 | Python 与 Go 实现可以通过同一组协议 fixtures | 被评组件侧：`TestBuiltinAndExternalGradersAgree`（内置 / http-json / stdio-jsonl Go / **stdio-jsonl Python** 四路一致）。结果消费侧：`contract/verify_fixtures.py`（纯标准库，自带 `--self-test` 检出 17 种违规） |

---

## 标准 5 的唯一性：一半不适用，如实记录

标准 5 要求 `eval_id` **全局唯一**。实现上分两种情况：

- **缺省生成**：UUIDv7，`TestGeneratedEvalIDIsUUIDv7` 断言格式（版本位 7、变体位 8–b）并断言两次调用产生不同值。这一半是可测的。
- **调用方提供**：**不校验唯一性**。这是 dev-plan 开放点 #6 的定稿 —— 标识符是调用方给的不透明字符串，EvalExec 没有全局视野可以验证唯一性，假装验证过反而危险。

所以这条标准的「全局唯一」对提供路径是**调用方的责任**，不是被测的实现属性。`TestSuppliedEvalIDIsKept` 明确断言提供值原样保留、不加格式限制。

## 标准 9 与 21 的加强

两条标准的实测强度超出了字面要求：

- **标准 9** 要求行数恒等。实测覆盖 4 个并发度、fail-fast、真实进程中断（含二次中断）、1000 样本压力测试，共 8 处独立断言；`evalexec.go` 内另有一道运行期自检（记录数与前置校验统计的行数不符即判运行级故障）。
- **标准 21** 要求 Python 与 Go 通过同一组 fixtures。实测有两个方向：一个 Python **被评组件**（Grader）与一个 Python **结果消费者**（校验器）。后者是 dev-plan 明确要求的那个；前者是额外的，因为「协议不绑定语言」在两个方向上都该成立。

## 统计

`make test` 运行 **200 个** `Test*` / `Example*` 函数（其中许多是表驱动，子用例更多）。

三个测试是**对测试本身的测试** —— 一个从未报过警的检查器与一个失效的检查器无法区分：

| 测试 | 验证什么 |
|---|---|
| `TestLeakScannerActuallyFires` | 密钥扫描器确实会命中植入的凭据 |
| `TestCredentialScannerCatchesARealKey` | fixtures 的凭据形态正则确实会触发，且不在合法配置上误报 |
| `verify_fixtures.py --self-test` | 协议校验器拒绝 17 种违规文档，并接受一份合法文档 |

另有两处守卫做过**注入式负向验证**（M0 验证报告 §3）：`lint-terms` 与 `lint-boundary` 各注入一次违规并确认失败。

## 不在 21 条之内、但值得记录的实测

| 性质 | 测试 |
|---|---|
| 「不匹配是成功的评估记 0 分」 | `TestMismatchIsSuccessNotFailure`（正反对照） |
| Grader panic 只损失一个样本 | `TestGraderPanicBecomesOneFailedSample` |
| 高并发下连接被复用 | `TestConnectionsAreReusedUnderConcurrency`（60 请求 / 并发 8 → 峰值 8 条） |
| 子进程 stderr 刷屏不死锁 | `TestStdioGraderSurvivesChattyStderr` |
| 外部实现返回违反不变量的结果被拒 | `TestExternalGraderFailureModes/failure carrying a score` |
| 库调用方省略 `eval_id` 仍得到合法结果 | `examples/consumer`（这个缺陷正是它发现的） |
| 真实模型能被要求按约定形状作答 | `e2e/TestLLMJudgeAgainstALiveModel` |
