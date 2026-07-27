# M6 外部协议互操作 — 阶段设计

对应 `doc/dev-plan.md` §4 M6（设计稿 S5）。让 Grader 与 Judge 都能是别的语言写的进程或服务。

这是「协议优先于 SDK」这条边界原则唯一能被证明的地方：在此之前所有实现都是 Go 的，协议不绑定语言只是声明。

---

## 1. 交付物

| 包 | 职责 |
|---|---|
| `grader/httpjson` | `protocol=http-json` 的 Grader |
| `grader/stdiojsonl` | `protocol=stdio-jsonl` 的 Grader |
| `judge/provider/httpjson` | `ais.ChatProvider`，注册名 `http-json` |
| `judge/provider/stdiojsonl` | `ais.ChatProvider` + 子进程 `RoundTripper` |
| `result` | `errors.jsonl`（运行级诊断，可选产出） |
| `contract/` | 参考外部实现 + 契约文档 |

外加：`anthropic-messages` 开放（M4 已可用，本阶段只是补文档与协议名映射的测试）。

---

## 2. Grader 侧协议

### 2.1 `http-json`

```text
POST <grader.entry>
Content-Type: application/json

<GradeCall 的 JSON>          ← 与 02-core-spec.md §4 完全一致

200 OK
<Evaluation 的 JSON>         ← 与 02-core-spec.md §5 的 evaluation 字段一致
```

- 非 2xx → `protocol_error`；
- 响应体不符合 `Evaluation` 形状 → `protocol_error`；
- 响应体是合法 `Evaluation` 但 `status=fail` → **原样采用**，不是 `protocol_error`。外部 Grader 有权说"我评不出来"；
- 连接失败 / 超时 → 分别 `protocol_error` / `timeout`。

**响应体要过 `Evaluation` 的不变量校验**：外部实现可能返回 `status=fail` 带 `score`。定稿：**校验失败即 `protocol_error`**，不静默修正。修正会让外部实现者永远不知道自己写错了。

### 2.2 `stdio-jsonl`

`grader.entry` 是可执行文件路径（可带参数，按 shell 词法分割？**不**——见下）。

```text
stdin:  <GradeCall 的 JSON>\n
stdout: <Evaluation 的 JSON>\n
stderr: → logs/grader-<case_id>.log
```

**`entry` 不做 shell 解析。** 定稿：`entry` 是**单个可执行文件路径**，参数通过 `grader.parameters` 传给 Grader 自己（它已经收到 `parameters`）。理由：shell 解析引入引号、转义与注入问题，而这里根本不需要 —— 需要包装就写个脚本。

### 2.3 并发模型：每个 worker 一个子进程

dev-plan 定稿如此（简单、隔离好）。文档写明**子进程数 = `concurrency`**。

进程池的生命周期：

- 首次使用时惰性启动；
- 一问一答，进程保持存活（避免每样本 fork 的开销）；
- 超时后 **kill 进程组**避免僵尸；被 kill 的进程从池中移除，下次使用时重启；
- 运行结束 / 中断时统一清理。

**为什么要 kill 进程组而不是进程**：子进程可能自己 fork（Python 脚本起了子进程），只 kill 父进程会留下孤儿。用 `Setpgid` + `kill(-pgid)`。

### 2.4 外部 Grader 同样要过前置校验

设计稿明确「不因协议不同而失去前置校验能力」。声明来自**配置文件**，不向外部进程查询 —— 查询会让前置校验依赖它本该先验证的东西。

M2 的 `checkGraderDeclaration` 对非 `builtin` 协议已经直接采用配置里的 `requires`。本阶段只需确认 `entry` 非空且（对 stdio）文件存在且可执行 —— 后者是新增的前置检查。

---

## 3. Judge 侧协议

### 3.1 `ais.ChatProvider` 的四个方法

M0 核对确认 aimodel v0.5.0 的接口与 dev-plan §2.3 所列一致：

```go
NewChatRequest(ctx, *ais.ChatRequest) (*http.Request, error)
ParseChatResponse(io.Reader) (*ais.ChatResponse, error)
ParseErrorResponse(int, []byte) error
NewStreamDecoder(io.Reader) ais.StreamDecoder
```

`NewStreamDecoder` 返回一个永远 `io.EOF` 的 decoder：EvalExec 不使用流式。

### 3.2 注册与每次运行的配置

M0 §2.3.1 已定稿：`ais.Register` **重名 panic**，所以 provider 只能在 `init()` 里注册一次，每次运行的配置通过 `aimodel.WithProviderOptions` 传给工厂。

```go
// judge/provider/httpjson
func init() { ais.Register(Name, New) }

type Options struct {
    Endpoint string
    APIKey   string
}
```

工厂必须拒绝不认识的 Options 类型（`ais.Config.Options` 是 `any`）。

### 3.3 `http-json` 的 wire 格式

EvalExec 自己约定，因为它不是任何厂商的格式：

```json
// 请求
{"messages": [{"role": "system", "content": "..."}, {"role": "user", "content": "..."}],
 "model": "...", "temperature": 0, "response_format": {...}}

// 响应
{"content": "...", "usage": {"input_tokens": 100, "output_tokens": 20,
                             "cache_read_tokens": 0, "reasoning_tokens": 0}}
```

比 OpenAI 的形状简单得多：单条回复、扁平 usage。理由是这个协议的目的是**让别的语言容易实现**，而不是兼容某个厂商。

usage 字段名与 `EvalResult.usage.judge_model` 一致（`input_tokens` 而非 `prompt_tokens`），少一层心智翻译。

### 3.4 `stdio-jsonl` 的传输

provider 照常构造 `*http.Request`（URL 用 `stdio://local` 占位），真正的传输由 `WithHTTPClient` 注入的自定义 `RoundTripper` 完成 —— dev-plan §2.3 的方案。

请求体与响应体就是 §3.3 的 JSON，各一行。

子进程管理与 §2.3 共用一套代码：抽一个 `subprocess` 包。

---

## 4. `errors.jsonl`

运行级诊断，**可选产出**（`02` §7 的 artifacts 里就是可选）。写什么：

| 事件 | 记录 |
|---|---|
| 子进程崩溃 | 命令、退出码、stderr 尾部 |
| 子进程启动失败 | 命令、错误 |
| 连接失败（http-json） | 端点、错误 |
| 日志写入失败 | 已有的 diag 提示改为也记这里 |

**不计入 `checksums.sha256`**（dev-plan 开放点 #5 已定）。`artifacts.errors` 仅在有内容时出现 —— 与 `logs/` 同样的规则，空文件会让人以为诊断跑过而一无所获。

---

## 5. `contract/`：参考实现与契约文档

dev-plan 要求「一组参考外部实现（Go 写的 HTTP server + 一个脚本形式的 stdio Grader），外部实现者照此契约自测」。

```text
contract/
├── README.md              协议规格（请求/响应形状、错误约定、超时语义）
├── grader-http/main.go    参考 HTTP Grader
├── grader-stdio/main.go   参考 stdio Grader
├── judge-http/main.go     参考 HTTP Judge
├── judge-stdio/main.go    参考 stdio Judge
└── grader-stdio.py        Python 版 stdio Grader —— 证明协议不绑定语言
```

Python 版是关键：它与 Go 版跑同一个 fixture、产出语义等价的结果，这才让「协议优先于 SDK」从声明变成事实。它也顺带为 M7 的跨语言验证铺路。

参考实现放 `contract/` 而不是 `examples/`：它们是**契约的一部分**，外部实现者要照着它自测，不是可选的示范代码。

---

## 6. 关键实现点

### 6.1 超时与取消必须穿透子进程

`RoundTripper` 要响应 `req.Context()` 的取消。子进程一问一答期间被取消 → kill 进程组并返回 `context.Canceled`（于是 M5 的机制把样本记 `skipped`）。

这是最容易漏的：一个不看 ctx 的 `RoundTripper` 会让中断卡在等子进程回应上。

### 6.2 子进程的 stderr 必须异步读

子进程写满 stderr 管道缓冲区会**阻塞在写上**，而我们在等 stdout —— 死锁。必须有一个 goroutine 持续读 stderr 到环形缓冲。

### 6.3 一行 JSON 可能很大

`bufio.Scanner` 默认 64KB 上限。子进程返回的 `Evaluation` 含 `evidence`，可以很大。用与 `dataset` 一致的 32MB 上限。

### 6.4 语义等价的判定

DoD 要求「同一 fixture 用 `builtin` 与 `http-json` 两种 Grader 协议跑出语义等价的结果」。等价的定义：归一化后 `records.jsonl` 与 `result.json` 的 `counts` / `evaluation` 块一致。

`evidence` 与 `reason` 不要求逐字相同 —— 参考实现是独立写的，措辞不同不代表协议不兼容。**定稿：比对 `status` / `score` / `label` / `error.code`，不比 `reason` / `evidence`。**

---

## 7. 验证方式

| 验证项 | 手段 |
|---|---|
| `builtin` ≡ `http-json` | 同一 fixture 两种协议，按 §6.4 比对 |
| `builtin` ≡ `stdio-jsonl` | 同上 |
| **Go stdio ≡ Python stdio** | 同上，证明协议不绑定语言 |
| `openai-chat` ≡ `http-json`（Judge） | 同一 fixture 两种 Judge 协议 |
| 外部 Grader 仍过前置校验 | 缺 `requires` 字段的数据集 + `http-json` Grader → 退出码 2 |
| 非 2xx → `protocol_error` | 参考 server 按需返回 500 |
| 响应形状不符 → `protocol_error` | 返回 `{"nonsense": true}` |
| 外部返回违反不变量的 evaluation → `protocol_error` | 返回 `status=fail` 带 `score` |
| 子进程崩溃 → `protocol_error` + `errors.jsonl` | 参考脚本按需 `exit 1` |
| 超时 kill 进程组 | 参考脚本 sleep 超过 `timeout_ms`，断言无僵尸 |
| 取消穿透子进程 | 中断一次 stdio 运行，断言退出码 130 且样本记 `skipped` |
| stderr 大量输出不死锁 | 参考脚本往 stderr 写 1MB |
| 子进程数 = concurrency | 参考脚本记录 PID，断言不同 PID 数 ≤ concurrency |

**DoD**

- 同一 fixture 用 `builtin` 与 `http-json` 两种 Grader 协议跑出语义等价的结果
- 同一 fixture 用 `openai-chat` 与 `http-json` 两种 Judge 协议跑出语义等价的结果
- 契约测试文档化

**覆盖验收标准**：21（协议侧）
