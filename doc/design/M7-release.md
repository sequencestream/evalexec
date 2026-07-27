# M7 一致性验证与双形态发布 — 阶段设计

对应 `doc/dev-plan.md` §4 M7。收口阶段：证明 21 条验收标准都有自动化测试，证明协议不绑定 Go，并让二进制与库同时可用。

---

## 1. 交付物

| 项 | 说明 |
|---|---|
| `contract/verify_fixtures.py` | Python 协议校验脚本（~150 行），读 fixtures 的期望结果并断言恒等式与字段语义 |
| `doc/acceptance.md` | 21 条验收标准 → 具体测试用例的覆盖表 |
| `examples/consumer/` | 下游冒烟仓库（独立 go.mod），证明库确实可被 import |
| `.goreleaser.yaml` | 多平台二进制 + checksums |
| `README.md` | 分两栏：CLI 用法与库用法；库那栏三段可运行示例 |
| `errors.jsonl` | M6 遗留 |
| `CHANGELOG.md` | L3 包变更走它 |

---

## 2. Python 协议校验脚本：与 M6 的 Python Grader不是一回事

M6 的 `grader-stdio.py` 是**被评组件**：它接收 call、返回 evaluation。

本阶段的 `verify_fixtures.py` 是**协议校验器**：它读 `fixtures/data/*/expected/result.json` 与 `records.jsonl`，独立地断言：

1. 计数恒等式全部成立；
2. `records.jsonl` 行数 == `counts.total` == 数据集行数；
3. 每条 record 的 `eval_id` 与 result 一致；
4. `fail` 的 `score` 恒为 `null`，且不进 `score.count`；
5. `skipped` 的 `evaluation` 恒为 `null`、时间字段恒为 `null`、`error.code == "skipped"`;
6. `score.count == 0` ⟺ `mean`/`min`/`max` 全为 `null`；
7. `status=completed` ⟺ `skipped == 0`；`status=cancelled` ⟹ `skipped > 0` 且有 `stop_reason`；
8. `fail == sum(fail_by_code)`，且 `fail_by_code` 不含 `skipped` 键。

**它必须用纯标准库**，否则"协议不绑定语言"这个论证就要附加"只要你装得上这些包"的脚注。

**它必须能失败。** 一个从未报过错的校验器与一个失效的校验器无法区分 —— 所以脚本自带 `--self-test`，构造若干违反恒等式的文档并断言校验器拒绝它们。这与 M4 给密钥扫描器加自测是同一个道理。

CI 里跑它。

---

## 3. 验收标准覆盖表

`doc/acceptance.md`：21 行，每行给出**具体测试函数名**，而不是"由 M3 覆盖"这种指向阶段的说法 —— 后者在测试被重命名或删除后不会失效，等于没有约束力。

写这张表时预计会发现覆盖缺口。已知的两处：

- **标准 4**（`task_id` 只校验非空并原样输出）：目前只有间接覆盖；
- **标准 5**（`eval_id` 全局唯一）：`eval_id` 的唯一性在调用方提供时不校验（开放点 #6 定稿），表里要如实写明这一点而不是假装覆盖了。

发现缺口就补测试，而不是把表写得好看。

---

## 4. 下游冒烟仓库

`examples/consumer/`，**独立 go.mod**，用 `replace` 指向父目录。

它要跑通 dev-plan 要求的三段：

1. `evalexec.Run` 完成一次评估；
2. 实现并注册一个自定义 Grader；
3. 用 `fixtures.FS` 自测该 Grader。

**为什么必须独立 go.mod**：同一模块内的 `examples/` 只能证明代码编译，证明不了「从外部 import 时该导出的都导出了」。一个被遗忘在小写标识符里的必需类型，只有跨模块编译才会暴露。

CI 里 `cd examples/consumer && go test ./...`。

`check-deps` 要跳过它 —— 它有自己的 go.mod。

---

## 5. `errors.jsonl`（M6 遗留）

M6 推迟的理由是"没有真实使用反馈时难判断该汇总什么"。本阶段的决定：**实现一个最小版本**，只记那些**当前完全没有落点**的运行级事件：

| 事件 | 当前落点 | 是否进 errors.jsonl |
|---|---|---|
| 子进程崩溃 | `evaluation.error.message` + `logs/` | 否 —— 已有落点 |
| 连接失败 | `evaluation.error.message` | 否 |
| 日志写入失败 | 仅 stderr（库模式下 `io.Discard`，等于丢失） | **是** |
| 补写期间的读取失败 | 运行级错误，但只在 error 里 | **是** |
| 数据集在运行中被改动 | 同上 | **是** |

即：**只记那些否则会消失的东西**。这让文件小、有明确用途，且不与已有落点重复。

不计入 `checksums.sha256`（开放点 #5）。`artifacts.errors` 仅在有内容时出现。

---

## 6. 发布

### 6.1 二进制

`.goreleaser.yaml`：linux/darwin/windows × amd64/arm64，注入 `version.*`。

**不在本阶段实际发布** —— 打 tag 与推 release 是仓库所有者的决定，不是开发阶段的动作。交付的是可用的配置 + `goreleaser check` 通过。

### 6.2 库

README 分两栏。库那栏的三段示例必须是**可运行的**，因此它们就是 `examples/consumer/` 里的代码，README 引用而非复制 —— 复制的示例会腐烂。

### 6.3 版本与兼容性声明

- `v1alpha1` 内只增可选字段；
- 破坏性变更提版本号；
- Go API 与协议版本的对应关系进 README；
- §1.3 的稳定性分层表已在 README。

`gorelease` 目前是 advisory（v0）。本阶段确认它能跑通并输出报告。

### 6.4 aimodel 升级检查清单

`doc/aimodel-upgrade.md`：因为 `judge` 是唯一 import aimodel 的包，升级只需检查一个包。清单列出 M0 核对过的每个假设，供下次升级逐条重验 —— v0.4.1 → v0.5.0 的结构性重构说明这不是过度谨慎。

---

## 7. 验证方式

| 验证项 | 手段 |
|---|---|
| Python 校验脚本通过全部 golden fixtures | `python3 contract/verify_fixtures.py fixtures/data` |
| 校验脚本确实会失败 | `--self-test` |
| 21 条标准逐条有测试 | `doc/acceptance.md` 人工核对 + 补齐缺口 |
| 下游冒烟仓库编译并通过 | `cd examples/consumer && go test ./...` |
| `goreleaser check` 通过 | 需要时安装 |
| `gorelease` 能输出报告 | advisory |
| CI 覆盖以上全部 | `.github/workflows/ci.yml` |

**DoD**：21 条验收标准全部有对应自动化测试；下游冒烟仓库能编译运行；`v0.1.0` 二进制与库同时可用。
