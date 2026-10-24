# MCP 到 AgentAPI 实际调用设计与实现状态

本期范围是服务端链路：MCP 客户端 → Go 控制服务 → AgentAPI → 服务端事件/状态/静态预览。真实设备、语音输入播报和投屏仍是后续工作；本期用 MCP 测试客户端、HTTP/WS/SSE 接口和开发联调页验收。

## 当前实现

- `transport/mcp` 提供 `send_message`、`start_new_project`、`get_status`，修改性工具快速入队并在五秒窗口内按灵矽会话去重。
- `session.Controller` 是唯一业务入口，串行处理新建/复用、interrupt 后真实 Status 轮询、rescheduling、terminated、创建/发送不确定结果和命令代际。
- `agentapi.HTTP` 使用 AgentAPI 文档推荐的 `github.com/anthropics/anthropic-sdk-go v1.72.0`：`Beta.Sessions.New/Get`、`Beta.Sessions.Events.Send/StreamEvents`、`Beta.Files.ListAutoPaging/Download`。SDK stream 的 `RawJSON()` 写入本地事件日志。
- HTTP 模式启动时只读校验 agent 和 environment，不自动创建或修改云端资源；agent version 可由 `AGENTAPI_AGENT_VERSION` 固定。
- 每次消息带 `client_request_id` payload；Send 回执中的 user.message ID 和事件流共同确定当前轮次。旧 idle、旧观察器和旧发布任务不能覆盖新轮次。
- end_turn 只调度异步 Files API 发布；会话事件继续消费。文件有界重查，按路径/内容指纹去重，发布前后校验 session/turn 代际。
- 设备快照增加 `stream_error`、`preview_error`，硬件仍未实现；开发联调页是本期展示入口。

## 状态决策

| AgentAPI 状态 | message | start_new_project |
| --- | --- | --- |
| 无绑定 / terminated | 新建 Session 后发送 | 新建 Session 后发送 |
| idle | 复用 Session 发送 | 新建 Session |
| running / rescheduling | interrupt，查询到 idle 后复用发送 | interrupt，查询到 idle 后新建 |
| 停止或提交不确定 | 保留绑定并等待有界核对，不自动重复写入 | 同样阻止绕过核对 |

`session.status_idle` 仍须结合 `stop_reason`；服务不会把 interrupted、requires_action、retries_exhausted 或 budget_reached 说成成功。

## 配置

```sh
AGENTAPI_MODE=http
AGENTAPI_BASE_URL=https://agent.example
AGENTAPI_API_KEY=server-only-key
AGENTAPI_AGENT_ID=agt_...
AGENTAPI_AGENT_VERSION=1
AGENTAPI_ENVIRONMENT_ID=env_...
CONTROL_TOKEN=separate-control-token
```

Key 只从服务端环境变量读取，`CONTROL_TOKEN` 与它分开。AgentAPI 资源不写入仓库；HTTP 模式启动失败会在 agent/environment 不可用时退出。

## 验收

自动测试使用 Mock 和本地 `httptest`，运行 `make check test build`。真实联调使用 MCP 测试客户端：工具发现、首次创建、运行中修改、同会话复用、新项目切换、真实 SSE 事件、Files API `index.html` 和浏览器预览均需记录脱敏的 session/event/file 证据。

当前已使用专用 claudecode agent 和 cloud environment 完成真实 SDK 链路验证；资源 ID 保存在本机联调记录中，密钥不写入代码。真实设备展示留待设备客户端完成后单独验收。
