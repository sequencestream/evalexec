# 外部组件契约

本目录是**契约的一部分**，不是可选示范：实现外部 Grader 或 Judge 的人照这些参考实现自测，EvalExec 自己的互操作测试也跑它们。

每个文件**自成一体，重复也不合并** —— 用别的语言实现协议的人读一个文件就够，不必读一张包依赖图。

| 文件 | 角色 |
|---|---|
| `grader-http/main.go` | `protocol=http-json` 的 Grader |
| `grader-stdio/main.go` | `protocol=stdio-jsonl` 的 Grader |
| `grader-stdio.py` | 同上的 **Python 版** |
| `judge-http/main.go` | `protocol=http-json` 的 Judge |
| `judge-stdio/main.go` | `protocol=stdio-jsonl` 的 Judge |

Python 版是「协议优先于 SDK」这条边界唯一能被**证明**的地方。在它之前，所有实现都是 Go 的，"协议不绑定语言"只是一句声明。`TestBuiltinAndExternalGradersAgree` 让同一组 fixture 走四条路径（内置 / http-json / stdio-jsonl Go / stdio-jsonl Python）并要求判决一致。

---

## 三条贯穿所有实现的规则

写外部组件时最容易搞反的三件事，按重要性排序：

### 1. 「不匹配」是成功的评估，记 0 分

比出「两者不同」意味着 Grader 完成了它的工作。这是 `status: "success"`，`score: 0`。

**只有「评不出来」才是 `fail`**：没有可比对的期望值、Judge 拒绝作答、超时、协议错误。

### 2. `fail` 的 `score` 必须是 `null`，不是 `0`

0 是一个测量结果。把「没测到」记成 0 会把一个谁也没量过的数字算进均值里。

宿主会**校验**这一点：一个 `status: "fail"` 却带 `score` 的响应被判 `protocol_error`，**不会**被静默修正 —— 悄悄改正会让实现者永远不知道自己写错了。

### 3. 不确定就拒答，不要猜

Judge 应答里的 `"insufficient_evidence": true` 是一等公民。轨迹为空、证据不足时，返回它比编一个分数有用得多。

---

## Grader 协议

### 请求（两种传输相同）

宿主发送规范化的 grade call，与 `02-core-spec.md` §4 一致：

```json
{
  "eval_id": "eval-01",
  "task_id": "customer-service-v1",
  "case_id": "case-001",
  "input": {...},
  "output": {...},
  "trajectory": [...],
  "reference": {...},
  "context": {...},
  "criteria": {...},
  "metadata": {...},
  "parameters": {...}
}
```

**只有 Session 里实际存在的字段会出现。** 这个区分是有意义的：`"output": null` 表示 Agent 没产出最终输出，而 `output` 键缺失表示数据集不合法（宿主的前置校验会先拦下来，不会走到你这里）。

`parameters` 是 `grader.parameters`（含 `--grader-param` 覆盖后）的原文。**Grader 自己的参数从这里读** —— `entry` 不做 shell 解析，见下。

### 响应（两种传输相同）

```json
{
  "status": "success",
  "score": 1.0,
  "label": "match",
  "reason": "为什么",
  "evidence": [{"source": "output", "path": "$.messages[0].content", "value": "..."}],
  "usage": {"judge_input_tokens": 0, "judge_output_tokens": 0},
  "error": null
}
```

`fail` 的形状：

```json
{
  "status": "fail",
  "score": null,
  "label": null,
  "reason": "为什么评不出来",
  "evidence": [],
  "usage": {"judge_input_tokens": 640, "judge_output_tokens": 32},
  "error": {"code": "insufficient_evidence", "message": "细节"}
}
```

`error.code` 取 `insufficient_evidence` / `judge_error` / `timeout` / `protocol_error` / `internal_error`。

**`usage` 在 `fail` 时照常填。** 失败的评估花掉的 token 确实花掉了，丢掉它会让汇总与账单对不上。

### `http-json`

`grader.entry` 是端点 URL。宿主 `POST` 上述 JSON，期待 `200` 与上述响应。

| 宿主看到 | 记为 |
|---|---|
| 非 2xx | `protocol_error` |
| 响应不是合法 `Evaluation` | `protocol_error` |
| 响应违反不变量（如 `fail` 带 `score`） | `protocol_error` |
| 合法的 `status: "fail"` | **原样采用** —— 你有权说评不出来 |
| 连不上 | `protocol_error` |
| 超过 `grader.timeout_ms` | `timeout` |

**错误响应体不会进 `result.json`** —— 它可能把整个 call 回显出来。原文只进 `logs/` 与 `errors.jsonl`。

### `stdio-jsonl`

`grader.entry` 是**单个可执行文件路径**。

**不做 shell 解析。** 引号、转义与注入问题一并免除，而这里根本不需要它：要传参数就从 `parameters` 读，要更复杂就写个包装脚本。宿主在前置校验期就检查文件存在且可执行 —— 配置写错应当在跑第一个样本之前失败。

```text
stdin   一行一个 JSON grade call
stdout  一行一个 JSON evaluation
stderr  仅诊断，宿主收进 logs/grader-<case_id>.log
```

四条必须做对的事：

1. **每行都要 flush。** 缓冲输出会让宿主等一个躺在你进程内存里的答案，直到超时。
2. **stdout 只放答案。** 宿主只读 stdout，诊断混进去就变成了协议错误。
3. **无论如何都要应答。** 连 call 解不开也要回一行 `protocol_error` —— 沉默会让宿主挂到超时，而不是立刻失败。
4. **stderr 可以随便写。** 宿主持续读取它，不会因为管道缓冲填满而死锁。但内容会被截取尾部（64 KB），因为崩溃总是在末尾自报家门。

**进程数 = `--concurrency`。** 协议是一问一答，共享一个进程会让对话交错、把一个样本的判决记到另一个样本头上。所以每个 worker 一个进程 —— 这一点明写出来，因为要承受它的是调用方的机器。

超时或崩溃后，宿主 **kill 进程组**而非进程：脚本自己 fork 出的子进程（Python 包装、shell 管道）否则会留下孤儿，而一个还握着管道的孤儿与「尚未应答的进程」无法区分。被 kill 的进程不再复用 —— kill 之后无从知道管道里是否还留着一个未读的答案。

---

## Judge 协议

比任何厂商的 Chat Completions 都简单：**一条回复、扁平 usage**。目的是让别的语言容易实现，不是兼容某个服务。

### 请求

```json
{
  "model": "judge-model",
  "messages": [{"role": "system", "content": "..."}, {"role": "user", "content": "..."}],
  "temperature": 0,
  "max_completion_tokens": 512,
  "response_format": {...}
}
```

采样参数**只在配置了的时候出现**，所以你只需处理实际收到的字段。

`user` 消息里的 Session 字段用**标签包裹**而非 JSON 嵌套：

```text
<rubric>
判断最终回答是否忠实于轨迹中工具返回的事实
</rubric>

<input>
{"messages":[...]}
</input>

<output>
{"messages":[...]}
</output>

<trajectory>
[{"sequence":1,...}]
</trajectory>
```

标签而非嵌套的理由有两个：省 token，且一个内容里恰好含 JSON 的 Session 不会被误认成包着它的信封。

`<trajectory>` 与 `<reference>` 只在 `llm_judge` 的 `use_trajectory` / `use_reference` 打开时出现。

### 响应

```json
{
  "content": "<模型的回复文本>",
  "usage": {"input_tokens": 850, "output_tokens": 80,
            "cache_read_tokens": 0, "reasoning_tokens": 0}
}
```

`usage` 的字段名与 `EvalResult.usage.judge_model` 一致（`input_tokens` 而非 `prompt_tokens`），少一层心智翻译。

**`reasoning_tokens` 请务必填。** 推理模型的思考 token 常常多于可见输出 —— 实测 DeepSeek 有 70% 的输出 token 是思考 token，不单列会让用量与账单严重对不上。

失败时：

```json
{"error": {"code": "rate_limited", "message": "细节"}}
```

宿主记为 `judge_error`。**它不会重试** —— 429 与 5xx 都直接计为失败。需要重试请由上层重跑整个评估。

### `content` 里应该放什么

`llm_judge` 期望一个 JSON 对象：

```json
{"score": 1, "label": "faithful", "reason": "...",
 "insufficient_evidence": false,
 "evidence": [{"source": "trajectory", "path": "$[0].result.status", "value": "shipping"}]}
```

宿主的解析是容错的：会剥掉 ```` ```json ```` 代码围栏与前后的解释文字。但**不修坏 JSON** —— 单引号、尾随逗号不会被修正。那是模型没遵守约定，静默修好会把一个需要改 prompt 的 Judge 藏在看起来正常的结果后面。

`score` 与 `label` 至少给一个。两个都没有 → `protocol_error`。

`score` **原样记录**。`min_score` / `max_score` 是量表元数据，宿主既不 clamp 也不拒绝越界值 —— 解释分数不是它的事。

### `stdio-jsonl`

payload 与 `http-json` **逐字节相同**，一行请求一行响应。四条注意事项与 Grader 的 stdio 完全一致（每行 flush、stdout 只放答案、无论如何都应答、stderr 随意）。

---

## 自测

用你的实现跑 EvalExec 的 fixtures：

```bash
# HTTP Grader
./your-grader --addr 127.0.0.1:8080 &
evalexec --task-id selftest \
  --dataset fixtures/data/f01-exact-match-all-pass/dataset.jsonl \
  --grader your-grader.json \
  --output-dir ./out
```

`your-grader.json`：

```json
{
  "id": "my-grader", "version": "v1",
  "protocol": "http-json",
  "entry": "http://127.0.0.1:8080/grade",
  "requires": ["input", "output", "reference"],
  "requires_judge": false
}
```

然后把 `out/records.jsonl` 与 `fixtures/data/f01-exact-match-all-pass/expected/records.jsonl` 比一比。

**比什么**：`status` / `score` / `label` / `error.code`。
**不比什么**：`reason` 与 `evidence` —— 措辞不同不代表协议不兼容。

`requires` / `requires_judge` 来自你的**配置文件**，宿主不会向你的进程查询。这是有意的：前置校验若要先联系它本该先验证的东西，就不成为前置校验了。所以一个连不上的 Grader 会产生一堆 `protocol_error`，而不是一次干净的前置失败 —— 这也是为什么 `requires` 要如实填写。
