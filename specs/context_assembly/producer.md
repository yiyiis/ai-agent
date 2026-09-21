# 生产层与写路径规范（进场有界 · 写时 Stub · 拦截置换）

> 所属模块：[上下文组装治理](./README.md)  
> 代码位置：`backend/pkg/tools/tools.go`（capRead / capProc / ArgsOverLimit）、`backend/pkg/agent/stub.go`（全部）、`backend/pkg/agent/agent.go`（saveMessage / ReAct 循环落库点）

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
- `IsErrorResult(text)` 命中（错误前缀）→ **原样返回，不截断不落盘**（失败是证据）；
- ≤3 万字符 → 原样直通；
- 超限 → ①完整输出经 `sandbox.WriteFile` 落 `.tool-outputs/<tool>-<时间戳>-<randHex(6)>.log`（cleanRel 对点目录放行，目录自动创建，三种沙箱驱动一致）；②`Text` = 头 8000 + `\n…[中间省略 %d 字符]…\n` + 尾 4000 + 路径提示行；③`BlobRef` 记录路径。
- 落盘失败 → 降级为纯头尾截断（提示改为"缩小命令输出范围后重跑"），slog 告警，不阻塞。

**链路顺序**：`sanitize`（控制字符清洗 + `maxOutput=256KB` 兜底）在 `formatResult` 内、`capProc` 之前执行——**blob 存的是清洗版（≤256KB），不是进程原始字节**。

### 1.3 参数超限判定

`ArgsOverLimit(raw) = len(raw) > 40KB`（与 `ParseArguments` 同一把尺，导出给 agent 层在落库前使用）。

---

## 2. 写时 Stub 规则（pkg/agent/stub.go）

**按工具类型生成、不按大小**——"要不要生成 stub"与单条大小无关（大小只管工具层截断）；只有天然短小的内容 stub 列为 NULL。

### 2.1 stubForToolCalls —— assistant 批次

结构不变（id/type/function.name 原样），仅替换大载荷工具的 arguments：

| 工具 | stub 版 arguments |
|---|---|
| write_file | `{"_pruned":true,"bytes":N,"lines":M,"note":"内容已省略，可用 read_file 查看","path":"…"}` |
| edit_file | `{"_pruned":true,"note":"改参已省略","path":"…"}` |
| read/bash/python 等 | **原样保留**（参数本来就小） |

幂等守卫：arguments 内含 `_pruned` / `_rejected` 键 → 原样返回。

### 2.2 stubForToolResult —— tool 结果

| 来源 | stub |
|---|---|
| 错误结果（IsErrorResult） | 原文（**列写 NULL**，组装直接用 content） |
| write/edit/export 成功 | 原文（短反馈，NULL） |
| read_file | `[read %s L%d 起，约 %dKB；结果已省略，需要时重新 read_file]`（path/offset 从配对调用参数取） |
| bash / python_exec | `[bash] <命令前 200 字符>\n<headTailLines(content, 3)>`；命令取 `command`/`code` 字段 |
| 其它工具 >512 字符 | `[%s 结果已省略（约 %dKB），需要时重新调用]` |
| 其它工具 ≤512 字符 | 原文（NULL） |

`headTailLines(s, n)`：总行数 ≤ 2n 原样；否则头 n 行 + `\n…（中间省略 %d 行）…\n` + 尾 n 行。  
幂等守卫：内容含"已省略"→ 原样返回。

### 2.3 拦截置换 —— `prepareAssistantCalls(calls, streamInterrupted, finishLength)`

触发条件：流中断 / finish=length / 参数超限（任一）。**逐调用判定**，仅替换满足以下之一的调用：
- `ArgsOverLimit(raw)`（>40KB）；
- `!validCallArgs(raw)`（半截 JSON 等不可回放参数）。

置换为：`{"_rejected":true,"note":"原始参数 %d 字节，超限未执行；请拆成多次小段写入（每次一小段）","path":"…"}`（path 用 `json.Unmarshal` 直接探测保留——超限参数过不了 ParseArguments 长度闸门，但合法超限 JSON 的 path 仍在）。同批正常调用**不受牵连**。

**关键时序**（agent.go ReAct 循环）：
```
streamOneRound → prepareAssistantCalls → persistCalls(stub 版) + rejected
  ├─ assistant 行落库：tool_calls = persistCalls（原文不进任何列）
  │   stub_tool_calls = 同款 · blob_ref = 原文 JSON · ctx_mode = 'stub'
  ├─ 内存 msgs append persistCalls → 原文自此不进任何后续请求
  └─ 执行仍用原始 calls → ParseArguments 产生自纠正错误文本：
      [参数超限] arguments 长度 %d 超过 %dKB 上限。请把大文件拆成多次
      write_file 追加写入（每次一小段），或只写入关键部分。
      → 该文本成为补位的 tool 行 content（IsErrorResult → stub NULL）
```

`finish=length`：同路径置换后 turn 终止（原有行为），下轮回放由 flushDangling 补 `[未执行]` 合成行——**修复了半截 JSON 回放被上游 400 拒绝的存量缺陷**。

### 2.4 blob_ref / ctx_mode 归属

| 字段 | 行 | 条件 | 指向 |
|---|---|---|---|
| blob_ref | tool 行 | bash/python 结果超限 | `.tool-outputs/<tool>-*.log` |
| blob_ref | assistant 行 | 批次含被拒调用 | `.tool-outputs/rejected-<ts>-<sigHash>.json` |
| ctx_mode='stub' | **仅 assistant 行** | 批次含被拒调用 | tool/user 行永不设置 |

被拒 write 的 assistant 行 `tool_calls` 与 `stub_tool_calls` 内容相同（均为 stub）——**有意冗余**：tool_calls 列被每轮 SELECT * 与前端历史接口读取，存原文会把大垃圾放回扫描路径。

### 2.5 token 估算与折叠键

- `tokens_full = len(content + marshaled tool_calls) / 2`、`stub_tokens` 同法——字节/2，中文密度高时偏高估（预算方向偏保守）；
- `tool_target` / `tool_op` 写时提取：文件类工具（write/edit/read/export）取 path + 操作名；bash/python/load_skill 不参与折叠；同批次目标不一致 → NULL（保守不折叠，宁可少合并不可误合并）。

---

## 3. 模板速查

| 场景 | 模板（逐字） |
|---|---|
| write 参数 stub | `{"_pruned":true,"bytes":12345,"lines":320,"note":"内容已省略，可用 read_file 查看","path":"src/a.py"}`（map 序列化按 key 字母序） |
| edit 参数 stub | `{"_pruned":true,"note":"改参已省略","path":"src/a.py"}` |
| 被拒参数 stub | `{"_rejected":true,"note":"原始参数 82000 字节，超限未执行；请拆成多次小段写入（每次一小段）","path":"big.bin"}` |
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
| 参数超限/半截 | `_rejected` stub | `[参数超限]…` | NULL（错误=原文） | `.tool-outputs/rejected-*.json` | 'stub' |
