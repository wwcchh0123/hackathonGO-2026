# 模块接口协议

## 云端 MCP

`GET /mcp` 打开 SSE，首个 `endpoint` 帧通告 `POST /mcp/message?sessionid=...`。客户端按标准流程 initialize → notifications/initialized → tools/list → tools/call。传输与 JSON-RPC 由官方 `modelcontextprotocol/go-sdk` 实现。

请求头：`X-Linx-Device-Id: <DEVICE_ID>`。仅 mock 允许省略；真实模式不允许由工具 arguments 指定设备。配置了控制令牌时，GET 与 POST 均需 `Authorization: Bearer <CONTROL_TOKEN>`。

| 工具 | 参数 | 行为 |
| --- | --- | --- |
| start_task | `{ "prompt": "做一个番茄钟" }` | 异步创建新会话并发送需求 |
| interrupt | `{ "new_instruction": "按钮改红" }` 或 `{}` | 先停止；有新需求时沿用会话发送 |
| continue_task | `{}` | 原会话继续 |
| get_status | `{}` | 同步返回一句话摘要及快照 |
| show | `{ "target": "timeline/code/terminal/preview" }` | 异步切视图，四个枚举值之一 |

修改性工具返回简短 `content[].text` 与受理信息；下游执行失败通过设备快照 `error` / `summary` 观察。若没有结果，show preview 会留下“结果尚未生成”的错误，保留原视图。队列最多 32 条，过载明确拒绝。

## 设备展示链路

| 接口 | 用途 |
| --- | --- |
| GET /api/bootstrap | 设备 ID、mock/http 模式、开发控制台是否开启 |
| POST /api/connect | `{ "token": "控制服务令牌" }`，换取 HttpOnly/SameSite Cookie |
| GET /api/device | 当前设备完整快照 |
| GET /api/device/channel | WebSocket，连接后立即发送最新快照，随后按变化发送 |
| GET /api/sessions/{id}/events | 当前绑定会话的 SSE，先回放后实时；跨 idle 保持连接 |
| GET /results/{随机发布ID}/... | 静态结果，通过不可枚举的链接供手机访问 |
| GET /healthz | 无凭据健康检查，不检查云依赖是否可达 |

API 允许 Bearer 或 Cookie 鉴权；浏览器 WebSocket/EventSource 使用 Cookie，避免把密钥放在 URL。未知会话不能订阅。旧连接由设备在 session id 切换时关闭。

快照示例：

```json
{
  "device_id": "demo-device",
  "session_id": "ses_...",
  "status": "running",
  "summary": "正在处理你的需求",
  "prompt": "帮我做个番茄钟网页",
  "view": "timeline",
  "result_url": "",
  "revision": 3
}
```

`status` 为 ready / creating / sending / running / idle / terminated / error。`revision` 进程内单调递增。可选 `error` 表示最近的控制或产物错误；原始 AgentAPI 错误仍在 SSE 中保留。

事件帧：

```text
id: evt_...
event: agent_event
data: {"id":"evt_...","type":"agent.message","content":[{"type":"text","text":"正在创建页面"}]}

```

`data` 完整保留上游 JSON，包括未知字段；SSE 的事件名称统一为 agent_event，事件真实类型取 data.type。首次全量回放，重连可用 `Last-Event-ID` 从已知事件后恢复；未知游标回放全部，设备仍按 data.id 去重。当前内存日志无跨进程游标保证。

## 开发控制台

仅 `DEV_TOOLS=true` 暴露 `POST /api/commands`；用于模拟云端意图，设备展示模块不使用。

```json
{"action":"start","prompt":"做一个番茄钟"}
```

其他请求：`{"action":"interrupt","prompt":"按钮改红"}`、`{"action":"continue"}`、`{"action":"show","view":"preview"}`。返回 HTTP 202 与 `{ "accepted": true, "message": "好的，开始做" }`。查进度直接读快照。

## AgentAPI 适配

服务端注入 `x-api-key`，不把密钥或上游错误响应体下发设备：

1. `POST /v1/sessions`，`{ "agent": "agent_id", "environment_id": "env_id" }`。
2. `POST /v1/sessions/{id}/events`，`{ "events": [{ "type": "user.message", "input": { "parts": [{ "type": "text", "text": "需求" }] } }] }`。
3. 同一 events 接口发送 `{ "events": [{ "type": "user.interrupt" }] }`。
4. `GET /v1/sessions/{id}` 查询真实状态，停止确认后才发送修改需求。
5. `GET /v1/sessions/{id}/events/stream` 读取历史与实时 SSE；服务端断线后重连并去重。
6. `GET /v1/files?scope_id={session_id}` 分页列举；`GET /v1/files/{id}/content` 下载结果。

依据：本机 AgentAPI 的 `docs/protocol/swagger.yaml`、会话/事件/文件开发文档。真实服务的版本与凭据仍需联调验证。

## 设备侧扩展

硬件端暂不选择技术栈、不实现原生桥；云端语音与端侧投屏分别走灵矽协议，控制服务只负责上述 WebSocket/SSE 展示协议。具体边界和工具约定见 [硬件接入边界](device-contract.md)。

灵矽协议依据：本机 `/Users/jicarl/qiniu/Xrobot-docs/docs/xrobot/mcp/software-mcp.md`、`hardware-mcp.md`、`platform/websocket.md`、`guide/mcp-store.md`。云 MCP 配置可在 `config.sse.header.Authorization` 中设置 Bearer；平台自动带入设备 ID。也可参考 [云端 MCP 接入指南](https://linx.qiniu.com/docs/xrobot/mcp/software-mcp)。
