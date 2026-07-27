# M0 工程基线 — 阶段设计

对应 `doc/dev-plan.md` §4 M0。本阶段不实现任何评估业务逻辑，只交付「后续七个阶段都依赖的地基」，并把 dev-plan §2 的 aimodel 对接方案与**实际发布的 v0.5.0 API 逐条核对定稿**。

---

## 1. 本阶段要解决的问题

| 问题 | 交付物 |
|---|---|
| 模块路径、Go 版本、依赖钉版 | `go.mod` |
| 每个包的职责与稳定性层级可查 | 每包 `doc.go` |
| 构建/测试/检查有统一入口 | `Makefile` |
| 词根边界（禁 `evaluator`）能自动守住 | `make lint-terms` + CI |
| 库路径不得 `os.Exit` / `signal.Notify` / 硬编码 `os.Stderr` | `make lint-boundary` + CI |
| 版本号可注入并被 `provenance` 使用 | `-ldflags` + `version` 包 |
| aimodel v0.5.0 真实 API 已验证可用 | `judge/aimodel_smoke_test.go`（`//go:build e2e`） |
| CI 基线 | `.github/workflows/ci.yml` |

**不做**：任何 `evalspec` 类型、任何 Grader、任何 CLI flag。`cmd/evalexec` 只打印版本。

---

## 2. aimodel 版本选型与 API 核对

### 2.1 选 v0.5.0，不选 v0.4.1

`v0.4.1` 是**单包扁平布局**（所有类型在根包 `aimodel`，协议是 `WithProtocol(ProtocolOpenAI|ProtocolAnthropic)` 硬编码 switch，**没有任何 provider 注册扩展点**）。在它之上，dev-plan §2.3 的核心手法——「把 `http-json` / `stdio-jsonl` 实现成自定义 provider」——根本无法实现。

`v0.5.0` 做了「核心抽象重构：provider 子包化 + 注册式分发 + 能力接口分层」，**恰好就是 dev-plan §2 所假设的结构**。因此选 v0.5.0，dev-plan §2 的方案整体成立。

`go.mod` 钉死 `github.com/vogo/aimodel v0.5.0`。

### 2.2 逐条核对结果

| dev-plan §2 的断言 | v0.5.0 实际 | 结论 |
|---|---|---|
| 有 `ais` 子包承载 canonical 类型 | ✅ `ais.ChatRequest` / `ChatResponse` / `Usage` / `Message` / `APIError` | 成立 |
| `provider/openai`、`provider/anthropic` | ✅ `openai.Name = "openai"`、`anthropic.Name = "anthropic"` | 成立 |
| `aimodel.WithProvider(name)` | ✅ `func WithProvider(name string) Option` | 成立 |
| `ais.ChatProvider` 四方法接口 | ✅ `NewChatRequest` / `ParseChatResponse` / `ParseErrorResponse` / `NewStreamDecoder`，签名与 dev-plan §2.3 所列**完全一致** | 成立 |
| `ais.Register(name, factory)` | ✅ 存在，`ais.Lookup` 配套 | 成立 |
| `ais.Usage` 有 `CacheReadTokens` / `ReasoningTokens` + `Add()` | ✅ 全部存在 | 成立，开放点 #12 照做 |
| `ais.ErrNoAPIKey` / `ErrNoBaseURL` / `ErrEmptyResponse` / `*ais.APIError` | ✅ 全部存在 | §2.4 错误分类表成立 |
| **`ChatRequest` 没有 `seed` 字段** | ✅ 确实没有（v0.5.0 把 canonical 字段收窄为「≥2 个 provider 共有」，`Seed` 被移除） | **§2.7 缺口 #1 成立**，`--seed` 不透传，只记 `provenance` |
| aimodel 零外部依赖、`go 1.26` | ✅ | evalexec 同样用 `go 1.26` |

### 2.3 dev-plan 未覆盖、但影响实现的事实

以下几条是核对源码时新发现的，必须在 M0 定稿，否则 M4/M6 会踩：

1. **`ais.Register` 对重名 panic**，且明确「注册是单调的、重名是编程错误」。
   → 后果：`http-json` / `stdio-jsonl` 这两个自定义 provider **只能在 `init()` 里各注册一次**，不能「每次评估注册一个带该次配置的 provider」。
   → 定稿：**每次评估的配置（endpoint / 子进程命令 / 超时）通过 `aimodel.WithProviderOptions(...)` 传给工厂**，由 `ais.Config.Options` 接收。这正是 v0.5.0 为 vendor 配置预留的通道，工厂需拒绝不认识的 Options 类型。

2. **`provider/anthropic` 由根包 `client.go` 的空白 import 自动注册**。
   → dev-plan §2.3「顺带开放 `anthropic-messages`，只需加一行空白 import」比预想更省：import 根包 `aimodel` 即已注册，只需加协议名映射。

3. **`NewClient` 末尾执行 `httpClient.Timeout = cfg.timeout`，会改写调用方传入的 `*http.Client`**。
   → 定稿：`judge` 包为每个 client 构造**独立的** `*http.Client` 实例，不跨 client 共享，避免超时被互相覆盖。

4. **`WithHTTPClient(nil)` 直接 panic**（v0.5.0 在 Option 构造期就 panic）。
   → `judge` 包必须保证非 nil。

5. **`ais.ChatRequest` 的 canonical 字段被大幅收窄**。v0.4.1 有而 v0.5.0 **没有**的：`Seed`、`N`、`FrequencyPenalty`、`PresencePenalty`、`User`、`Verbosity`、`Logprobs`、`TopLogprobs`、`LogitBias`、`ServiceTier`、`Store`、`Metadata`、`PromptCacheKey`、`StreamOptions`、`Modalities`、`Audio`。
   → 定稿 M4 的 `judge_model.parameters` **白名单**（dev-plan §2.2 要求「未知键直接报参数错误」，故白名单必须显式）：

   | `parameters` 键 | `ais.ChatRequest` 字段 |
   |---|---|
   | `model` | `Model` |
   | `temperature` | `Temperature *float64` |
   | `max_completion_tokens` | `MaxCompletionTokens *int` |
   | `max_tokens` | `MaxTokens *int`（已弃用，仅为老模型保留） |
   | `top_p` | `TopP *float64` |
   | `top_k` | `TopK *int` |
   | `stop` | `Stop []string` |
   | `reasoning_effort` | `ReasoningEffort string` |
   | `parallel_tool_calls` | `ParallelToolCalls *bool` |
   | `response_format` | `ResponseFormat any` |

   其余键（含 `seed`）一律参数错误 → 退出码 `2`。

6. **`ChatCompleter` 在根包 `aimodel`，但参数类型在 `ais`**：
   ```go
   type ChatCompleter interface {
       ChatCompletion(ctx context.Context, req *ais.ChatRequest) (*ais.ChatResponse, error)
       ChatCompletionStream(ctx context.Context, req *ais.ChatRequest) (*Stream, error)
   }
   ```
   → `judge` 包对上暴露的统一面就是它（dev-plan §2.3 的收益全部保留）。

7. **空 choices 由 provider 层返回 `ais.ErrEmptyResponse`**（`provider/openai/openai.go:101`、`provider/anthropic/provider.go:159`）。
   → §2.4 中「`ErrEmptyResponse` → `judge_error`」的分类可直接用 `errors.Is`。

### 2.4 需回写 dev-plan 的内容

M0 收尾时回写 `doc/dev-plan.md`：

1. §2 开头补一句「以 aimodel **v0.5.0** 为准」；
2. §2.1 的 import 块补 `provider/openai`（取 `openai.Name`），并注明 `WithModel` 实为 `WithDefaultModel`（仅在 `ChatRequest.Model` 为空时兜底，本项目始终显式设 `Model`，兜底不生效）；
3. §2.3 补「自定义 provider 的每次运行配置走 `WithProviderOptions`，因为 `ais.Register` 重名 panic」；
4. §2.5 补「每个 client 独立 `*http.Client`，因 `NewClient` 会改写其 `Timeout`」；
5. §2.7 表格新增一行「canonical 字段收窄 → `parameters` 白名单固定为 10 个键」；
6. §1.2 目录树新增 `version/`。

---

## 3. 目录与稳定性分层

按 dev-plan §1.2 建包。M0 只建**目录 + `doc.go`**，类型留给各自阶段。

```text
evalexec/
├── go.mod  Makefile  .golangci.yml  README.md
├── evalexec.go            L2  根包门面（M3 填充）
├── version/               L3  ldflags 注入点（M0）
├── cmd/evalexec/          —   main（M0 只打印版本）
├── evalspec/              L1  协议类型（M1）
├── fixtures/              L1  跨语言 fixtures（M1）
├── grader/                L2  接口 + 注册表（M3）
│   ├── builtin/           L3  内置 Grader（M3/M4）
│   ├── httpjson/          L3  （M6）
│   └── stdiojsonl/        L3  （M6）
├── judge/                 L2  唯一 import aimodel 的包（M4）
│   ├── transport/         L3  记录 + 脱敏 RoundTripper（M4）
│   └── provider/          L3  httpjson / stdiojsonl（M6）
├── dataset/               L3  （M2/M3）
├── validate/              L3  （M2）
├── runner/                L3  （M3/M5）
├── summary/               L3  （M3）
├── result/                L3  （M2/M3）
├── redact/                L3  （M2）
├── exitcode/              L3  （M2）
├── cli/                   L4  不承诺兼容（M2）
└── doc/design/            阶段设计文档
```

`version` 是 dev-plan 未列出的新增包：`provenance.implementation.version` 需要一个 ldflags 注入点，而它既不能放 `cmd/`（库路径也要读到），也不该塞进 `evalspec`（L1 不该随构建变动）。定为 L3。

每个 `doc.go` 固定两段：首段包职责，末段稳定性层级 + 承诺（照抄 dev-plan §1.3 对应行）。

**M0 只创建当下有代码的包**（`version`、`cmd/evalexec`、`judge` 的冒烟测试位），其余包在各自阶段随代码一起建 —— 空目录进不了 git，建了也是噪音。

---

## 4. 守卫设计

### 4.1 词根守卫（dev-plan §1.5）

全仓库（`git ls-files` 范围）grep `evaluator`，命中即失败。排除 `doc/design/`（本文档要引用该词说明禁令）。

### 4.2 库路径边界守卫（dev-plan §1.5 / §7）

`cmd/` 之外、非 `_test.go` 的 `.go` 文件**禁止**出现：

| 禁项 | 理由 |
|---|---|
| `os.Exit` | 库被嵌入时会杀死宿主进程 |
| `signal.Notify` | 抢宿主进程的信号 |
| `os.Stderr` | 污染宿主输出；诊断须走可注入 `io.Writer` |

测试文件豁免（M5 的中断测试需要起子进程并处理信号）。

### 4.3 API 兼容性守卫

`gorelease` 未预装。`make apidiff` 用 `go run golang.org/x/exp/cmd/gorelease@latest` 按需拉取，**失败不阻断**（v0 期间只警告，符合 dev-plan）。CI 中 `continue-on-error`。v1.0 起改硬失败。

### 4.4 密钥泄漏守卫

dev-plan §1.5 要求「对 fixtures 跑一遍结果目录扫描，断言不含预置的假密钥串」。该扫描依赖 M1 的 fixtures 与 M3 的可运行二进制，**M0 只留 `make lint-secrets` 骨架**，M4 落地（届时才有 Judge 与 `logs/`）。

### 4.5 依赖面守卫

`make check-deps`：断言 `go list -m all` 中非标准库的**直接**依赖只有 `github.com/vogo/aimodel`。M3 会引入 JSON Schema 库（dev-plan 允许「aimodel + 一个 JSON Schema 库」），届时把它加进白名单。

---

## 5. aimodel 连通性冒烟测试

`judge/aimodel_smoke_test.go`，`//go:build e2e`：用真实端点打一次 `ChatCompletion`，断言 `resp.Choices` 非空、`resp.Usage.PromptTokens > 0`。

- 默认不跑（`make test` 不带 `-tags e2e`）；
- `make test-e2e` 显式触发，读 `OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL`；
- 缺环境变量时 `t.Skip`，不 fail；
- 它同时是**最终 E2E 验证的第一块拼图**：证明 DeepSeek 端点在 `provider/openai` 下可用，且 `Usage` 能正常回填。

M0 阶段 `judge` 包尚无生产代码，该文件与 `doc.go` 是包内仅有的两个文件。

---

## 6. 版本注入

```go
// version/version.go
package version
var (
    Name    = "evalexec"
    Version = "dev"      // -X github.com/sequencestream/evalexec/version.Version=...
    Commit  = "none"
    Date    = "unknown"
)
func String() string  // "evalexec 0.1.0 (abc1234, 2026-07-27)"
```

`Makefile` 从 `git describe --tags --always --dirty` 取版本。仓库当前无 tag，退化为短 commit。

DoD 中「`evalexec --version` 输出与 git tag 一致」在无 tag 时降级为「与 `git describe --tags --always --dirty` 一致」，打了首个 tag 后自然满足。

---

## 7. 验证方式（本阶段测试）

| 验证项 | 手段 |
|---|---|
| 依赖面 | `make check-deps` |
| 构建 | `make build` 产出 `bin/evalexec` |
| 版本 | `version_test.go` 覆盖 `String()` 的四种组合；`cmd` 层用构建后的二进制跑 `--version` 比对 |
| 词根守卫 | `make lint-terms` 通过 + 一次注入式负向验证 |
| 边界守卫 | `make lint-boundary` 通过 + 一次注入式负向验证 |
| lint | `golangci-lint run` 零告警 |
| aimodel 可用 | `make test-e2e`（DeepSeek）真实打一次 |

**DoD**：`make build && make test && make lint` 在干净环境通过；`make check-deps` 通过；`make test-e2e` 在给定 DeepSeek 环境变量下通过。
