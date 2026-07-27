# aimodel 升级检查清单

当前钉版：`github.com/vogo/aimodel v0.5.0`。

**升级只需检查一个包**：`judge/` 是唯一 import aimodel 的地方。这不是过度谨慎 —— v0.4.1 → v0.5.0 是一次把整个 API 重排的重构（单包扁平 → `ais` 子包 + `provider/*` + 注册式分发），而 evalexec 的其余部分完全不受影响。

`judge/provider/httpjson` 与 `judge/provider/stdiojsonl` 也 import `ais`（它们实现 `ais.ChatProvider`），同样在检查范围内。

---

## 逐条重验

M0 核对源码时确认过的每个假设。升级后按序重验，**不要只看编译过不过** —— 下表里有一半的项目即使编译通过也可能已经变了语义。

### 类型与包路径

| 假设 | 怎么验 |
|---|---|
| canonical 类型在 `ais` 子包 | `judge/` 编译通过即可 |
| `openai.Name == "openai"`、`anthropic.Name == "anthropic"` | 编译通过即可；名字变了会断 |
| `provider/anthropic` 由根包 `client.go` 空白 import 自动注册 | `TestEveryProtocolConstructs` |
| `aimodel.ChatCompleter` 的方法签名 | `judge.client` 编译通过即可 |
| `ais.ChatProvider` 的四个方法 | 两个自定义 provider 编译通过即可 |
| `ais.Register` / `ais.Lookup` 存在 | 同上 |

### 语义 —— 这些编译能过但可能已经变了

| 假设 | 为什么重要 | 怎么验 |
|---|---|---|
| **`ais.Register` 对重名 panic** | 若改成静默覆盖，两个 provider 注册同名时行为将取决于 import 顺序 | 读 `ais/registry.go`；或写一个临时测试重复注册并期待 panic |
| **`NewClient` 会改写传入 `*http.Client` 的 `Timeout`** | 若不再改写，`WithTimeout` 的兜底就失效了；若仍改写而我们改成共享 client，超时会互相覆盖 | 读 `client.go` 末尾 |
| **`WithHTTPClient(nil)` panic** | 决定 `judge` 是否需要自己保证非 nil | 读 `client.go` |
| **环境变量兜底顺序**：`AI_API_KEY` → `OPENAI_API_KEY` → `ANTHROPIC_API_KEY`，以及 `AI_BASE_URL` / `AI_MODEL` | evalexec 始终显式传参正是为了让兜底永不生效。若新增了别的兜底通道（如从文件读配置），要确认它也不会生效 | 读 `NewClient`；`TestCompleteAgainstAServer` 断言请求里的 model 是我们设的那个 |
| **空 API key 返回 `ais.ErrNoAPIKey`** | `auth.type = "none"` 的占位 key 约定依赖它 | `TestAuthNoneNeedsNoCredential` |
| **openai provider 缺 `BaseURL` 返回 `ais.ErrNoBaseURL`** | 前置校验第 4 步靠构造期失败来拦截 | `TestConstructionValidatesConfiguration/missing endpoint` |
| **空 choices 返回 `ais.ErrEmptyResponse`** | 错误分类靠 `errors.Is` 识别它 | `TestErrorCodes/no choices` |
| **`*ais.APIError` 含 `StatusCode` / `Code`** | 写进 `error.message` 的就是这两个 | `TestErrorCodes`、`TestErrorMessageOmitsTheResponseBody` |

### canonical 字段：新增与移除都要看

| 假设 | 怎么验 |
|---|---|
| `ais.ChatRequest` **没有** `Seed` | `grep -n 'Seed' $(go env GOMODCACHE)/github.com/vogo/aimodel@<新版本>/ais/schema.go`。**若新增了 `Seed`**，`--seed` 就可以透传了，需要更新 `judge` 的参数白名单、dev-plan §2.7、README 的「三点限制」与 `Execution.Seed` 的注释 |
| `judge` 的 10 个参数键都仍有对应字段 | `judge` 编译通过即可 |
| **是否新增了 canonical 字段** | 对比 `ais/schema.go` 的 `ChatRequest`。新增的字段若在 ≥2 个 provider 共有，可能值得加进白名单 —— 白名单是显式的，不加就是拒绝 |
| `ais.Usage` 的 `CacheReadTokens` / `ReasoningTokens` | `TestUsageCarriesReasoningTokens` |
| `ais.Usage.Add` 的语义（不合并 `ServiceTier` 与 `Extensions`） | 读 `ais/schema.go`；evalexec 的 `Usage.Add` 是自己的实现，只需确认字段映射仍对 |

---

## 升级步骤

```bash
# 1. 看看变了什么
go list -m -versions github.com/vogo/aimodel
git -C $(go env GOMODCACHE)/github.com/vogo/aimodel@<新版本> log --oneline    # 若有 git 信息

# 2. 升级并钉死精确版本（不用 latest）
go get github.com/vogo/aimodel@<新版本>
go mod tidy

# 3. 编译能过只是最低要求
make build

# 4. judge 包的全部测试。它们分两层：业务逻辑用 fake，
#    装配与错误分类用 httptest.Server 打真实 wire 格式。
go test ./judge/...

# 5. 全量
make all

# 6. 真实端点 —— 这一步不能省。上面每一层都可以在
#    provider 的 wire 格式变了之后仍然通过。
export OPENAI_BASE_URL=... OPENAI_API_KEY=... OPENAI_MODEL=...
make test-e2e
```

第 6 步是关键。`httptest.Server` 打的是**我们以为**的 wire 格式；只有真实端点能证明 provider 仍然按那个格式说话。

---

## 若 aimodel 的结构再次大改

M0 面对 v0.4.1 与 v0.5.0 的抉择过程可以复用（见 `doc/design/M0-baseline.md` §2）：

1. **先读源码，不要信文档**。v0.4.1 与 dev-plan 假设的结构完全不同，而这一点只有读 `ais/provider.go` 才看得出来。
2. **判断扩展点是否还在**。EvalExec 需要的是「能注册自定义 provider」。v0.4.1 没有这个能力，`http-json` / `stdio-jsonl` 在它上面无法实现 —— 那是选版本的决定性条件。
3. **把偏差写成文档再动手**。M0 的做法是先写一份逐条核对表，再改代码；否则 M4/M6 会按错误前提实现。
4. **爆炸半径检查**：`grep -rl 'vogo/aimodel' --include='*.go' .` 的结果应当只有 `judge/` 下的文件。若它扩散了，先收回来再升级。
