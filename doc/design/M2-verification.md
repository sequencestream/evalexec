# M2 验证报告

对应 `doc/design/M2-precheck.md` §8。`make build && make test && make lint` 全绿。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| f06 全部子用例通过 | ✅ | 10/10，`TestPrecheckFixtures` 跑真实二进制路径 |
| 校验失败时不产生任何结果目录（含临时目录） | ✅ | 每个子用例跑完调用 `assertNoResultDirectory`；另有 `TestNoDirectoryIsCreated` |
| 覆盖 dev-plan 列出的 8 种失败场景 | ✅ | 实际做了 10 种，见 §2 |

```console
--- PASS: TestPrecheckFixtures/duplicate-case-id
--- PASS: TestPrecheckFixtures/duplicate-grader-flag
--- PASS: TestPrecheckFixtures/empty-case-id
--- PASS: TestPrecheckFixtures/grader-missing-requires
--- PASS: TestPrecheckFixtures/judge-auth-env-unset
--- PASS: TestPrecheckFixtures/judge-missing-endpoint
--- PASS: TestPrecheckFixtures/malformed-jsonl
--- PASS: TestPrecheckFixtures/output-dir-not-empty
--- PASS: TestPrecheckFixtures/requires-judge-without-model
--- PASS: TestPrecheckFixtures/session-missing-required-field
```

## 2. 检查顺序：本阶段唯一「实现得很合理但就是错」的地方

`output-dir-not-empty` 子用例的数据集**故意也是非法的**，另有 `TestCheckOrderDirectoryConflictWins` 构造**三重错误**（目录冲突 + 数据集非法 + Grader 声明不符），断言退出码为 **4** 而非 2。

自然的实现顺序是「先校验输入、再碰输出目录」—— 它能通过其余 9 个子用例，唯独这一个错。所以顺序测试单独成立，失败时直接指名规则，而不是混在 fixture 扫描里。

## 3. 三个真实缺陷，都是测试先抓到的

### 3.1 `evalerr.Error.Error()` 吞掉被包装的原因

初版实现只在 `Message == ""` 时才附加 `Err`。结果 `Wrap(..., err, "dataset is not valid JSONL")` 打印出的是「数据集不合法」，**却不说是哪一行** —— 而行号正是 `dataset.ParseError` 精心携带的信息。

四个 f06 子用例同时失败暴露了它。改为 `step: message: cause` 三段拼接，任一段缺失即省略。

### 3.2 `report()` 的「flag 已打印过」启发式吞掉了校验诊断

初版用「`Message == "" && Err != nil`」判断「flag 包已经打印过了，不要重复打印」。但这个形状同样匹配 `Wrap(KindArgument, StepArguments, req.Validate(), "")` —— 于是 `grader-missing-requires` 与 `requires-judge-without-model` 两个子用例**退出码正确但 stderr 全空**，用户得不到任何解释。

改为 `evalerr.Error.Reported bool` 显式标记，只有 flag 解析路径设它。猜测换成声明。

### 3.3 `make lint` 的边界守卫误判注释

`lint-boundary` 命中了 `cli/cli.go` 里**解释这条规则的文档注释**（"It is never os.Stderr here: ..."）。grep 分不出代码与注释。

这个假阳性的代价不是一次误报，而是**它会逼人不写这条注释** —— 恰好反了。两处修改：

1. 权威检查改为 golangci-lint 的 **`forbidigo`**，工作在 AST 上，不会看见注释。配置里对三个符号各给一句为什么禁；`cmd/` 与 `_test.go` 豁免。
2. Makefile 的 grep 降级为「无需 golangci-lint 时的快速预检」，先 `sed 's|//.*||'` 剥离注释。其局限（字符串里的 `//` 会截断该行）写在 Makefile 注释里，并说明由 forbidigo 兜底。

## 4. 验收标准覆盖

| # | 标准 | 覆盖 |
|---:|---|---|
| 1 | 无子命令即可完成一次运行 | `TestPositionalArgumentsRejected`（位置参数被明确拒绝并说明无子命令） |
| 2 | 每次只能指定一个 Grader | `TestGraderFlagRejectsRepetition` |
| 3 | 两个 `--grader` 在调用前失败 | 同上 + f06 `duplicate-grader-flag`（退出码 2，无目录产生） |
| 4 | `task_id` 只校验非空并原样输出 | `EvalRequest.Validate()` 只查非空；`TestFlagsOverrideRequestFile` 验证原样透传 |
| 5 | `eval_id` 非空且全局唯一 | `TestGeneratedEvalIDIsUUIDv7` 校验版本位与变体位 |
| 6 | 缺省时自动生成 | 同上；`TestSuppliedEvalIDIsKept` 验证提供时不改 |
| 7 | 所有有效输入可规范化为一个 EvalRequest | `TestNormalizationDefaults`、`TestFlagsOverrideRequestFile`、`TestPathsResolveAgainstRequestFile` |
| 14 | 必须声明 `requires` / `requires_judge` | `TestStepFailures` 三例 + `TestLLMJudgeDynamicRequires` 七例 |
| 15 | 非法数据集 / 重复 ID / 缺 Judge 在调用前失败 | `TestStepFailures` + f06 六个子用例 |
| 18 | 结果不含密钥（参数侧） | `TestSecretFlagsRejected`、`TestCredentialInConfigurationIsRefused` |
| 19 | 输出目录不被静默覆盖 | `TestStepFailures/output directory not empty`；无 `--force` |
| 20（一半） | 连续两次调用产生两个独立 `eval_id` | `TestGeneratedEvalIDIsUUIDv7` |

## 5. 本阶段定稿的几个设计开放点

| dev-plan 开放点 | 定稿 | 落点 |
|---|---|---|
| #3 `llm_judge` 的动态 `requires` 规则未定义 | 显式参数 `use_reference` / `use_trajectory` 推导 | `declaration.EffectiveRequires`，7 个用例覆盖四种组合 + 声明不符 + 非布尔值 |
| #4 `--grader-param` 能否改变 `requires` 推导 | 能。覆盖在第 1 步后、第 3 步前应用 | `applyParamOverrides` 在 `buildRequest` 末尾；`TestFlagsOverrideRequestFile` |
| #6 `eval_id` 唯一性由谁保证 | 调用方提供时不校验；缺省生成 UUIDv7 | `cli/id.go`，自实现约 40 行，不引入依赖 |
| #9 空数据集是否合法 | 合法，`counts.total=0` | `TestEmptyDatasetIsLegal` |
| #10 覆盖冲突时的日志 | 覆盖发生时向 stderr 输出一行 | `overrideString`；`TestFlagsOverrideRequestFile` 断言提示存在 |

另外定稿了两条 dev-plan 未提的：

- **相对路径相对谁**：相对 `--request` 文件所在目录，无 `--request` 时相对 CWD。理由是请求文件通常与数据集一起签入仓库，若相对 CWD 解析，同一个文件从不同目录调用会指向不同数据。`TestPathsResolveAgainstRequestFile` 钉住。
- **单值 flag 一律拒绝重复**，不只是 `--grader`。重复给同一个单值参数总是写错了，静默取最后一个只会掩盖。

## 6. 偏离设计文档之处

### 6.1 `redact` 发现疑似密钥时**拒绝运行**，而非脱敏

设计文档 §6.1 已定此策，实现时保持。理由值得复述：悄悄抹掉会让用户以为密钥被安全处理了，而实际上它仍写在磁盘上的配置文件里 —— 泄漏已经发生，抹掉的只是证据。

`TestCredentialInConfigurationIsRefused` 覆盖 OpenAI 风格 key、Bearer token、GitHub token、AWS key 四种形态；`TestScannerDoesNotFlagOrdinaryConfiguration` 反向验证不会在 `bearer_env` 引用、argv 数组、中文 rubric 上误报。

### 6.2 `eval_request_sha256` 的规范化序列化器已定稿

M1 遗留的归一化豁免，本阶段解决：`redact.Canonical` 定为「递归 key 排序 + 紧凑 + `SetEscapeHTML(false)`」。

关闭 HTML 转义不是可选项：`encoding/json` 默认把 `<`、`>`、`&` 改写成 `<` 形式，那既会让摘要依赖 rubric 里恰好出现的字符，又会在读回时改变 rubric 的文本。`TestCanonicalDoesNotEscapeHTML` 钉住。

**但 fixtures 的归一化豁免暂未移除** —— 移除它需要真正跑出 `result.json`，那是 M3 的事。M3 收尾时删除 `PlaceholderEvalRequestSHA256` 并填入真实值。

### 6.3 第 4 步的 aimodel 客户端构造推迟到 M4

设计文档 §4.2 已定：本阶段第 4 步只做结构检查（协议合法、endpoint 必填、`auth.env` 非空），并留 `JudgeChecker` 注入点。`TestJudgeCheckerIsConsulted` 用 stub 验证注入点确实被调用，M4 只需把 `judge.New` 包进去。

## 7. 新增的两个包（dev-plan 未列）

| 包 | 为什么不能放在别处 |
|---|---|
| `evalerr` | 所有包都要**打**错误标签，只有 `exitcode` **读**。若 `Kind` 定义在 `exitcode` 里，`validate`/`cli`/`dataset`/`result` 全都要 import 退出码包 —— 而它本该在依赖链末端 |
| `grader/declaration` | 前置校验必须在**构造任何 Grader 之前**知道它的 `requires`。若表放在 `grader` 包里，`validate` 就要 import 它所要校验的实现 |

## 8. 遗留到后续阶段

| 项 | 阶段 |
|---|---|
| fixtures 的 `eval_request_sha256` 归一化豁免 | M3（届时能跑出真实结果） |
| 第 4 步构造真实 aimodel client | M4（`JudgeChecker` 注入点已就位） |
| `result` 包只有目录生命周期，尚无内容写入 | M3 |
| 数据集第二遍扫描（执行阶段） | M3 |
| `CaseIndex` 的磁盘实现 | M7 之后的可选优化，接口已留 |
