# M0 验证报告

对应 `doc/design/M0-baseline.md` §7。全部验证项在 `go1.26.3 darwin/arm64`、`golangci-lint 2.12.2` 上实测通过。

---

## 1. DoD 逐条核对

| DoD 项 | 结果 | 证据 |
|---|:--:|---|
| `make build` 通过 | ✅ | 产出 `bin/evalexec` |
| `make test` 通过（`-race`） | ✅ | 3 个包，`cmd/evalexec` 与 `version` 有测试 |
| `make lint` 通过 | ✅ | `golangci-lint run` → `0 issues.` |
| `make check-deps` 通过 | ✅ | `check-deps: ok (1 direct dependencies)` |
| `--version` 与 `git describe` 一致 | ✅ | 见 §2 |
| `make test-e2e` 在 DeepSeek 下通过 | ✅ | 见 §4 |

## 2. 版本注入

```console
$ make build && ./bin/evalexec --version
evalexec e128521 (e128521, 2026-07-27T07:31:01Z)

$ git describe --tags --always --dirty
e128521
```

仓库尚无 tag，`git describe --always` 退化为短 commit，与设计文档 §6 的预期一致。打首个 tag 后自然满足「与 tag 一致」。

`TestLdflagsStampReachesBinary` 用独立的 `-X` 值真实构建一次二进制并比对 `--version` 输出，防止 `-X` 路径写错时静默把 `dev` 写进 `provenance`。

## 3. 守卫的正反向验证

设计文档 §7 要求两个守卫都做**注入式负向验证** —— 只跑通过态无法证明守卫真的在工作。

| 守卫 | 正向 | 负向（注入违规后） |
|---|:--:|:--:|
| `lint-terms` | `ok` | 向 `version/version.go` 追加含 `evaluator` 的注释 → **exit 2**，精确报出 `version/version.go:54` |
| `lint-boundary` | `ok` | 向 `judge/doc.go` 追加 `os.Exit(1)` → **exit 2**，精确报出 `judge/doc.go:22` |

首轮验证暴露了两个真实缺陷，均已修复：

1. **守卫看不到未提交文件**。原实现用 `git ls-files`，只覆盖已跟踪文件，在全新仓库里输出 `lint-boundary: ok (no files)` —— 一个**假通过**。改为 `git ls-files --cached --others --exclude-standard`，覆盖工作区中所有未被 `.gitignore` 排除的文件。
2. **守卫把定义禁令的文件本身判为违规**。`Makefile`（写着禁令）与 `doc/dev-plan.md`（论证禁令）都必须书写 `evaluator` 这个词。排除范围定为 `^doc/` 与 `^Makefile$`；Go 源码、fixtures、README 全部在管辖内 —— 那才是词根写错会造成破坏性变更的地方。

## 4. aimodel v0.5.0 连通性（DeepSeek）

```console
$ export OPENAI_BASE_URL=https://api.deepseek.com
$ export OPENAI_MODEL=deepseek-v4-flash
$ make test-e2e
=== RUN   TestAimodelChatCompletionSmoke
    model deepseek-v4-flash replied: "{\"ok\":true}"
    usage: prompt=13 completion=33 total=46 cache_read=0 reasoning=27
--- PASS
```

三点结论：

1. `provider/openai` 对 DeepSeek 端点可用，`ais.Usage` 正常回填 —— EvalExec 的用量汇总完全建立在这些计数器上。
2. **`deepseek-v4-flash` 是推理模型**：33 个 completion token 里有 27 个是 reasoning token。若按 `02-core-spec.md` §5 只记 `judge_input_tokens` / `judge_output_tokens`，用量汇总会与账单严重对不上。这实测印证了 dev-plan 开放点 #12（新增可选字段 `judge_reasoning_tokens` / `judge_cache_read_tokens`）**必须做**，不是可选优化。
3. 该模型能按 prompt 要求返回严格 JSON，M4 的 `llm_judge` 结构化输出有可行基础。

## 5. 本阶段产出的设计变更

M0 的主要价值不在代码量，而在**用真实 API 校正了 dev-plan §2 的技术前提**。详见 `M0-baseline.md` §2，摘要：

| 变更 | 影响面 |
|---|---|
| 版本基准定为 **v0.5.0**（而非当时最新的 v0.4.1） | v0.4.1 无 provider 注册扩展点，§2.3 的整套方案在其上无法实现 |
| `parameters` 白名单收窄为 **10 个键** | v0.5.0 把 canonical 字段限制为「≥2 provider 共有」，M4 的参数映射范围随之确定 |
| 自定义 provider 的每次运行配置走 `WithProviderOptions` | `ais.Register` **重名 panic**，无法「每次评估注册一个」。影响 M6 |
| 每个 aimodel client 配独立 `*http.Client` | `NewClient` 会改写传入 client 的 `Timeout`，共享会导致超时互相覆盖。影响 M4 |
| `--seed` 不透传确认成立 | v0.5.0 确实移除了 `ChatRequest.Seed` |

以上已全部回写 `doc/dev-plan.md`。

## 6. 遗留到后续阶段

| 项 | 阶段 | 原因 |
|---|---|---|
| `make lint-secrets` 仅有骨架 | M4 | 需要 Judge 与 `logs/` 才有可扫描对象 |
| `make apidiff` 为 advisory | v1.0 | dev-plan §1.3 明确 v0 期间只警告 |
| `check-deps` 白名单需加 JSON Schema 库 | M3 | `json_schema` Grader 引入 `santhosh-tekuri/jsonschema/v6` |
| 其余包目录未创建 | 各自阶段 | 空目录进不了 git，随代码一起建 |
