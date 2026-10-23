# 阻塞式 request_input

## 范围

第一版支持交互聊天中的模型主动提问，不新增表或数据库列。
工具审批和 MCP 的 `input_required` / elicitation 继续协议不在本次范围内。
无头 `run_*` 会话和 API key 执行不暴露这个工具，避免无人值守任务永久等待。

工具参数：

```json
{"question":"需要导出什么格式？","options":["PDF","Word"]}
```

`question` 必填，1–4000 字；`options` 可省略，最多 8 项，每项 1–500 字。
用户始终可以自由输入。回答最大 16KB。等待没有自动超时。

## 消息与检查点

- assistant.tool_calls：保存完整原始问题参数。
- role=tool、name=request_input、tool_call_id：保存用户答案或取消结果。
- `{"status":"answered","answer":"PDF"}`：回答后继续。
- `{"status":"cancelled","action":"skip"}`：跳过问题，模型知道没有得到答案。
- `{"status":"cancelled","action":"stop"}`：停止任务；同一事务补一条 assistant 停止说明。

最新 assistant 批次中有合法 request_input，且没有对应结果，即待回答。
如果答案已保存但尚无下一条 assistant，仍然是可以恢复的执行检查点。
后续 assistant/user 消息关闭该检查点。
问题不会被历史修复补成“调用中断”，也不会被悬空轮清理删除。
恢复入口在所有结果齐全后才组装并发送模型请求。

要求模型单独调用 request_input。若混入普通工具，同批其他调用先保存
“未执行，回答后请重新调用”结果，再等待问题完成。这些调用不会产生副作用，
恢复也不会重放。一个批次包含多个合法 request_input 时，等待全部回答或跳过。

## HTTP / SSE

回答接口：

```text
POST /api/sessions/:id/messages/:mid/tool-calls/:cid/answer
{"action":"answer","answer":"PDF"}

POST /api/sessions/:id/messages/:mid/tool-calls/:cid/answer
{"action":"skip"}
```

接口采用现有会话写权限校验。在事务中锁 sessions 行，再检查活动问题和已有结果。
答案的 message_id 根据 session ID、assistant message ID、call ID 确定性生成，
复用 messages.message_id 唯一索引兜底。
相同答案重试返回已有结果；不同答案、已经停止的请求返回业务错误。
原始 call ID 在不同 assistant 中复用时，不会串用答案。

停止仍使用 `POST /api/sessions/:id/messages/stop`。
停止结果及终止消息原子落库后取消后台 Turn。

`GET /api/sessions/:id` 返回 `pending_inputs`，每项含
`message_id`、`tool_call_id`、`question`、`options`，用于恢复卡片。

新 SSE 事件：

```json
{"type":"input_required","request":{"message_id":"...","tool_call_id":"...","question":"...","options":["PDF"]}}
{"type":"stream_reset"}
```

`input_required` 是等待状态，不是 error/done；SSE 保持订阅及心跳。
`stream_reset` 表示重启恢复的事件流重新开始，前端清空流式过程再重放。
所有事件携带 `stream_id`，重连同时提交 `stream_id` 和 `from_seq`；
服务端实例重建导致标识改变时从 0 重播，避免多个页面沿用旧游标而错过答案。
结果继续走 tool_call_result，后续模型输出继续走 delta。

## 活动执行与服务重启

正常情况：原 Run 保留 msgs，等待数据库答案。回答先提交事务再通过 channel
唤醒原协程；每 2 秒查询是通知丢失的兜底。通道只传唤醒信号，不保存答案。
全部问题解决后直接把结果加入原 msgs，继续原 Run。

刷新只重连现有 Turn，不中止等待。
服务重启后，在有写权限的用户重连事件流或提交回答时，惰性创建恢复执行者；
无需重新发送原问题。恢复会重放本轮已保存过程、等待尚未回答的问题，
然后重新组装历史进入公共执行循环，不新增 user 消息。

恢复保留已生成模型轮数、Loop Guard 历史及可查询的累计 token 用量，
不会因为回答或重启把工具轮数预算清零（当前上限 64 轮）。达到上限保存终止说明，防止反复恢复。
上下文仍遵守 ctx_mode；用户答案不会被通用工具结果 stub 省略。

沿用项目现有单实例 TurnRegistry：同一进程只允许一个会话执行者。
数据库可防多个页面的重复答案，但不等于多服务实例的分布式执行锁。
多副本部署仍需要共享执行租约/任务队列，不能依靠本次回答防重保证执行唯一性。

## 验证

后端离线 GORM 事务 fixture 覆盖并发重试、冲突答案、权限、停止、混合批次、
原 Run 唤醒、待回答/已回答重启恢复、轮数上限和冷区答案保留。
fixture 不连接真实 MySQL，生产 MySQL 的事务锁仍建议在部署环境做集成验证。
前端测试覆盖问题解析、取消语义和 SSE 重置；本地模拟 API 页面验证刷新、
选项提交、跳过、停止与卡片状态。未操作生产会话。
