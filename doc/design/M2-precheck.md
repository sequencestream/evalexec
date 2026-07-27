# M2 参数、前置校验与退出码 — 阶段设计

对应 `doc/dev-plan.md` §4 M2（设计稿 S1 收尾 + S4 前移）。21 条验收标准里有 11 条落在这一层，且后续每个阶段都依赖它。

本阶段结束时，`evalexec` 是一个**只会拒绝、还不会评估**的命令：任何非法输入都得到正确的退出码与诊断，任何合法输入走到「校验通过」就停下。

---

## 1. 交付物与包划分

| 包 | 层 | 职责 |
|---|---|---|
| `cli` | L4 | flag 定义、`--request` 合并、`key=value` 覆盖、规范化 |
| `validate` | L3 | 6 步固定顺序校验，产出结构化错误 |
| `dataset` | L3 | JSONL 流式 reader、两遍扫描的第一遍 |
| `exitcode` | L3 | 错误 → `0/2/3/4/130` 的**唯一**映射点 |
| `redact` | L3 | 请求快照脱敏 |
| `result` | L3 | 临时目录、原子发布、`checksums.sha256` |
| `evalerr` | L3 | 带 `Kind` 的错误模型（**新增包**，见 §2） |

### 1.1 为什么新增 `evalerr` 包

dev-plan §1.5 要求「所有内部错误实现带 `Kind` 的接口，`exitcode` 包做唯一映射」。这个接口不能放 `exitcode` 里 —— 那样 `validate`、`cli`、`result`、`dataset` 全都要 import `exitcode` 才能构造错误，而 `exitcode` 本该是**依赖链末端**（只有它认识退出码）。

定为独立包 `evalerr`：所有包 import 它来**打标**，只有 `exitcode` import 它来**读标**。依赖方向单一。

---

## 2. 错误模型

```go
package evalerr

type Kind int
const (
    KindArgument  Kind = iota + 1 // 参数或结构非法      → 2
    KindPrecheck                  // 前置校验失败        → 2
    KindOutput                    // 输出目录冲突或写失败 → 4
    KindRuntime                   // 运行级故障          → 3
    KindInterrupt                 // 用户中断            → 130
)

type Error struct {
    Kind    Kind
    Step    string // 失败的步骤名，进 stderr 与 f06 的 expected.step
    Message string
    Err     error  // 可选包装
}

func (e *Error) Error() string
func (e *Error) Unwrap() error
func KindOf(err error) (Kind, bool)  // errors.As 查找链上第一个 *Error
```

`KindArgument` 与 `KindPrecheck` 都映射到 `2`，仍分成两个 Kind：诊断信息里要区分「你把命令行写错了」和「你的数据不合法」，而且 f06 的 `step` 字段需要这个区分。

**`Step` 是稳定契约的一部分**：f06 每个子用例的 `expected/failure.json` 都断言它。取值固定为 6 步校验的名字（见 §4）。

---

## 3. `cli`：参数解析与合并

### 3.1 `--grader` 不可重复（验收标准 2、3）

标准库 `flag` 对重复出现的 flag **静默保留最后一个**。必须自定义 `flag.Value` 记录出现次数：

```go
type onceString struct {
    name  string
    value string
    count int
}
func (o *onceString) Set(v string) error {
    o.count++
    if o.count > 1 {
        return fmt.Errorf("--%s may only be given once (got %d)", o.name, o.count)
    }
    o.value = v
    return nil
}
```

`flag.Value.Set` 返回 error 时 `flag` 包会中止解析并报错，正好得到退出码 2。

同样处理 `--eval-id`、`--task-id`、`--dataset`、`--judge-model`、`--output-dir`、`--request` —— 重复给同一个单值参数一律是调用方写错了，静默取最后一个只会掩盖问题。

`--judge-param` / `--grader-param` 显式**允许**重复（它们是 `key=value` 累积型）。

### 3.2 `key=value` 的值按 JSON 标量解析

`03-cli-and-execution.md`：「值覆盖按 JSON 标量解析；复杂值应写入请求或组件文件」。

```text
temperature=0        → float64(0)
temperature=0.5      → float64(0.5)
use_reference=true   → bool(true)
model=judge-v1       → string("judge-v1")   （JSON 解析失败 → 退回字符串）
rubric="a,b"         → string("a,b")        （合法 JSON 字符串）
tags=[1,2]           → 参数错误             （复杂值明确拒绝）
```

规则实现为：先尝试 `json.Unmarshal` 到 `any`；

- 成功且是标量（`bool`/`float64`/`string`/`null`）→ 用该值；
- 成功但是 `[]any`/`map[string]any` → **报参数错误**，不静默接受；
- 失败 → 整体当作字符串（这才让 `model=gpt-4o` 这种最常见的写法免于加引号）。

`=` 只按**第一个**切分，因此 `rubric=a=b` 的值是 `a=b`。

### 3.3 合并顺序与规范化

```text
--request 文件 → CLI flags 覆盖 → --judge-param/--grader-param 覆盖 → 规范化
```

规范化做四件事：

1. `spec_version` 缺省填 `evalexec/v1alpha1`；
2. `dataset.path`、`output_dir` 转**绝对路径**（相对 `--request` 文件所在目录解析，无 `--request` 时相对 CWD —— 这条 dev-plan 未定，本阶段定稿：**相对 `--request` 文件**，因为请求文件里写的相对路径显然是相对它自己）；
3. `execution.concurrency` 缺省 1；
4. `eval_id` 缺省生成 **UUIDv7**（验收标准 5、6）。

**覆盖发生时向 stderr 输出一行提示**（dev-plan 开放点 #10）。

### 3.4 拒绝密钥类参数（验收标准 18）

`03-cli-and-execution.md`：「密钥不得通过 CLI 参数传递」。实现为一条显式黑名单：任何以 `--api-key`、`--token`、`--secret`、`--password` 开头的参数直接报参数错误，并提示改用 `auth.env`。

标准库 `flag` 对未定义 flag 本来就会报错，但默认信息是 `flag provided but not defined`，看不出是**故意不支持**。给出针对性的提示能避免用户以为是拼写问题而去找正确的密钥参数名。

### 3.5 `eval_id` 生成用 UUIDv7

dev-plan 开放点 #6 定稿为 UUIDv7。依赖面守卫要求不引入新依赖，因此**自己实现**：UUIDv7 是 48 位毫秒时间戳 + 4 位版本 + 12 位随机 + 2 位变体 + 62 位随机，`crypto/rand` 足够，约 20 行。

时间与随机源都要可注入（dev-plan §1.5「时间与 ID 可注入，否则黄金文件测试无法稳定比对」）。

---

## 4. `validate`：6 步固定顺序

顺序是**硬约束**，不是实现细节。`03-cli-and-execution.md` §4 明确：「目录冲突与数据集非法同时成立时返回 `4`」。

| # | `Step` 名 | 检查 | Kind → 退出码 |
|---:|---|---|---|
| 1 | `arguments` | 参数与 `EvalRequest` 结构合法、`--grader` 未重复 | argument → **2** |
| 2 | `output_dir_conflict` | 输出目录不存在或为空 | output → **4** |
| 3 | `grader_declaration` | `id`/`version`/`protocol`/`entry`/`requires`/`requires_judge` 齐备且合法 | precheck → **2** |
| 4 | `judge_model` | `requires_judge=true` 时 `judge_model` 存在、可解析、`auth.env` 非空 | precheck → **2** |
| 5 | `dataset_parse` | JSONL 逐行可解析、`case_id` 非空且不重复 | precheck → **2** |
| 6 | `session_requires` | 每条 Session 具备 `requires` 声明的全部字段 | precheck → **2** |

### 4.1 第 2 步必须在第 5 步之前 —— 单独写顺序测试

自然的实现顺序是「先校验输入、再碰输出目录」，那样 f06 的 `output-dir-not-empty`（数据集**也**非法）会返回 2 而不是 4。这是唯一一个「实现得很合理但就是错」的地方，因此：

- f06 的 `output-dir-not-empty` 子用例数据集**故意也是非法的**；
- 另加一个专门的顺序测试，构造「目录冲突 + 数据集非法 + Grader 声明也不全」三重错误，断言退出码为 4。

### 4.2 第 4 步要连 aimodel 客户端一起构造 —— 本阶段的取舍

dev-plan M2 要求「把 `aimodel.NewClient` 放进前置校验」，让 Judge 配置不可用在首次调用前失败。但 `judge` 包要到 M4 才实现。

定稿：**本阶段第 4 步只做不需要 aimodel 的检查**（协议合法、endpoint 按协议必填、`auth.env` 指向的环境变量非空），并在 `validate` 里留一个可选的 `JudgeChecker` 注入点：

```go
type JudgeChecker interface {
    Check(spec *evalspec.JudgeModelSpec) error
}
```

M4 把 `judge.New` 包装成 `JudgeChecker` 注入进来，第 4 步自然升级为「构造真实 client」。本阶段注入 nil 即跳过。

这样 f06 的 `judge-missing-endpoint` 在 M2 就能通过（endpoint 必填是纯结构检查），而「provider 内部校验」推迟到 M4，不阻塞本阶段。

### 4.3 第 5、6 步：数据集扫两遍中的第一遍

`dataset` 包提供流式 reader：

```go
type Reader struct { ... }
func Open(path string) (*Reader, error)
func (r *Reader) Next() (*evalspec.Session, int, error)  // session, sequence, err
func (r *Reader) Close() error
```

第一遍（本阶段）只保留 `case_id` 集合与行数，不缓存 Session 内容 —— 否则大数据集会把内存吃光。第二遍在 M3 的执行阶段。

`case_id` 唯一性索引留一个接口 `CaseIndex`（`Add(id) error` / `Len() int`），本阶段只提供 `MemoryIndex`。dev-plan 提到超过阈值切磁盘索引，**本阶段留接口不实现**。

**空数据集合法**（dev-plan 开放点 #9）：`counts.total = 0`、`status = completed`、`score.*` 全 null。M2 只需保证第 5、6 步不把 0 行当错误。

### 4.4 内置 Grader 的声明是固定的

第 3 步要按下表校验 `builtin` 协议的 `requires` / `requires_judge`，写错即失败：

| `entry` | `requires` | `requires_judge` |
|---|---|---|
| `exact_match` | `["input","output","reference"]` | `false` |
| `contains` | `["input","output","reference"]` | `false` |
| `regex` | `["input","output"]` | `false` |
| `json_schema` | `["input","output"]` | `false` |
| `llm_judge` | `["input","output"]` + 按参数追加 | `true` |

`llm_judge` 的动态 `requires`（dev-plan 开放点 #3）：显式参数 `use_reference: bool` / `use_trajectory: bool` 决定是否追加 `reference` / `trajectory`。**本阶段就实现这个推导**（它只依赖 `parameters`，不依赖 Judge），闭合 dev-plan 留给 M4 的这个 TODO。

推导发生在**第 1 步之后、第 3 步之前**（dev-plan 开放点 #4：`--grader-param` 覆盖可以改变 `requires` 推导结果）。

未知的 `builtin` entry → 第 3 步失败。外部协议（`http-json`/`stdio-jsonl`）的声明来自配置文件，不向外部进程查询，只校验元素合法性。

---

## 5. `exitcode`：唯一映射点

```go
package exitcode

const (
    OK           = 0
    Argument     = 2
    Runtime      = 3
    Output       = 4
    Interrupt    = 130
)

// FromError maps any error to an exit code. An unclassified error is Runtime:
// an unknown failure is a run-level fault, not a silent success.
func FromError(err error) int
```

配一个反向的 `FromResult(*evalspec.EvalResult) int`（M3 用）：`completed` → 0、`cancelled + fail_fast` → 0、`cancelled + interrupt` → 130、`failed` → 3。

**fail-fast 返回 0** 是最容易写错的一条：它是调用方显式请求的停止策略，属于正常收尾。结果不完整由 `status=cancelled` 与 `counts.skipped` 表达，不由退出码表达。

---

## 6. `redact` 与 `result`

### 6.1 `redact`

`judge_model.auth` 在快照中只保留 `{"type":"bearer_env","env":"..."}` —— 即**原样保留**，因为 `Auth` 结构本身就装不下密钥（M1 的设计）。`redact` 包的真正职责是：

1. 深拷贝请求，防止后续修改影响快照；
2. **扫描整个请求**，对任何看起来像密钥的值报错 —— 而不是悄悄抹掉。抹掉会让用户以为密钥被安全处理了，实际上他仍然把密钥写进了配置文件（那个文件还在磁盘上）。定稿：**发现疑似密钥 → 参数错误，拒绝运行**。
3. 产出**规范化 JSON**（key 排序 + 紧凑），供 `eval_request_sha256` 计算。

第 3 点顺带把 M1 遗留的 `eval_request_sha256` 归一化豁免解决掉 —— 序列化规则在本阶段定稿：`encoding/json` + 递归 key 排序 + 无缩进 + `SetEscapeHTML(false)`。

### 6.2 `result`

```text
<output-dir>.tmp-<eval_id>/   ← 写在目标的父目录下，保证同一文件系统
├── result.json
├── records.jsonl
└── checksums.sha256          ← 只覆盖上面两个，不含自身
                              ← errors.jsonl 与 logs/ 不计入（开放点 #5）
```

写完 → `rename` 原子发布。`rename` 要求同一文件系统，所以临时目录必须在**目标父目录**下，不能用 `os.TempDir()`。

**校验失败时不产生任何目录，含临时目录**：因此临时目录的创建必须在 6 步校验**全部通过之后**。有专门测试断言这一点。

本阶段 `result` 包只需交付目录生命周期（创建临时、发布、清理），内容写入在 M3。

---

## 7. `cmd/evalexec` 的形态

```go
func run(args []string, stdout, stderr io.Writer) int {
    req, err := cli.Parse(args, stderr)
    if err != nil { report(stderr, err); return exitcode.FromError(err) }

    if err := validate.All(ctx, req, opts); err != nil {
        report(stderr, err); return exitcode.FromError(err)
    }

    // M3 起：调用 evalexec.Run
    fmt.Fprintln(stderr, "evalexec: pre-checks passed; execution lands in M3")
    return 0
}
```

`os.Exit` 仍只在 `main`。诊断只走 stderr，stdout 保持干净。

---

## 8. 验证方式（本阶段测试）

| 验证项 | 手段 |
|---|---|
| f06 全部 10 个子用例 | 端到端跑真实二进制，断言退出码 + `step` + stderr 子串 |
| **检查顺序** | 三重错误构造，断言 4 而非 2；单独测试不与 f06 混 |
| 校验失败不留目录 | 每个 f06 子用例跑完断言父目录下无 `*.tmp-*`、无 `out/` |
| `--grader` 重复 | `cli` 单元测试 + f06 子用例 |
| `key=value` 解析 | 表驱动：标量 5 种、复杂值拒绝、`a=b=c` 切分 |
| 密钥参数拒绝 | `--api-key` / `--token` 等各一例 |
| 合并优先级 | `--request` + flag 覆盖 + param 覆盖三层，断言最终值与 stderr 提示 |
| `eval_id` 生成 | UUIDv7 格式、版本位、单调性、可注入性；两次调用产生不同 ID（验收标准 20 的一半） |
| 空数据集 | 0 行数据集通过 6 步校验 |
| `llm_judge` 动态 requires | `use_reference`/`use_trajectory` 四种组合 |
| 退出码映射 | `exitcode` 单元测试覆盖全部 Kind + 未分类错误 |
| 原子发布 | `result` 单元测试：发布前目标不存在、发布后临时目录消失 |

**DoD**

- f06 全部子用例通过
- 校验失败时不产生任何结果目录（含临时目录），有测试断言
- 覆盖 dev-plan 列出的 8 种失败场景

**覆盖验收标准**：1、2、3、4、5、6、7、14、15、18、19
