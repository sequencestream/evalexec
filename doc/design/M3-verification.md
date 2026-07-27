# M3 验证报告

对应 `doc/design/M3-execution.md` §9。`make build && make test && make lint` 全绿。

**dev-plan 的 v0.0.1 节点达成**：规则 Grader + 串行执行 + 根包 `Run`，不依赖任何模型服务。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| `f01`、`f02` 端到端通过 | ✅ | `TestGoldenCasesEndToEnd` 与黄金文件归一化比对 |
| 10 条数据单命令跑完且行数一致 | ✅ | 每个端到端测试都跑 `assertLineCountIdentity`（行数 + sequence 覆盖 1..n 各一次） |
| 恒等式自检有负向测试 | ✅ | `TestRunIsAtomicOnSummaryFailure`：Grader 返回违反不变量的 evaluation → 运行失败且**不发布目录** |

二进制实测：

```console
$ evalexec --task-id cs-demo --dataset sessions.jsonl --grader grader.json --output-dir out
$ echo $?
0
```

产出 `counts{total:3,completed:3,skipped:0}`、`success:2 fail:1`、`fail_by_code{insufficient_evidence:1}`、`score{count:2,mean:0.5}`，`checksums.sha256` 覆盖 `result.json` 与 `records.jsonl`。二次调用同目录返回 **4** 并拒绝覆盖。

## 2. 最重要的一处发现：前置校验把扩展点校验没了

`TestCustomGraderRegistration` 一跑就失败：

```
grader_declaration: unknown builtin grader entry "my_custom_grader";
known entries are contains, exact_match, json_schema, llm_judge, regex
```

M2 的 `validate` 第 3 步实现成了「`protocol: builtin` ⟹ entry 必须是硬编码表里的五个之一」。但 dev-plan §1.2 明确写着下游可以注册自己的 Grader 并用 `builtin` + 自定义 entry 跑 —— **前置校验会把注册表这个扩展点直接校验掉**。

这不是测试写错了，是 M2 的设计漏了一层。修法：

- `validate.Options` 新增 `GraderResolver`，第 3 步优先问注册表，问不到才回落到内置表；
- `grader.Registry` 实现该接口：**构造 Grader 并调用 `Declare()`**。构造发生在前置校验期是有意的 —— 一个因参数不合法而构造不出来的 Grader（正则编译不了、schema 非法），应该在第一个样本之前失败；
- `validate.ErrUnknownEntry` 区分「不是我的」与「是我的但坏了」。前者回落，后者立即报错 —— 否则一个坏掉的自定义 Grader 会因为回落而得到「未知 entry」这种误导性诊断。

这条已回写 `M3-execution.md` §2.2 未覆盖的部分（设计文档只说了注册表存在，没说校验要怎么找到它）。

## 3. 三个规则 Grader 的行为定稿

设计文档 §3 定了契约，实现时补齐的细节：

| 决定 | 理由 |
|---|---|
| `contains` 的字符串列表是**合取**（全部出现才算匹配） | 若当成析取，一个答对三分之一事实的回答会与完整回答同分 |
| 结构化 `output` 按**紧凑 JSON** 搜索 | Grader 不该猜「真正的文本」藏在结构的哪一层 |
| `regex` 的大小写不敏感用内联 `(?i)` 而非两边转小写 | 保住 pattern 自身的语义 —— 字符类与锚点都不受影响 |
| `pattern` / `schema` 在**构造期**校验 | `TestRegexRejectsABadPatternAtBuildTime` / `TestJSONSchemaRejectsABadSchemaAtBuildTime`。在第一个样本失败与在第一千个样本失败是同一个缺陷，提前说严格更好 |
| `exact_match` 用**语义** JSON 相等而非字节相等 | 键序与空白不该改变结论。`TestExactMatch/key order does not matter` |

`TestMismatchIsSuccessNotFailure` 单独成立并放在文件最前 —— 它是最容易写反的一条：**比出「两者不同」是成功的评估，记 0 分；只有「评不出来」才是 fail，且 fail 不带分数**。同一个测试里正反对照两种情形。

## 4. 参数校验的位置改了一次

初版把 `validateRegexParams` 放在 `builtin` 包，用 `declaration.SetValidateParams` 在 `init()` 里挂上去。写到一半发现这会造成 **init 顺序脆弱性**：`validate` 包的测试不 import `builtin`，于是拿不到校验器，前置校验会静默变弱。

一个「没被链接进来就悄悄失效」的前置校验比没有更糟，因为它看起来还在。改为 `declaration` 包自己拥有参数解析与校验（`CompilePattern` / `CompileSchema`），`builtin` 反过来调用它们 —— 顺带消除了两处重复的参数读取逻辑。

## 5. `LocalizedString(nil)` 会 panic

`jsonschema` 的 `ErrorKind.LocalizedString` 需要一个非 nil 的 `*message.Printer`，传 nil 直接 panic（被 `TestJSONSchema` 抓到）。

改用 `ValidationError.Error()`，它内部使用该库自带的默认 printer。接受一个 printer 会为了一个字符串把 `golang.org/x/text` 拉进直接依赖集 —— 而依赖预算（aimodel + 一个 JSON Schema 库）此刻正好用完。

## 6. M1 遗留的归一化豁免：原因与设计文档所写不同

`M3-execution.md` §8 计划「M2 定稿了 `redact.Canonical`，本阶段填入真实摘要并移除豁免」。实现时发现**真实原因根本不是序列化规则**：

`eval_request_sha256` 覆盖的是**规范化后**的请求，而规范化会把 `dataset.path` 与 `output_dir` 转成**绝对路径**。同一次评估在两台机器、两个目录下运行，会得到相同的结果和两个不同的请求摘要。**没有任何共享 fixture 能钉住这个值。**

因此豁免是**永久的**，不是临时的。这不构成缺口：对给定请求它仍然可复现（`TestDigestIsStableAcrossRuns` 与 `TestTwoRunsAreIndependent` 各验证一面），而可复现正是可追溯性需要的。已更正 `normalize.go` 的常量注释、对应测试名与 `fixtures/data/README.md`。

同理，端到端比对时 `request` 快照整块排除 —— 它嵌着临时目录的绝对路径。

## 7. 验收标准覆盖

| # | 标准 | 覆盖 |
|---:|---|---|
| 8 | 每次调用只产生一个 EvalResult | `TestTwoRunsAreIndependent`：两次调用两个目录两个 `eval_id` |
| 9（非停止路径） | `records.jsonl` 行数恒等于数据集行数 | `assertLineCountIdentity` 是所有端到端测试的公共断言；`evalexec.go` 内另有一道运行期自检（行数与前置校验统计的行数不符即判 runtime 故障） |
| 10 | `evaluation` 是单对象，仅 `skipped` 时为 null | 类型层面（M1）+ `Record.Validate()` 在**写盘前**逐条调用 |
| 12 | 不产生任何达标判定 | `min_score`/`max_score` 只在 `declaration` 的参数白名单里，无任何代码读取它们参与比较 |
| 13 | 计数恒等式成立 | `summary` 包 7 个测试 + 落盘前 `EvalResult.Validate()` 无条件调用 |
| 20 | 连续两次调用产生两个独立结果与 `eval_id` | `TestTwoRunsAreIndependent`，并断言 `dataset_sha256` 相同、`eval_request_sha256` 不同 |

## 8. 健壮性：Grader 是扩展点，会崩

| 场景 | 行为 | 测试 |
|---|---|---|
| Grader panic | recover → 该样本 `fail`/`internal_error`，**其余样本照常** | `TestGraderPanicBecomesOneFailedSample`（2 个样本都跑完，run 仍 `completed`） |
| Grader 超时 | `fail`/`timeout`，样本仍算 `completed` 不是 `skipped` | `TestGraderTimeoutIsAFailedEvaluation` |
| Grader 返回违反不变量的 evaluation | 整个 run 失败，**不发布目录** | `TestRunIsAtomicOnSummaryFailure` |

panic 恢复对**库形态**尤其必要：让 panic 逃逸会杀掉宿主进程，而这只是别人写的一个 Grader 出了 bug。

## 9. 偏离设计文档之处

### 9.1 `mean` 的浮点确定性

设计文档 §5 讨论了并发下按完成顺序累加导致 `mean` 末位可能抖动，定为「按完成顺序累加，README 注明不保证跨并发度逐位一致」。本阶段是串行的，顺序即输入顺序，问题尚未出现。M5 引入并发时需要在 README 落实这条说明。

### 9.2 `GraderID`/`GraderVersion` 取自配置

设计文档 §2.3 已论证：`Declaration` 描述的是内置 entry 的能力（`exact_match` 需要哪些字段），而 `id`/`version` 是调用方给这次评估起的名字（`order-status-exact` / `v1`），二者描述不同的东西。实现照此，`summary.Accumulator.Evaluation(graderID, graderVersion)` 显式接收它们。

## 10. 遗留到后续阶段

| 项 | 阶段 |
|---|---|
| `llm_judge` 尚未实现（`declaration` 里已有声明，注册表里没有实现） | M4 |
| `f03` 端到端 | M4 |
| `f04`（fail-fast）、`f05`（中断）端到端 | M5 |
| `runner` 的 `Outcome.Stopped` / `StopReason` 目前恒为零值 | M5 |
| `make lint-secrets` 仍是骨架 | M4 |
| README 的 `mean` 说明 | M5 |
