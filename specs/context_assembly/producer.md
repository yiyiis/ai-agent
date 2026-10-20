# 生产层与写路径规范（结果有界 · 原始参数持久化 · 写时 Stub）

> 所属模块：[上下文组装治理](./README.md)  
> 代码位置：`backend/pkg/tools/tools.go`（capRead / capProc / ArgsNeedStub）、`backend/pkg/agent/stub.go`（全部）、`backend/pkg/agent/agent.go`（saveMessage / ReAct 循环落库点）

---

## 1. 工具层进场有界（pkg/tools）

职责：在结果**变成消息之前**限制单条体积。落库口径与发模型口径是同一个 `out.Text`，因此这里同时保护两者。

### 1.1 read_file —— `capRead(text, path, paged)`

| 参数 | 值 | 说明 |
|---|---|---|
| `maxReadLines` | 2000 | 单次最多返回行数 |
| `maxLineChars` | 2000 | 单行超长截断阈值（**rune/字符**计数） |

行为：
- 超行数：返回前 2000 行 + 尾注 `…（%s 共 %d 行，已显示前 %d 行。%s。）`；`paged=false`（模型没传 offset/limit）时提示引导分段读/edit_file，`paged=true` 时提示"继续用更大的 offset 往后读"。
- 单行超 2000 字符：截断加行尾 `…[单行超长已截断]`，尾部另加一行总提示。字节预检优化：`len(ln) <= maxLineChars` 直接跳过（字节不超字符必不超）；中文场景字节超但字符未超的保留原样。
- read_file **不产生 blob**——文件本体在沙箱，天然是原文。

### 1.2 bash / python —— `capProc(ctx, sessionID, tool, text)`

| 参数 | 值 | 说明 |
|---|---|---|
| `maxProcChars` | 30000 | 喂给 LLM 的输出上限（**rune** 计数） |
| `procHeadChars` / `procTailChars` | 8000 / 4000 | 超限时保留的头/尾 |

行为：
- `IsErrorResult(text)` 命中（错误前缀）→ **原样返回，不截断不落盘不归档**（失败是证据）；
- ≤3 万字符 → 原样直通；
- 超限 → 完整输出**双写**：①`sandbox.WriteFile` 落 `.tool-outputs/<tool>-<时间戳>-<randHex(6)>.log`（模型当场可 read_file；cleanRel 对点目录放行，目录自动创建，三种沙箱驱动一致）；②`storage.PutArchive` 归档对象存储（Bucket 空自动落本地 uploads/，`ArchiveURL` 携带回捞 URL）。③`Text` = 头 8000 + `\n…[中间省略 %d 字符]…\n` + 尾 4000 + 路径提示行。
- 沙箱落盘 / 对象归档**各自失败均降级继续**（warn 不阻塞对话）；两者都失败时提示换为"缩小命令输出范围后重跑"。

**持久化闭环（v1.0.1）**：`blob_ref` 列存 `{"path":".tool-outputs/…","url":"归档URL"}` JSON——path 是模型侧身份（content/stub 里的路径文本），url 是沙箱回收后的重物化凭据（`materializeBlobs` 每轮检查，文件缺失才按 URL 取回写回沙箱，与 `materializeAttachments` 同款生命周期兜底；v1.0.0 裸路径行兼容读取但无从恢复）。**模型永远只感知工作区路径，归档 URL 不进任何请求。**

**链路顺序**：`sanitize`（控制字符清洗 + `maxOutput=256KB` 兜底）在 `formatResult` 内、`capProc` 之前执行——**blob 存的是清洗版（≤256KB），不是进程原始字节**。

### 1.3 参数回放阈值与执行校验

`ArgsNeedStub(raw) = len(raw) > 40KiB`，仅决定上下文是否强制精简，不拒绝存储或执行。`ParseArguments` 只校验完整 JSON 对象，不再限制字节数；空串、半截 JSON、数组、null 等不视为参数对象，不执行工具。本地工具与 MCP 均使用完整解析结果。

解析使用 `json.Number` 保留数字原值，避免 MCP 的大整数 ID 经 float64 转换后丢失精度；读取行号、超时等本地整数参数兼容此类型。对象后有额外 JSON 或文本也判为非法。

---

## 2. 写时 Stub 规则（pkg/agent/stub.go）

write/edit 按工具类型生成参数 stub；其他工具（含 MCP）参数超过 40KiB 时生成通用摘要。结果 stub 的规则保持不变；与原结果相同的 stub 不重复存储，列为 NULL。

### 2.1 stubForToolCalls —— assistant 批次

结构不变（id/type/function.name 原样），仅替换大载荷工具的 arguments：

| 工具 | stub 版 arguments |
|---|---|
| write_file | `{"_pruned":true,"bytes":N,"lines":M,"note":"内容已省略，可用 read_file 查看","path":"…"}` |
| edit_file | `{"_pruned":true,"note":"改参已省略","path":"…"}` |
| 其他工具（含 MCP）参数 ≤40KiB | 原样保留 |
| 其他工具（含 MCP）参数 >40KiB | `_pruned` + 原字节数 + 字段数 + 字段摘要；最多 16 个字段，每个字段名最多 64 字符，记录类型和大小，不复制嵌套载荷 |
| 非法/不完整 JSON | `_rejected` + 准确的解析失败说明；仅影响回放副本 |

幂等守卫：小于等于阈值、带 `_pruned:true` / `_rejected:true` 的摘要保持原样。原始大参数不能凭同名字段绕过精简。write/edit 的路径摘要最多 1024 字符。

强制 stub 的批次同时归档完整调用数组，摘要携带 `_source` 工作区路径。文件名为调用数组 JSON 的 SHA-256，落库、历史回放与轮内重建都能确定性地指向同一份原文。

### 2.2 stubForToolResult —— tool 结果

| 来源 | stub |
|---|---|
| 错误结果（IsErrorResult） | 原文（**列写 NULL**，组装直接用 content） |
| write/edit/export 成功 | 原文（短反馈，NULL） |
| read_file | `[read %s L%d 起，约 %dKB；结果已省略，需要时重新 read_file]`（path/offset 从配对调用参数取） |
| bash / python_exec | `[bash] <命令前 200 字符>\n<headTailLines(content, 3)>`；命令取 `command`/`code` 字段 |
| 其它工具 >512 字节 | `[%s 结果已省略（约 %dKB），需要时重新调用]` |
| 其它工具 ≤512 字节 | 原文（NULL） |

`headTailLines(s, n)`：总行数 ≤ 2n 原样；否则头 n 行 + `\n…（中间省略 %d 行）…\n` + 尾 n 行。进程超限结果的 stub 尾 3 行必然携带预览版末行的 blob 路径提示——**冷区模型仍可 read_file 取回**。  
幂等守卫（`isResultStub`）：按 stub 固定前缀精确判定（`[read_file ` / `[bash] ` / `[python_exec] ` / 含"结果已省略"），**不可用"已省略"模糊匹配**——工具层预览自带的"…[中间省略 N 字符]…"含同样字样，会把超限结果的 stub 生成误拦（v1.0.0 修复的缺陷，有回归用例钉住）。

### 2.3 存储、执行、回放分离

`callsNeedStub` 检查批次内是否存在 >40KiB 或非法 JSON 参数。命中时设置 `ctx_mode='stub'`，但不修改原始调用。

```text
streamOneRound → 原始 calls
  ├─ assistantExtras：生成 stub_tool_calls，估算两份 token，必要时归档
  ├─ saveMessage：tool_calls 始终保存原文（包括半截 JSON 字符串）
  ├─ assistantContextMessage：ctx_mode=stub 时立即向内存 msgs 追加精简副本
  └─ 执行：对原始参数调用 ParseArguments
      ├─ 完整合法：交给本地工具或 MCP，体积不阻止执行
      └─ 非法/不完整：返回 [参数解析失败]，不调用工具
```

`_rejected` 是回放说明，实际执行依据原始参数的解析结果。流异常、取消或 `finish=length` 时仍结束本轮且不执行该批调用，已生成的调用原文全部保存；下轮回放由 flushDangling 补 `[未执行]`。

### 2.4 blob_ref / ctx_mode 归属

| 字段 | 行 | 条件 | 值 |
|---|---|---|---|
| blob_ref | tool 行 | bash/python 结果超限 | `{"path":".tool-outputs/<tool>-*.log","url":…}` |
| blob_ref | assistant 行 | 批次含大参数或非法参数 | `{"path":".tool-outputs/arguments-<sha256>.json","url":…}`（`writeCallBlob` 双写） |
| blob_ref | read_file 结果 | **永远没有** | 文件本体在沙箱，无需另存 |
| ctx_mode='stub' | assistant 行 | 批次含大参数或非法参数 | 正常执行与强制精简回放相互独立 |

`tool_calls` 保存原始参数，`stub_tool_calls` 保存回放副本。数据库与前端历史可以查看原文；模型请求只使用符合上下文策略的版本。`ctx_mode` 随内存消息保留，但不序列化到模型协议。

### 2.5 token 估算与折叠键

- `tokens_full = len(content + marshaled tool_calls) / 2`、`stub_tokens` 同法——字节/2，中文密度高时偏高估（预算方向偏保守）；
- `tool_target` / `tool_op` 写时提取：文件类工具（write/edit/read/export）取 path + 操作名；bash/python/load_skill 不参与折叠；同批次目标不一致 → NULL（保守不折叠，宁可少合并不可误合并）。

---

## 3. 模板速查

| 场景 | 模板（逐字） |
|---|---|
| write 参数 stub | `{"_pruned":true,"bytes":12345,"lines":320,"note":"内容已省略，可用 read_file 查看","path":"src/a.py"}`（map 序列化按 key 字母序） |
| edit 参数 stub | `{"_pruned":true,"note":"改参已省略","path":"src/a.py"}` |
| 非法参数 stub | `{"_rejected":true,"note":"原始参数 N 字节，JSON 非法或不完整，未执行；原文已保留，请重新输出完整 JSON 对象"}`（批次强制精简时另带 `_source`） |
| read 结果 stub | `[read src/main.go L1 起，约 61KB；结果已省略，需要时重新 read_file]` |
| bash 结果 stub | `[bash] <命令>␊<头3行>␊…（中间省略 N 行）…␊<尾3行>` |
| 未知工具大结果 | `[some_tool 结果已省略（约 NKB），需要时重新调用]` |
| read 截断提示 | `…（%s 共 %d 行，已显示前 %d 行。%s。）` |
| 单行截断标记 | 行尾 `…[单行超长已截断]` + 尾注 `…（存在超长单行，已按 2000 字符截断显示。）` |
| 进程超限 content | 头8000 + `\n…[中间省略 %d 字符]…\n` + 尾4000 + `\n[输出超长，完整结果已写入工作区 %s，可 read_file 配合 offset 分段查看]` |
| 落盘失败降级 | `\n[输出超长已截断；如需完整输出请缩小命令输出范围后重跑]` |

## 4. 组合矩阵（bash/python 为例）

| 组合 | tool_calls 列 | content | stub 列 | blob_ref | ctx_mode |
|---|---|---|---|---|---|
| 参数小、结果小 | 原文 | formatResult 全文 | `[bash] 命令+头尾3行` | 无 | '' |
| 参数小、结果超限 | 原文 | 预览版 | 预览版头尾 3 行（尾含路径提示行） | `.tool-outputs/<tool>-*.log` | '' |
| 参数 >40KiB 且合法 | 原文 | 正常执行结果 | 参数通用摘要；结果按原规则 | assistant 行参数归档 | 'stub'（assistant 行） |
| 参数非法/半截 | 原文 | `[参数解析失败]…`；响应截断/流异常时不执行该批 | 参数 `_rejected`；错误结果 NULL | assistant 行参数归档 | 'stub'（assistant 行） |
