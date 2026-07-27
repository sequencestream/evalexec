# M1 协议层与 fixtures — 阶段设计

对应 `doc/dev-plan.md` §4 M1（设计稿 S1）。目标：把 `02-core-spec.md` 变成可编译、可往返的 Go 类型，并产出跨语言共享的 fixtures。

`evalspec` 是 **L1**：与 `spec_version` 同生命周期，下游会直接构造其中的结构体。因此本阶段的每个取舍都要按「这个名字/形状要活到 v1.0」的标准来定。

---

## 1. 本阶段边界

**做**：`evalspec` 全部协议类型、三层状态枚举、受控构造函数与 `Validate()`、归一化比对器、`fixtures` 包与 6 组用例数据。

**不做**：读数据集（M2/M3 的 `dataset` 包）、任何校验编排（M2 的 `validate`）、任何执行。M1 的类型是**纯数据 + 不变量**，不含 I/O。

fixtures 的 `expected/` 在 M1 是**手写的目标形状**，M3–M5 的端到端测试再来消费它们。手写而非「跑一遍生成」是有意的：生成出来的期望值只能证明实现自洽，证明不了它符合规范。

---

## 2. 最难的一处：Session 字段的三态

`02-core-spec.md` §3 与 dev-plan 都点名这是「最容易被简化掉的细节」：

> 声明为必需的字段必须在每条 Session 中出现（**键存在即可**，`output` 的值允许为 `null` 表示 Agent 未产生最终输出）

于是 `requires: ["output"]` 下，三种输入的判定截然不同：

| 数据行 | `output` 状态 | `requires` 校验 |
|---|---|---|
| `{"case_id":"c1","input":{}}` | 键缺失 | **失败**（退出码 2） |
| `{"case_id":"c1","input":{},"output":null}` | 键存在、值为 null | **通过** |
| `{"case_id":"c1","input":{},"output":{...}}` | 键存在、有值 | 通过 |

Go 的普通结构体字段无法表达前两者的差别，而且比看上去更糟：

- 值类型字段：「键缺失」与 `"output":null` 都留下零值；
- **指针字段 `*json.RawMessage`：同样不行**。`encoding/json` 遇到 JSON `null` 会把指针**置为 nil**，与键缺失的结果完全相同。

第二条是实现时被测试当场抓住的（见 `M1-verification.md` §2）。因此定稿方案不能用结构体。

### 2.1 定稿：显式 presence + 原始字节

```go
// SessionField 是 requires 的合法元素，也是 Session 的字段键。
type SessionField string

const (
    FieldInput      SessionField = "input"
    FieldOutput     SessionField = "output"
    FieldTrajectory SessionField = "trajectory"
    FieldReference  SessionField = "reference"
    FieldContext    SessionField = "context"
    FieldCriteria   SessionField = "criteria"
    FieldMetadata   SessionField = "metadata"
)

type Session struct {
    CaseID string
    fields map[SessionField]json.RawMessage // 只含**出现过**的键
}

func (s *Session) Has(f SessionField) bool          // 键是否出现
func (s *Session) IsNull(f SessionField) bool       // 出现且值为 JSON null
func (s *Session) Field(f SessionField) json.RawMessage // 原始字节，未出现时返回 nil
```

三个理由：

解码时把**整行解到 `map[string]json.RawMessage`**：map 的键存在性是唯一可靠的 presence 信号，`"output": null` 保留为「键存在、值为四个字节 `null`」。三个理由：

1. **`Has` 与 `IsNull` 分离**，`requires` 校验直接就是 `s.Has(f)`，语义与规范逐字对应，不需要注释解释；
2. **值存 `json.RawMessage`**：规范要求「Grader 不得改变 Session 原始内容」，保留原始字节是最强的保证 —— 不经历一次 Go 类型的往返，就不存在数字精度、key 顺序、未知字段被吃掉的问题；
3. `map` 只装出现过的键，`len(fields)` 天然等于实际字段数。

代价：字段值是 `json.RawMessage` 而非具体类型，Grader 要自己解。这是对的 —— `input`/`output` 的内部结构由上游框架决定，EvalExec 声明过「不理解其内部框架」。

### 2.2 `SessionField` 的合法性

`requires` 的元素必须是上述七个之一（`02-core-spec.md` §2）。`case_id` **不是** `SessionField`，规范明确「`case_id` 始终必填，无需出现在 `requires` 中」，因此不进枚举 —— 否则 `requires: ["case_id"]` 会变成一个合法但无意义的写法。

`IsValid()` + `AllSessionFields()` 一起导出，后者让下游能枚举而不必抄常量表。

---

## 3. 三层状态：三个独立类型

dev-plan §7 把「三层状态被混用」列为头号风险。定稿为三个**不同的 Go 具名类型**，编译期就不可互相赋值：

| 层 | 类型 | 取值 | 出处 |
|---|---|---|---|
| 运行级 | `RunStatus` | `completed` / `cancelled` / `failed` | `EvalResult.status` |
| 样本级 | `RecordStatus` | `completed` / `skipped` | `Record.status` |
| 评估级 | `EvaluationStatus` | `success` / `fail` | `Evaluation.status` |

注意 `completed` 在运行级与样本级**都存在但含义不同**（前者是「全部派发完毕」，后者是「本样本交给过 Grader」）。如果用裸 `string`，这两个值会在代码里自由流动而编译器毫无意见 —— 这正是要用独立类型的原因。

另有两个枚举：

- `StopReason`：`fail_fast` / `interrupt` / `error`；
- `ErrorCode`：`insufficient_evidence` / `judge_error` / `timeout` / `protocol_error` / `internal_error`，外加补写记录专用的 `skipped`。

`skipped` 作为 `ErrorCode` 值得说明：`02-core-spec.md` §5 的补写记录形状是 `"error": {"code": "skipped", "reason": "fail_fast"}` —— 它出现在 `Record.error` 而非 `Evaluation.error`，且带的是 `reason` 而不是 `message`。因此**顶层 `Record.error` 与 `Evaluation.error` 是两个不同的结构**：

```go
// Evaluation.error：评估失败的原因
type EvalError struct {
    Code    ErrorCode `json:"code"`
    Message string    `json:"message,omitempty"`
}

// Record.error：样本未被执行的原因（仅 skipped 记录）
type RecordError struct {
    Code   ErrorCode  `json:"code"`   // 恒为 "skipped"
    Reason StopReason `json:"reason"` // fail_fast | interrupt
}
```

把它们合并成一个带四个可选字段的结构会让「哪些字段该填」变成运行期约定，而分开后由类型直接表达。

---

## 4. 不变量与受控构造函数

`evalspec` 是 L1，「只有我们的代码会构造它」这个假设不成立（dev-plan §1.5）。所有不变量都要有构造函数保证 + `Validate()` 兜底。

### 4.1 三条硬不变量

| 不变量 | 出处 | 保证方式 |
|---|---|---|
| `evaluation.status = fail` ⟹ `score == nil` 且 `error != nil` | `02` §5 表格 | `NewFailEvaluation` 不接受 score 参数 |
| `record.status = skipped` ⟹ `evaluation == nil`、时间字段为 null、`error != nil` | `02` §5 | `NewSkippedRecord(caseID, seq, reason)` |
| `record.status = completed` ⟹ `evaluation != nil`、`record.error == nil` | `02` §5 | `NewCompletedRecord(..., eval)` |

`NewFailEvaluation` **不接受 score 参数**是关键设计：dev-plan M4 要求「即使 Judge 返回了分数也丢弃」。如果签名里有 score 参数再在内部置 nil，调用方会困惑；干脆让类型系统表明 fail 没有分数可谈。

```go
func NewSuccessEvaluation(score *float64, label, reason string, ev []Evidence, u Usage, latencyMS int64) Evaluation
func NewFailEvaluation(code ErrorCode, message, reason string, ev []Evidence, u Usage, latencyMS int64) Evaluation
```

`NewSuccessEvaluation` 的 `score` 仍是 `*float64`：`success` 但只给 `label` 不给分数是合法的（`02` §6.2「`score.count` 可能小于 `success`」）。

### 4.2 计数恒等式

```text
counts.total     = counts.completed + counts.skipped
evaluation.evaluated = counts.completed
evaluation.evaluated = evaluation.success + evaluation.fail
evaluation.fail      = sum(evaluation.fail_by_code.*)
evaluation.score.count ≤ evaluation.success
status=completed ⟹ skipped == 0
status=cancelled ⟹ skipped > 0 且 stop_reason != null
```

全部落在 `EvalResult.Validate()`。M3 的 `summary` 包在写盘前无条件调用它（dev-plan §7：「`result` 包在落盘前无条件调用 `EvalResult.Validate()`，不信任调用方」）。

`Validate()` 返回**结构化错误**而非字符串，让 M2 的 `exitcode` 能按 `Kind` 映射：

```go
type ValidationError struct {
    Path    string // "counts.total" / "records[3].evaluation.score"
    Message string
}
type ValidationErrors []ValidationError  // 实现 error，聚合报告
```

一次报全部问题而不是首个失败即返回 —— 校验失败时用户要的是完整清单。

### 4.3 `score.count == 0` 时 mean/min/max 为 null

```go
type ScoreStats struct {
    Count int      `json:"count"`
    Mean  *float64 `json:"mean"`   // 注意：无 omitempty
    Min   *float64 `json:"min"`
    Max   *float64 `json:"max"`
}
```

**不加 `omitempty`**：规范写的是「三者均为 `null`」，即键必须存在且值为 null，不是键消失。这是 `omitempty` 最容易出错的地方 —— 对 `*float64` 而言 nil 会被 `omitempty` 整个删掉键。同理适用于 `Evaluation.Score`、`Record.StartedAt`、`stop_reason`、所有 `error` 字段。

**定稿规则：凡是规范中出现过 `null` 作为合法值的字段，一律不加 `omitempty`。**

---

## 5. 编解码规则

| 规则 | 实现 |
|---|---|
| 未识别字段忽略 | 用 `encoding/json` 默认行为，**不**启用 `DisallowUnknownFields`（`02` §1「未识别字段应忽略」，是前向兼容的基础） |
| 时间 RFC 3339 UTC | 自定义 `Timestamp` 类型包 `time.Time`，`MarshalJSON` 强制 `.UTC().Format(time.RFC3339)`；可空处用 `*Timestamp` |
| 耗时整数毫秒 | `int64`，字段名 `latency_ms` / `duration_ms` |
| `score` 为 `*float64` | 见 §4 |
| `spec_version` 常量 | `const SpecVersion = "evalexec/v1alpha1"` |

`Timestamp` 而不是直接用 `time.Time`：默认 `time.Time` 序列化带纳秒和本地时区偏移（`2026-07-27T09:00:00.123456789+08:00`），与「RFC 3339 UTC」不符，而且会让 fixtures 的黄金文件无法稳定比对。秒级精度足够 —— 毫秒级耗时另有 `duration_ms` 字段承担。

### 5.1 Usage 的两个新增可选字段

按 dev-plan 开放点 #12（M0 已用 DeepSeek 实测印证：33 个 completion token 里 27 个是 reasoning token，不单列则用量与账单对不上）：

```go
type Usage struct {
    JudgeInputTokens     int `json:"judge_input_tokens"`
    JudgeOutputTokens    int `json:"judge_output_tokens"`
    JudgeCacheReadTokens int `json:"judge_cache_read_tokens,omitempty"`  // 新增可选
    JudgeReasoningTokens int `json:"judge_reasoning_tokens,omitempty"`   // 新增可选
}
```

这两个字段**带 `omitempty`** —— 它们是 `v1alpha1` 内新增的可选字段，规范里没有「为 null」的语义，不产生时应当整个消失，否则会让不使用 Judge 的规则 Grader 的记录里凭空多两个 0。

结果级 `usage.judge_model` 用同构的 `ModelUsage`（字段名去掉 `judge_` 前缀，与 `02` §6 的 `{"input_tokens": 5000, "output_tokens": 1200}` 一致）。

---

## 6. 归一化比对器

fixtures 的 `expected/result.json` 含 `eval_id`、时间戳与 `duration_ms`，逐字节比对必然失败。dev-plan M1 要求「用归一化比对器，而不是字符串相等」，且**比对器自己要有单元测试**。

放在 `evalspec/evalspectest` 子包（测试辅助不该占用 L1 主包的 API 面，但又必须被跨包测试与下游 fixtures 自测使用）。

归一化规则：

| 字段 | 处理 |
|---|---|
| `eval_id` | 替换为 `"<eval-id>"`，但先断言**全文一致**（`result.json` 与每条 record 的 `eval_id` 必须相同 —— 这正是验收标准 9 的一部分，不能归一化掉） |
| `started_at` / `finished_at` | 替换为 `"<ts>"`；null 保持 null（区分「有时间」与「skipped 的 null」是有意义的） |
| `duration_ms` / `latency_ms` | 替换为 `0`；null 保持 null |
| `provenance.implementation.version` | 替换为 `"<version>"` |
| `dataset_sha256` / `eval_request_sha256` | **不归一化** —— 它们必须可复现，是可追溯性的核心 |

比对器的自测覆盖：归一化后相等的两份、只差 `eval_id` 一处的两份（应相等）、`eval_id` 内部不自洽的一份（应报错）、真实差异的两份（应给出字段路径）。

---

## 7. fixtures 包

按 dev-plan §1.4，放 `fixtures/` 而非 `testdata/`：`testdata` 会被 Go 工具链特殊对待，且其他语言实现要能直接 `git clone` 取用。

```text
fixtures/
├── doc.go
├── fixtures.go            //go:embed all:data → FS；Load(name) 辅助函数
└── data/
    ├── f01-exact-match-all-pass/
    │   ├── request.json
    │   ├── dataset.jsonl
    │   └── expected/{result.json,records.jsonl}
    ├── f02-mixed-success-fail/
    ├── f03-llm-judge-basic/
    │   └── judge-responses.jsonl      # 录制的 Judge 响应，M4 消费
    ├── f04-fail-fast-cancelled/
    ├── f05-interrupt-cancelled/
    └── f06-precheck-failures/
        └── <子用例>/{request.json,dataset.jsonl,expected/failure.json}
```

`f06` 与其他五组形状不同：前置校验失败**不产生结果目录**，所以期望值不是 `result.json` 而是 `expected/failure.json`，只含 `{"exit_code": 2, "stage": "dataset_parse"}` 之类的形状断言（dev-plan：「只断言退出码与 stderr 形状」）。子用例覆盖 M2 DoD 列出的 8 种失败。

`f03` 的 `judge-responses.jsonl` 是录制响应：CI 不能依赖真实模型（dev-plan M4「CI 不依赖真实模型」）。

### 7.1 fixtures 里绝不能有真密钥

反过来说，**必须有假密钥**：dev-plan §1.5 要求 CI 扫描结果目录断言不含预置的假密钥串。定一个显眼的哨兵值：

```
EVALEXEC_FIXTURE_FAKE_KEY = "sk-fixture-DO-NOT-LEAK-0000"
```

fixtures 的 `judge_model.auth` 写 `{"type":"bearer_env","env":"EVALEXEC_FIXTURE_KEY"}`，M4 的 `lint-secrets` 用该哨兵值跑扫描。**哨兵值只出现在环境变量里，不进任何 fixture 文件** —— 否则扫描会命中 fixture 自身。

---

## 8. 验证方式（本阶段测试）

| 验证项 | 手段 |
|---|---|
| 三态正确 | 表驱动：缺失 / null / 有值 × `Has` / `IsNull` / `Field` |
| 往返等价 | 每个 fixture 的 `dataset.jsonl` 与 `expected/*` 解析后重新序列化，**语义等价**（非字节相等）比对 |
| 不变量 | `Validate()` 的负向测试：fail 带 score、skipped 带 evaluation、恒等式各破坏一次 |
| 受控构造函数 | 断言 `NewFailEvaluation` 产出的 `Score` 恒为 nil |
| null vs 缺失 | 断言 `ScoreStats{Count:0}` 序列化出 `"mean":null` 而不是键消失 |
| 枚举 | 每个枚举的 `IsValid()` 正负例；断言三个 status 类型不可互相赋值（编译期，写成注释 + 一个 `Example`） |
| 归一化比对器 | §6 列出的四类用例 |
| fixtures 可被外部读取 | 外部测试包（`fixtures_test`）遍历 `FS` 断言 6 组齐备、每组文件齐备 |
| 文档示例 | `Example_*` 全部通过，同时是 pkg.go.dev 上的第一份用法 |

**DoD**：所有 fixtures 能被 `evalspec` 解析并语义等价地序列化回去；归一化比对器有自己的单元测试；`fixtures.FS` 可被外部测试包读到全部用例。

**覆盖验收标准**：7、10（类型层面）
