# M1 验证报告

对应 `doc/design/M1-protocol.md` §8。`make build && make test && make lint` 全绿。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| 所有 fixtures 能被 `evalspec` 解析并语义等价地序列化回去 | ✅ | `TestDatasetsRoundTrip` 覆盖 5 组 run 用例的每一行 |
| 归一化比对器有自己的单元测试 | ✅ | `evalspectest` 11 个测试，覆盖 §6 列出的四类用例 |
| `fixtures.FS` 可被外部测试包读到全部用例 | ✅ | `fixtures_test`（外部包）遍历 `FS`，断言 6 组齐备 |
| `Example_*` 全部通过 | ✅ | 5 个 Example |
| 覆盖验收标准 7、10（类型层面） | ✅ | 见 §5 |

## 2. 三态：本阶段最关键的一处，且第一次就写错了

设计文档 §2 预判「Go 的普通结构体字段无法表达缺失与 null 的差别」，但**第一版实现仍然写错了**，被测试当场抓住：

```
--- FAIL: TestSessionThreeState/key_present_with_null_value
    Has(output) = false, want true
```

原因值得记下来：我用了 `*json.RawMessage` 字段，以为「指针为 nil = 键缺失，指针非 nil = 键存在」。实际上 **`encoding/json` 遇到 JSON `null` 会把指针字段置为 `nil`** —— 与键缺失产生完全相同的结果。设计文档只说对了一半（`*T` 分不出「缺失」），没意识到它连 null 都保不住。

定稿实现改为把整行解到 `map[string]json.RawMessage`：map 的键存在性是唯一可靠的 presence 信号，`"output": null` 保留为键存在、值为四个字节 `null`。这条已回写 `M1-protocol.md` §2.1 与 `session.go` 的 `UnmarshalJSON` 注释。

覆盖该行为的测试：

- `TestSessionThreeState`：缺失 / null / 有值 / 空对象 / false 五种输入 × `Has`/`IsNull`/`Field`
- `TestSessionAbsentIsNotNull`：单独钉住「缺失 ≠ null」
- `TestSessionRoundTrip`：往返后缺失仍缺失、null 仍 null
- `TestSessionPreservesRawBytes`：`12345678901234567890` 与 `1.7976931348623157e308` 原样保留，证明未经 Go 类型往返
- `TestSessionFieldCopyIsDefensive`：调用方改返回值污染不了 Session
- f01 的 `case-003`（output 为 null，通过）与 f06 的 `session-missing-required-field`（output 缺失，退出码 2）在 fixtures 层面形成正反对照

## 3. 不变量的负向测试

`Validate()` 的每条不变量都被单独破坏一次：

| 不变量 | 负向用例 |
|---|---|
| `fail` ⟹ `score == nil` | `failed evaluation carrying a score`（记 0 分这个经典错误） |
| `fail` ⟹ `error != nil` | `failed evaluation without an error` |
| `fail` 的 code 不能是 `skipped` | `failed evaluation coded as skipped` |
| `success` ⟹ `error == nil` | `successful evaluation carrying an error` |
| `skipped` ⟹ `evaluation == nil` | `skipped record carrying an evaluation` |
| `completed` ⟹ `evaluation != nil` | `completed record without an evaluation` |
| `total = completed + skipped` | `total != completed + skipped` |
| `evaluated = completed` | `evaluated != completed` |
| `evaluated = success + fail` | `evaluated != success + fail` |
| `fail = sum(fail_by_code)` | `fail != sum(fail_by_code)` |
| `score.count ≤ success` | `score.count > success` |
| `completed` ⟹ `skipped == 0` | `completed with skipped samples` |
| `cancelled` ⟹ `skipped > 0` 且有 `stop_reason` | 两个用例 |
| `score.count == 0` ⟺ 统计量全 null | 5 个用例（含 min>max、mean 越界） |

`TestValidateReportsEveryProblem` 断言一次报全部问题而非首个失败。

## 4. `omitempty` 陷阱

规范中 `mean`/`min`/`max`/`score`/`label`/`started_at` 等在特定情形下的值是 **null**，即键必须存在且值为 null。`omitempty` 对 `*float64` 的 nil 会把整个键删掉，语义完全不同。

定稿规则：**凡是规范中出现过 `null` 作为合法值的字段，一律不加 `omitempty`**。

由两个测试钉住：`TestScoreStatsMarshalsNullsNotOmissions`（断言 `{"count":0,"mean":null,"min":null,"max":null}`）、`TestEvaluationMarshalsNullScore`。同一个测试反向断言 `judge_reasoning_tokens` 这类**新增可选字段**在未使用时必须消失 —— 否则规则 Grader 的记录里会凭空多出两个 0。

## 5. 验收标准覆盖

| # | 标准 | 本阶段覆盖方式 |
|---:|---|---|
| 7 | 所有有效输入可规范化为一个 EvalRequest | `EvalRequest` 类型 + `Validate()`；`TestRequestsParseAndValidate` 对 5 个 fixture 请求逐个校验 |
| 10 | `evaluation` 是单对象，仅 `skipped` 时为 null | 类型上 `*Evaluation` 而非 slice；`Record.Validate()` 双向强制；`TestExpectedRecordsAreValid` 对全部黄金记录跑一遍 |

## 6. 两处偏离设计文档的定稿

### 6.1 f05 改为不变量断言，不写黄金文件

设计文档原计划 6 组用例全部 `expected/result.json`。实现时发现 **f05（中断）的停止点取决于调度，不可预测**。写黄金文件等于先编造一个停止点、再测试实现能否复现这个编造。

改为 `expected/invariants.json`，断言可预测的部分：退出码 130、行数恒为 8、sequence 1..8 各一次、至少一条 `skipped`、**无任何 `fail` 记录**。最后一条是 dev-plan §7 点名的头号易错项（取消被误判为 timeout/fail）。

`fixtures.GoldenCases()`（f01–f04）与 `fixtures.RunCases()`（含 f05）因此分开导出。

### 6.2 `eval_request_sha256` 暂时纳入归一化

设计文档 §6 写明「两个 sha256 都不归一化 —— 它们必须可复现」。实现时发现二者性质不同：

- `dataset_sha256` 对**原始文件字节**计算，规范完全确定，任何实现必须算出同一值 → **保持不归一化**，`TestNormalizeKeepsDatasetChecksum` 钉住；
- `eval_request_sha256` 对**脱敏后规范化 JSON** 计算，而"规范化"（key 排序、紧凑序列化）的具体规则要到 M3 才定。此刻手写一个值，等于用实现证明实现 → **暂时归一化**，M3 定稿序列化器后移除。

该豁免在 `normalize.go` 的常量注释、`TestNormalizeErasesRequestChecksumForNow` 的注释与 `fixtures/README.md` 三处都写明了到期条件。

## 7. fixtures 概况

| 用例 | 形态 | 行数 | 钉住的性质 |
|---|---|---:|---|
| f01 | golden | 3 | happy path；`output: null` 与 `expected_output: null` 相等 |
| f02 | golden | 4 | **0 分是 success，失败才 null**；`score.count`(3) < `evaluated`(4) |
| f03 | golden | 3 | 失败样本照常记 usage（2360 = 850+870+640） |
| f04 | golden | 5 | 补写保持行数；退出码 0 |
| f05 | invariants | 8 | 中断产生 skipped 而非 fail |
| f06 | exit codes | 10 子用例 | 含检查顺序规则（目录冲突 4 压过数据集错误 2） |

三个交叉校验测试保证手写的黄金文件不会互相漂移：

- `TestExpectedRecordsAreValid`：每条黄金记录跑真正的 `Record.Validate()`
- `TestExpectedResultsAreValid`：每个黄金结果跑真正的 `EvalResult.Validate()`
- `TestGoldenResultAgreesWithRecords`：从 records 重算 counts / success / fail / fail_by_code / score 统计 / usage 总量，与 result.json 逐项比对

第三个测试在编写 f02 时立刻发挥了作用 —— 它强制我把 `score.mean` 写成 `0.6666666666666666` 而不是随手写的 `0.67`。

## 8. 密钥扫描：修掉一个假阳性，并给扫描器加了自测

`TestNoFixtureContainsACredential` 初版用子串匹配 `"sk-"`，立刻命中 `args.json` 里的 `"--task-id"`（`ta`**`sk-i`**`d`）。假阳性的代价不是这一次误报，而是扫描器会被关掉。

改为匹配**密钥形态**的正则（`\b(sk|pk|api)-[A-Za-z0-9_-]{16,}` 等三条），并新增 `TestCredentialScannerCatchesARealKey`：用三个真实形态的泄漏串验证扫描器确实会触发，同时验证它不会在 `bearer_env` 引用与 argv 数组上误报。一个从未报过警的泄漏检测器，和一个失效的检测器无法区分。

## 9. 顺带修掉的一个 M0 缺陷

`make lint` 原本写成：

```make
command -v golangci-lint && golangci-lint run || echo "not installed, skipping"
```

golangci-lint **运行并报错**时，`||` 分支同样触发，打印「未安装」并让整个 target 返回 0 —— lint 失败被静默吞掉。M1 期间它确实吞了 3 条真实告警。已改为 `if command -v ...; then ...; else ...; fi`，把「不存在」与「失败」分开判定。

## 10. 遗留到后续阶段

| 项 | 阶段 |
|---|---|
| `eval_request_sha256` 的归一化豁免 | M3（定稿规范化序列化器后移除） |
| f06 各子用例的实际执行 | M2（本阶段只断言 fixture 数据自身形状） |
| f01–f04 的端到端比对 | M3 / M4 |
| f05 的中断实测 | M5 |
| `judge-responses.jsonl` 的回放 | M4 |
