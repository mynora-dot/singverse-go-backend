# 管理员操作助手

Go 是权威实现，接口前缀 `/api/v1/ai/assistant`。仅管理端可访问，沿用管理员 Cookie、同源与 CSRF 校验，响应 `Cache-Control: no-store`。本模块没有任意 HTTP、SQL 或脚本执行工具。

## 接口

| 方法 | 路径（省略前缀） | 请求与结果 |
| --- | --- | --- |
| GET | `/status` | `ready/message/model/toolCount/settingsPage`；未绑定模型返回配置引导 |
| GET | `/tools` | 工具数组；含 `name/title/group/description/write/page/inputSchema`，无参数工具也声明对象 Schema |
| GET | `/conversations` | 最近更新的 200 个会话；历史不自动清理 |
| POST | `/conversations` | `{title}`，最多 100 字；返回会话 |
| GET | `/conversations/{id}` | `conversation/messages/runs/operations`，含持久化确认与执行回执 |
| DELETE | `/conversations/{id}` | 204；活动运行或仍在执行的写调用返回 409；只删除助手历史 |
| POST | `/conversations/{id}/messages` | `{requestId,content}`；返回新运行或同一提交回执；消息最多 8000 字 |
| GET | `/runs/{id}` | 运行状态及 `lastSequence` |
| POST | `/runs/{id}/stop` | 204；停止回复、拒绝尚未接受的写操作；不取消已接受的业务任务 |
| GET | `/runs/{id}/events` | SSE；用 `Last-Event-ID` 或 `after`（非负整数）续读，前者优先 |
| POST | `/runs/{id}/operations/{operationId}/decision` | `{requestId,revision,approve}`；返回操作回执 |
| GET | `/runs/{id}/operations/{operationId}/reconcile` | 只读业务核对；不能自动修改不确定回执或重新执行 |

数据结构以 `openapi/singverse.openapi.json` 为准。提交与决定的 `requestId` 为 8～80 字；客户端生成 UUID，在网络重试时复用。相同提交标识配不同消息、相同决定标识配不同选择、过期修订或竞争状态返回 409。格式错误返回 400；Cookie/同源/CSRF 拒绝沿用认证契约。

SSE `id` 为运行内递增序号，`event: message` 的 JSON 是 `{runId,sequence,kind,data,createdAt}`。按序号去重，断线携带最新游标。事件包括 `text_delta`、`tool_started`、`tool_result`、`approval_required`、`approval_decided`、`operation_result`、`run_status`。连接约两分钟后结束，运行中应续连；15 秒心跳为 SSE 注释。等待确认及终态会在已有事件发完后关闭连接。

## 确认与恢复

每会话最多一个活动运行。运行状态为 `RUNNING/WAITING/COMPLETED/FAILED/STOPPED/INTERRUPTED`。写操作状态为 `PENDING/APPROVED/REJECTED/STALE/EXECUTING/SUCCEEDED/FAILED/UNCERTAIN`。

写工具先验证参数并只读解析目标，保存参数、完整预览和修订，通过 Eino 中断及检查点等待确认。聊天文本中的“确认”不能批准操作。服务端确认时和执行前均重新解析；目标或内容变化后作废旧操作。批量下载、失败重试和音频处理按一次批次确认，执行结果保留逐项失败。旧音频 HTTP `preview` 仍准备业务批次，Agent 使用独立只读 `PreviewAudioBatch`。

执行前以数据库条件更新领取操作；已接受的业务调用最多运行 90 秒，停止对话不取消它。操作 ID 用作歌单、下载和音频创建的业务回执 ID；重复请求读取已存在回执。重启后保存等待确认及检查点，普通活动运行标记 `INTERRUPTED`，执行中的操作标记 `UNCERTAIN`，不自动重放。相同会话中同工具、同参数仍有不确定写回执时，拒绝再次提交。只读核对提供证据，需管理员在业务页面处理，不能证明缺失回执一定未执行。

`assistant_conversation/run/message/event/operation/checkpoint` 由 SQLite/MySQL 增量迁移 `202611160001` 创建。历史保留到手动删除，列表当前显示最近 200 条。等待确认可在重启后继续；删除历史不回滚业务副作用。

## 工具与模型

覆盖清单 `assistant-tool-coverage.json` 包含 35 个查询、13 个确认写入和 17 个表单导航。FORM 只返回页面入口和说明。密码、MFA、密钥、配对、文件和音源脚本通过原表单处理。部分复杂编辑和审核也通过表单完成。

Eino ADK 固定 v0.9.23，使用原生 AgenticMessage、Runner、ToolSearch、流式事件和中断恢复。模型请求最多暴露 9 个工具，单次运行最多 24 轮、10 分钟。模型查询结果和上下文有窗口限制，不改变完整历史持久化保留。

固定官方适配器 `agenticopenai v0.2.4`、`agenticclaude v0.1.7`、`agenticgemini v0.2.5`，对应 CHAT_COMPLETIONS、RESPONSES、ANTHROPIC、GEMINI。沿用 Endpoint 含义、加密凭据、网络限制和超时；禁用 SDK 隐式重试，限制响应体，脱敏错误。模型测试接口的 `mode: "tools"` 只测试随机 nonce echo 工具及回执，不接业务服务。

助手绑定使用现有 `ai/features/assistant`：选模型，可选搜索；保留 displayMode/petId。未绑定时显示配置入口。下载、扫描、分类、音频任务来自所属服务，回执提供真实任务 ID，通用任务中心仅查询投影。

管理路由 `/system/assistant` 使用左侧结果、确认、任务和右侧对话；窄屏切页签。宠物面板和工作台共用服务端会话，工作台隐藏重复悬浮宠物。任务进度独立查询，不因停止回复消失。
