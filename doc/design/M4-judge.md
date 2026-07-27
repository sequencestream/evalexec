# M4 aimodel Judge 接入与 `llm_judge` — 阶段设计

对应 `doc/dev-plan.md` §4 M4（设计稿 S3 后半）。把 §2 的映射方案落成代码。

**`judge` 是唯一 import aimodel 的包** —— 这条在 M0 核对出 v0.4.1 与 v0.5.0 结构完全不同之后更重要了：一次上游重构的爆炸半径必须限制在一个包内。

---

## 1. 交付物

| 包 | 层 | 职责 |
|---|---|---|
| `judge` | **L2** | `judge_model` 配置 → `aimodel.ChatCompleter` 的装配；错误分类；用量映射 |
| `judge/transport` | L3 | 记录原始请求/响应的 `RoundTripper`，含脱敏 |
| `grader/builtin/llm_judge` | L3 | `llm_judge` Grader（放在 `builtin` 包内，与其余四个并列） |

外加：`make lint-secrets` 落地、`f03` 端到端、五种 `error.code` 各一个 fixture。

---

## 2. `judge` 包的接口：一个方法

```go
// Judge is one question and one answer. Nothing above it knows which
// protocol answered.
type Judge interface {
    Complete(ctx context.Context, p Prompt) (Completion, error)
}

func New(cfg *evalspec.JudgeModelSpec, concurrency int) (Judge, error)
```

L2 扩展点，**一个方法**（dev-plan §1.3：接口一开始就要收窄）。

`Prompt` / `Completion` 是 evalexec 自己的类型，不暴露 aimodel 的：

```go
type Prompt struct {
    System string
    User   string
    // ResponseSchema, when set, asks the provider for structured output.
    ResponseSchema map[string]any
}

type Completion struct {
    Text  string
    Usage evalspec.Usage
}
```

这样 aimodel 的类型不会渗透到 `grader/builtin`，`lint` 之外还有个编译期保证。

---

## 3. 配置 → aimodel 的装配（dev-plan §2.2 + M0 校正）

| `judge_model` | aimodel |
|---|---|
| `protocol: openai-chat` | `WithProvider(openai.Name)` |
| `protocol: anthropic-messages` | `WithProvider(anthropic.Name)`（根包已自动注册） |
| `protocol: http-json` / `stdio-jsonl` | M6 |
| `endpoint` | `WithBaseURL` |
| `auth: {bearer_env, X}` | `WithAPIKey(os.Getenv(X))` |
| `auth: {none}` | `WithAPIKey("-")` —— `NewClient` 拒绝空 key |
| `timeout_ms` | 每次调用 `context.WithTimeout`；`WithTimeout` 设为 2 倍兜底 |
| `parameters.*` | `ais.ChatRequest` 字段，**10 个键的白名单**（M0 §2.3.5） |

三条 M0 核对出来的约束：

1. **每个 client 配独立 `*http.Client`** —— `NewClient` 末尾会改写传入 client 的 `Timeout`；
2. **`WithHTTPClient(nil)` 会 panic** —— 必须保证非 nil；
3. **绝不依赖环境变量兜底** —— 始终显式 `WithAPIKey`/`WithBaseURL`/`req.Model`。`auth.env` 为空已在 M2 的第 4 步拦截。

### 3.1 `JudgeChecker` 闭环

M2 留了注入点，本阶段 `judge.New` 实现它：

```go
type Checker struct{ Concurrency int }
func (c Checker) Check(spec *evalspec.JudgeModelSpec) error {
    _, err := New(spec, c.Concurrency)
    return err
}
```

于是 `aimodel.NewClient` 的构造期校验（openai provider 缺 `BaseURL` → `ais.ErrNoBaseURL`）发生在**首次调用之前**，正是设计稿的要求。

---

## 4. 并发与传输调优（dev-plan §2.5）

```go
tr := http.DefaultTransport.(*http.Transport).Clone()
tr.MaxIdleConns        = concurrency * 2
tr.MaxIdleConnsPerHost = concurrency
tr.MaxConnsPerHost     = concurrency
```

默认 `MaxIdleConnsPerHost` 是 2，高并发下会不断新建 TLS 连接。本阶段并发度恒为 1（M5 才有并发），但调优代码现在写好，M5 直接受益。

---

## 5. 记录与脱敏（dev-plan §2.6）

aimodel 非流式没有拦截点，所以在 `RoundTripper` 层做：

- 请求体、响应体、状态码、耗时 → `logs/judge-<case_id>.jsonl`；
- **仅在 `--debug` 或该样本 `fail` 时保留** —— 全量保留会把 prompt 回显写满磁盘；
- `Authorization` 头在记录前替换为 `Bearer ***`；
- 记录不进 `checksums.sha256`，不进 `result.json`。

### 5.1 一个设计文档没提但必须解决的问题：`RoundTripper` 怎么知道 case_id

`RoundTripper` 在 HTTP 层，看不到样本。方案：**通过 `context` 传递**。

```go
ctx = transport.WithCaseID(ctx, call.CaseID)
```

`llm_judge` 在调用 `Complete` 前把 `case_id` 放进 ctx，`RoundTripper` 从 `req.Context()` 取出。这是 ctx 值的合法用途（请求作用域的元数据），且不污染 `Judge` 接口。

### 5.2 「仅 fail 时保留」怎么实现

调用发生时还不知道结果。方案：**先写到内存缓冲，样本评估结束后由 `llm_judge` 决定是否落盘**。

单次 Judge 响应通常几 KB，并发窗口内的缓冲量可控。`--debug` 时直接落盘不缓冲。

---

## 6. 错误分类（dev-plan §2.4）

| aimodel 返回 | `error.code` |
|---|---|
| `*ais.APIError` | `judge_error` |
| `ais.ErrEmptyResponse` | `judge_error` |
| 网络/传输错误 | `judge_error` |
| `errors.Is(err, context.DeadlineExceeded)` | `timeout` |
| `errors.Is(err, context.Canceled)` | **不写 fail** —— 该样本按 `skipped` 处理 |
| 响应正常但文本不是合法 JSON / 缺 `score` | `protocol_error` |
| Judge 明确表示证据不足 | `insufficient_evidence` |
| Grader 内部 panic | `internal_error`（M3 的 `runner` 已实现） |

**`context.Canceled` 与 `DeadlineExceeded` 必须用 `errors.Is` 严格区分**，不能只看 `ctx.Err()` —— dev-plan §7 把这列为头号风险，且它只在 M5 的 fail-fast/中断路径才暴露。本阶段的做法：`judge` 包的分类函数把 `Canceled` 映射成一个哨兵错误 `ErrCancelled`，由 `runner` 在 M5 转成 `skipped`；M4 先让分类函数正确，并单独单测。

`*ais.APIError` 的 `StatusCode` 与 `Code` 写进 `error.message`，**响应体原文不写**（可能含 prompt 回显），原文只进 `logs/`。

---

## 7. `llm_judge` Grader

### 7.1 参数

| 参数 | 作用 |
|---|---|
| `rubric` | 评判标准，拼进 prompt |
| `min_score` / `max_score` | **仅量表元数据，不参与任何比较** |
| `use_reference` / `use_trajectory` | 决定 `requires`（M2 已实现推导）与 prompt 内容 |

`min_score`/`max_score` 要在代码里显式标注「只透传」—— 这是验收标准 12，也是最容易被后人加一行「越界就 clamp」的地方。

### 7.2 prompt 组装

把**实际 `requires` 用到的** Session 字段序列化进去。字段用 XML 风格标签分隔（比 JSON 嵌套更抗 prompt 注入，也更省 token）：

```text
<input>...</input>
<output>...</output>
<trajectory>...</trajectory>   ← 仅当 use_trajectory
<reference>...</reference>     ← 仅当 use_reference
```

### 7.3 结构化输出与兜底

优先 `ResponseFormat`（OpenAI `json_schema`）；失败回退到「prompt 约定 JSON + 容错解析」。两者都失败判 `protocol_error`。

容错解析要处理最常见的三种脏输出：

1. ```` ```json ... ``` ```` 代码围栏；
2. 前后有解释性文字；
3. 单引号 / 尾随逗号 —— **不处理**，这属于模型没按要求输出，判 `protocol_error` 更诚实。

期望字段：`score` / `label` / `reason` / `evidence`，外加 `insufficient_evidence: true` 表示拒绝作答。

### 7.4 `--seed` 不透传（M0 §2.2 确认）

v0.5.0 的 `ais.ChatRequest` 确实没有 `seed`。`llm_judge` 用 `temperature=0` 求稳，并在 README 与 `provenance` 中如实说明「不承诺评分可复现」。

---

## 8. 测试策略：两层

dev-plan M4 明确「CI 不依赖真实模型」。

| 层 | 手段 | 覆盖 |
|---|---|---|
| 业务逻辑 | fake `Judge`（我们自己的接口，一个方法） | prompt 组装、响应解析、五种 code、`min/max_score` 不参与计算 |
| 装配与错误分类 | `httptest.Server` 打真实 wire 格式 | provider 装配、`APIError`、超时、连接复用 |

fake 打在**我们的 `Judge` 接口**上而不是 `aimodel.ChatCompleter` 上 —— 前者只有一个方法，后者有两个且带 aimodel 类型。这正是 §2 收窄接口的回报。

`f03` 的回放：`judge-responses.jsonl` 按 `case_id` 索引，fake Judge 按 `case_id` 返回对应响应。

---

## 9. `make lint-secrets` 落地

M0 留的骨架，本阶段实现：

1. 设 `EVALEXEC_FIXTURE_KEY` 为哨兵值 `sk-fixture-DO-NOT-LEAK-0000`；
2. 跑一遍带 Judge 的 fixture（用 `httptest.Server`，不打真实模型）；
3. 扫描结果目录**全部文件**（含 `logs/`），断言：
   - 不含哨兵值原文（`redact.ContainsSentinel`）；
   - 不含任何密钥形态（`redact.FindSecrets`）；
   - `logs/` 中的 `Authorization` 已替换为 `Bearer ***`。

作为一个 Go 测试实现（`TestNoSecretReachesTheResultDirectory`），`make lint-secrets` 调用它。写成测试而非 shell 脚本，因为它需要先跑出一个结果目录。

---

## 10. 验证方式

| 验证项 | 手段 |
|---|---|
| `f03` 端到端 | 回放 `judge-responses.jsonl`，与黄金文件比对 |
| 五种 `error.code` | 各一个测试；`insufficient_evidence` 与 `protocol_error` 用 fake，`judge_error`/`timeout` 用 `httptest.Server` |
| `fail` 样本照常记 usage | 单独断言（dev-plan 点名） |
| `fail` 时 `score` 为 null | 断言 Judge 返回了分数也被丢弃 |
| `Canceled` ≠ `timeout` | 分类函数单测，两者必须分到不同结果 |
| 密钥不泄漏 | §9 |
| 参数白名单 | 未知键（含 `seed`）报参数错误 |
| `min/max_score` 只透传 | 构造一个超出量表的分数，断言原样记录、不被 clamp |

**DoD**

- `f03` 通过；5 个 `error.code` 各有一个通过的 fixture
- 断言：任何 `fail` 记录的 `score == null` 且未进入 `score.count`
- 断言：结果目录与 `logs/` 中都不出现环境变量里的假密钥串
- 断言：`Authorization` 头在日志中已被替换

**覆盖验收标准**：11、12、13、18
