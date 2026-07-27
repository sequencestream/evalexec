# M3 串行执行与规则 Grader — 阶段设计

对应 `doc/dev-plan.md` §4 M3（设计稿 S2 + S3 前半）。本阶段第一次真正产出 `EvalResult`：`evalexec` 从「只会拒绝」变成「能跑完一次评估」。

结束时 `f01`、`f02` 端到端通过，且**不依赖任何模型服务** —— 这正是 dev-plan 的 v0.0.1 可交付节点。

---

## 1. 交付物

| 包 | 层 | 职责 |
|---|---|---|
| `grader` | **L2** | `Grader` 接口、`Declaration`、`GradeCall`、注册表 |
| `grader/builtin` | L3 | `exact_match` / `contains` / `regex` / `json_schema` |
| `runner` | L3 | 串行派发、单写入 goroutine |
| `summary` | L3 | 计数恒等式与 score 统计 |
| `result` | L3 | 补齐内容写入（M2 只做了目录生命周期） |
| 根包 `evalexec` | **L2** | 门面 `Run(ctx, req, ...Option)` |

---

## 2. `Grader` 接口：L2 扩展点，一开始就要收窄

```go
type Grader interface {
    Declare() Declaration
    Grade(ctx context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error)
}
```

**两个方法就是全部。** dev-plan §1.3 说明了原因：这是 L2，v1.0 后给接口加方法就是破坏性变更。以后想加能力只能加在 `Declaration` 或 `GradeCall` 的**字段**上 —— 加字段是兼容的，加方法不是。

### 2.1 `Grade` 返回 `(Evaluation, error)` 的语义边界

这是本阶段最容易设计错的一处。两个返回值必须表达**不同层次**的事：

| 情形 | 返回 |
|---|---|
| Grader 完成了评估（无论 agent 表现好坏） | `Evaluation{Status: success}`, `nil` |
| Grader 明确「评不出来」（证据不足、Judge 挂了、超时） | `Evaluation{Status: fail, Error: {...}}`, `nil` |
| Grader 自己坏了（panic、内部 bug） | 零值, `error` |

第二行是关键：**「评估失败」是一个正常返回值，不是 error**。`fail` 是协议里的一等状态，样本仍算 `completed`。如果把它做成 error，调用方就得从 error 里反解 `error.code`，而那正是 `Evaluation.Error` 已经表达的。

第三行的 error 由 `runner` 兜底转成 `internal_error` 的 `fail` 记录 —— 一个 Grader 崩溃不该让整个 run 失败（`03` §3：「单个样本评估失败默认不终止其他样本」）。

### 2.2 注册表与下游扩展

```go
func Register(entry string, factory Factory)      // init() 中调用，重名 panic
func Lookup(entry string) (Factory, bool)
type Factory func(spec evalspec.GraderSpec) (Grader, error)
```

与 `ais.Register` 同构，包括**重名 panic** —— 静默覆盖会让行为依赖 import 顺序。

下游 Go 程序 import evalexec 后可以注册自己的 Grader，用 `protocol: "builtin"` + 自定义 `entry` 直接跑，不必走子进程。**这不算范围蠕变**：能力在下游自己的二进制里，`cmd/evalexec` 这个二进制仍然只注册内置的五个。

注册表要能被替换（`WithGraderRegistry`），否则测试之间会互相污染全局状态。

### 2.3 `declaration` 与 `grader` 的关系

M2 已经建了 `grader/declaration`（前置校验要在构造 Grader 之前知道 `requires`）。本阶段 `Grader.Declare()` 返回的 `Declaration` 就复用它，避免同一张表写两遍。

`EvaluationSummary.GraderID/GraderVersion` 取自**配置**（`grader.id`/`grader.version`），不是 `Declare()`：`Declaration` 描述的是内置 entry 的能力（`exact_match` 需要哪些字段），而 `id`/`version` 是调用方给这次评估起的名字（`order-status-exact` / `v1`）。dev-plan M3 说「取自 `Declare()` 而非配置文件原文」，那是针对二者可能不一致的担忧 —— 但实际上它们描述的是不同东西，且 M2 的第 3 步已经保证配置与声明一致。**定稿：`id`/`version` 取配置，`requires`/`requires_judge` 以 `Declare()` 为准。**

---

## 3. 四个规则 Grader 的语义

fixtures 已经把 `exact_match` 的行为钉住了，这里补齐四个的完整契约。

**共同规则**：比较得出结论 → `success`（分数 1 或 0）；无法比较 → `fail`。**「不匹配」是 success 加 0 分，不是 fail。**

### 3.1 `exact_match`

- 参数：`reference_path`（默认 `$.expected_output`）
- 取 `reference` 中该路径的值，与 `output` 做 **JSON 语义相等**比较
- 路径不存在 → `fail` / `insufficient_evidence`
- `evidence` 给出参与比较的两个值

JSON 语义相等：解码成 `any` 后递归比较，而不是字节比较 —— 键序与空白不该影响结论。

### 3.2 `contains`

- 参数：`reference_path`（默认 `$.expected_contains`）、`case_sensitive`（默认 `false`）
- 参考值为字符串或字符串数组；**全部**出现在 `output` 的文本形式中才算匹配
- `output` 的文本形式：字符串直接取值，其他类型取其紧凑 JSON
- 参考值缺失或不是字符串/字符串数组 → `fail` / `insufficient_evidence`

### 3.3 `regex`

- 参数：`pattern`（**必填**）、`case_sensitive`（默认 `false`）
- 对 `output` 的文本形式做搜索
- `pattern` 缺失或无法编译 → **前置校验失败（退出码 2）**，不是运行期 `fail`

最后一条是有意的：正则写错是配置错误，在第一个样本上失败与在第一千个样本上失败没有区别，不如在跑之前就说。因此 `declaration` 里 `regex` 的参数校验要在 M2 的第 3 步就编译一次。**这需要扩展 `declaration`**，见 §7。

### 3.4 `json_schema`

- 参数：`schema`（**必填**，内联 JSON Schema 对象）
- 对 `output` 做校验，通过 → 1 分 / `valid`，不通过 → 0 分 / `invalid`，`evidence` 列出违规项
- `schema` 缺失或本身非法 → 前置校验失败

用 `santhosh-tekuri/jsonschema/v6`。这是 dev-plan 允许的第二个直接依赖，`check-deps` 白名单相应扩充。

---

## 4. `runner`：串行版本，但按并发的形状写

dev-plan M3 的关键提醒：

> `records.jsonl` 写入顺序：串行阶段等于输入顺序；并发阶段（M5）改为完成顺序。为避免返工，从本阶段就用**单写入 goroutine + channel**，而不是在循环里直接写。

因此本阶段的 `runner` 已经是：

```text
dataset reader ─→ [dispatch loop] ─→ results chan ─→ [single writer goroutine] ─→ records.jsonl
                                                   └─→ summary accumulator
```

串行只体现在「dispatch loop 一次只处理一个样本」。M5 把 dispatch loop 换成 worker 池即可，写入端与汇总端不动。

**汇总也在写入 goroutine 里做**，而不是最后遍历 records：只有一个 goroutine 碰计数器，天然无竞态，也不必把全部记录留在内存。

### 4.1 三级 context 的位置（本阶段只用两级）

`grader.timeout_ms`（外）→ `judge_model.timeout_ms`（内，M4）→ 全局取消。

本阶段实现最外层：每个样本 `context.WithTimeout(ctx, grader.timeout_ms)`。超时 → `fail` / `timeout`。

### 4.2 panic 恢复

Grader 是可扩展点，下游代码会崩。`runner` 在调用 `Grade` 处 `recover()`，转成 `fail` / `internal_error`。不 recover 会让一个下游 bug 杀掉整个进程 —— 对库形态尤其不可接受。

---

## 5. `summary`：恒等式自检是最后一道防线

计数在写入 goroutine 里累加，写盘前调用 `EvalResult.Validate()`（M1 已实现全部恒等式）。

**不满足则整体降级为 `status=failed` + 退出码 3**，而不是写出一个自相矛盾的结果。dev-plan §7 明确：「`result` 包在落盘前无条件调用 `EvalResult.Validate()`，不信任调用方」。

score 统计：只统计 `success` 且 `score != nil` 的样本。`count == 0` 时 `mean`/`min`/`max` 全 `null`。

**浮点均值的确定性**：`mean = sum / count`，按记录顺序累加。并发下记录顺序会变，浮点加法不满足结合律，理论上会导致 `mean` 的最后几位随运行而变。定稿：**累加时按 `sequence` 排序** —— 但那要求把所有分数留在内存。折中：分数是 `float64`，数据集规模到百万级时误差仍在 1e-10 量级，而 fixtures 的归一化比对本就不比较到最后一位。**本阶段按完成顺序累加，并在 README 注明 `mean` 不保证跨并发度逐位一致。**

---

## 6. 根包门面 `Run`

```go
func Run(ctx context.Context, req *evalspec.EvalRequest, opts ...Option) (*evalspec.EvalResult, error)

type Option func(*config)
func WithGraderRegistry(r *grader.Registry) Option
func WithClock(c Clock) Option
func WithIDGenerator(g IDGenerator) Option
func WithDiagnosticWriter(w io.Writer) Option
```

内部串起 `validate` → `result.Create` → `dataset` → `runner` → `summary` → `result.Publish`。

**`cmd/evalexec` 从本阶段起只调这一个函数。** 库的主用法应该和 CLI 一样是一次原子调用。

`Run` 返回 `(*EvalResult, error)`：

- 前置校验失败 → `(nil, err)`，无结果目录；
- 运行完成（含 `cancelled`）→ `(result, nil)`，退出码由 `exitcode.FromResult` 决定；
- 运行级故障 → `(result_with_failed_status, err)` —— 两者都给，调用方既能拿到诊断也能看到部分信息。

---

## 7. 需要扩展 `declaration`：参数的前置校验

M2 的 `declaration` 只校验参数**名**。`regex` 的 `pattern` 与 `json_schema` 的 `schema` 需要校验**值**（能否编译 / 是否合法 schema），且必须在第 3 步做。

扩展方式：给 `Declaration` 加一个可选钩子

```go
type Declaration struct {
    ...
    // ValidateParams checks parameter values, not just their names. It runs
    // during pre-check so a bad pattern fails before the first sample.
    ValidateParams func(params map[string]any) error
}
```

`Declaration` 是 L3，加字段没有兼容性问题。`validate` 的第 3 步在 `EffectiveRequires` 之后调用它。

---

## 8. 顺带解决 M1/M2 的两处遗留

1. **fixtures 的 `eval_request_sha256` 归一化豁免**：M2 定稿了 `redact.Canonical`，本阶段能真正跑出 `result.json`，因此可以填入真实摘要并从 `evalspectest` 移除 `PlaceholderEvalRequestSHA256`。
2. **`check-deps` 白名单**加入 `santhosh-tekuri/jsonschema/v6`。

---

## 9. 验证方式（本阶段测试）

| 验证项 | 手段 |
|---|---|
| `f01`、`f02` 端到端 | 跑真实 `Run`，与黄金文件归一化比对 |
| 行数恒等 | 所有端到端测试的公共断言：`records.jsonl` 行数 == 数据集行数 |
| 恒等式自检的负向 | 注入一个故意算错计数的 summary，断言降级为 `failed` + 退出码 3 |
| 四个 Grader 的语义 | 每个表驱动：匹配 / 不匹配 / 证据不足 / 边界值 |
| **不匹配是 success 不是 fail** | 单独断言，这是最容易写反的一条 |
| Grader panic | 注册一个必崩的 Grader，断言得到 `internal_error` 的 `fail` 记录且其余样本照常 |
| 超时 | 注册一个慢 Grader + 极小 `timeout_ms`，断言 `timeout` |
| 空数据集 | 0 行 → `completed`、`total=0`、`score.*` 全 null、退出码 0 |
| 两次调用互不干扰（验收 20） | 连跑两次不同 `output-dir`，断言两个独立 `eval_id` 与目录 |
| 摘要可复现 | 同一请求跑两次，`dataset_sha256` 与 `eval_request_sha256` 相同 |
| 下游注册自定义 Grader | 一个测试模拟下游：注册自定义 entry 并跑通 |
| 原子性 | 运行中途强制失败，断言目标目录不存在 |

**DoD**：`f01`、`f02` 端到端通过；10 条数据单命令跑完且行数一致；恒等式自检有负向测试。

**覆盖验收标准**：8、9（非停止路径）、10、12、13、20
