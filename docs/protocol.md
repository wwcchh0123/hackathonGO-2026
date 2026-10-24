# 模块接口协议

## 云端 MCP

以 [issue #8](https://github.com/wwcchh0123/hackathonGO-2026/issues/8) 为准，使用官方 `modelcontextprotocol/go-sdk` 的旧版 SSE：

1. `GET /mcp` 建流，首个 `endpoint` 帧通告 `POST /mcp/message?sessionid=...`。
2. 向该 endpoint 发送 JSON-RPC：initialize → notifications/initialized → tools/list → tools/call；响应通过 GET 的 SSE `message` 帧返回，POST 返回 HTTP 202。
3. endpoint 的 `sessionid` 是 **MCP 传输会话**，不同于灵矽对话 ID 和 AgentAPI session id。断线后重新建流、初始化；不复用失效 endpoint。

| 请求头 | GET /mcp | POST /mcp/message |
| --- | --- | --- |
| Authorization: Bearer CONTROL_TOKEN | 校验，错误 401 | 校验，错误 401 |
| X-Linx-Device-Id | 不要求、不绑定身份 | 必须等于 DEVICE_ID，否则 403；仅 mock 可省略 |
| X-Linx-Session-Id | 不用于调用身份 | 灵矽对话 ID，仅日志和去重 |
| X-Linx-Agent-Id | 不用于调用身份 | 仅日志 |

MCP 不接受浏览器 Cookie 作为鉴权。mock 的 CONTROL_TOKEN 可为空以便本机演示；配置后 GET/POST 都必须带 Bearer。身份以每次 POST 为准，工具参数不接受设备或会话 ID。

只提供以下三个工具，不提供云端投屏或切画面工具：

| 工具 | 参数 | 行为 |
| --- | --- | --- |
| send_message | `{ "text": "按钮改红" }` | 默认入口；新需求、修改、继续、暂停均原话转交，由服务端判断会话状态 |
| start_new_project | `{ "text": "换个，做个计算器" }` | 仅明确要做与当前作品无关的新项目时使用 |
| get_status | `{}` | 同步只读快照，不入队、不查询云端 |

text 去首尾空白后须为 1–2000 个 Unicode 字符。其余字段拒绝。两种修改性工具仅做非阻塞队列受理，不等待 Provider（工具延迟目标 P99 < 300 ms）；队列最多 32 条，另有一条可正在执行。

### 会话决策

`session` worker 按**执行时**状态决策，创建期间的消息和新项目均按顺序排队：

- message：没有绑定会话、上次创建失败或 Provider.Status 为 terminated → 新建并绑定后发送；idle → 复用发送；正在执行 → interrupt → 轮询 Provider.Status 到 idle → 复用发送。停止期间若变成 terminated，改建新会话。
- start：旧会话若仍执行，先 interrupt 并等到 idle（或 terminated）；取消旧观察器，清空绑定和结果，再创建、绑定并发送。旧会话不删除，留给 AgentAPI 生命周期回收。
- 不凭 interrupt 返回成功或历史 idle 事件判断运行时停止。"继续"、"停一下"是普通消息，暂停原话也会产生一个简短新轮次。
- 异步失败写入设备快照 error / summary；下一次消息可重试创建或复用已有会话。

### 返回与去重

工具只返回一句简短中文，至多 60 字；不返回 structuredContent / outputSchema、链接、内部 ID 或 JSON。业务拒绝返回 isError=true，受理成功不代表编程已完成：

```json
{"content":[{"type":"text","text":"好的，已转达"}],"isError":false}
```

| 工具 / 受理时快照 | 文本 |
| --- | --- |
| send_message，无会话 / 已终止 | 好的，开始做。云端准备环境约需半分钟 |
| send_message，创建 / 提交 / 执行中，或前面还有排队命令 | 好的，先停下当前的再按你说的做 |
| send_message，空闲 | 好的，已转达 |
| start_new_project | 好的，开个新项目。云端准备环境约需半分钟 |
| get_status，无任务 | 还没有任务，可以告诉我想做什么 |
| get_status，creating | 正在准备云端环境 |
| get_status，sending / running | 正在做：摘要 |
| get_status，idle | 按 stop_reason 的摘要；有 result_url 时补充“结果已经在屏幕上了” |
| get_status，terminated | 任务已结束，再说个需求就会开始新任务 |
| get_status，异步错误 | 操作未完成，请检查设备上的错误后重试 |

摘要含技术标识、链接、JSON 或非中文文本时，语音使用安全的中文状态摘要；完整事件和错误保留给设备调试。idle 不等于成功，interrupted / requires_action / retries_exhausted / budget_reached 均如实报告，不声称完成。

同一工具、同一非空 X-Linx-Session-Id、相同的去首尾空白后 text，在首次成功受理后的 **5 秒**内返回原 receipt，不入队，不因重试延长窗口。两个修改性工具分别去重，避免吞掉明确的新项目意图；无对话头不去重；拒绝不缓存。缓存仅在当前进程内共享，可跨 MCP 连接生效。调用日志记录工具名、当次 POST 的三项灵矽身份头、是否重复、是否错误和耗时，不记录 token 或需求正文。

go-sdk 的旧 SSE 不传递每次 POST 的 RequestExtra.Header，因此 HTTP 入口将身份覆盖写入内部 `_meta` 后交给工具处理器。客户端同名 metadata 会被覆盖，不可伪造去重身份；它不属于工具 arguments 或返回值。

## 灵矽配置与部署

私有自定义 MCP：URL `https://<域名>/mcp`，Header `Authorization: Bearer <CONTROL_TOKEN>`；设备 ID 须匹配服务端 DEVICE_ID。选择“统一意图识别（Function Call）”，在 MCP 编辑中关联本服务后单独保存。

智能体工具段：

```text
你是编程搭档的传话员，云端有一位程序员负责写代码。回复一句话，口语化，不念链接和编号。
- 对当前作品说的话（新需求、修改、继续、停下）→ send_message，text 保留原话。
- 明确要做一个与当前作品无关的新东西 → start_new_project；拿不准时问一句“是改现在这个，还是做个新的？”
- 问进度、好了没、看看效果 → get_status，如实转述。
- 工具失败时转述原因，不要重复调用；闲聊自己回答。
```

公网须提供 HTTPS。反代关闭 SSE 缓冲、允许 WebSocket，读超时 ≥ 10 min。例如已有 TLS 入口后的 nginx location：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $proxy_host;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 600s;
}
```

反代向本机上游传递 `$proxy_host`（本例为 127.0.0.1:8080），不要把公网 Host 原样传给监听 loopback 的 MCP SDK，否则其 localhost Host 校验会返回 403。对外结果地址仍由 PUBLIC_URL 配置。

不启用 go-sdk KeepAlive，它会要求客户端回应 ping。若实测有空闲断流，再按需增加不要求回应的 HTTP SSE 注释心跳（`: ping`）；当前未启用。服务不主动播报；灵矽 LLM 转述工具结果，也可直接 TTS。

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

设备 API 允许 Bearer 或 Cookie 鉴权；浏览器 WebSocket/EventSource 使用 Cookie，避免把密钥放在 URL。未知会话不能订阅。旧连接由设备在 session_id 切换时关闭。

```json
{
  "device_id": "demo-device",
  "session_id": "ses_...",
  "status": "running",
  "summary": "正在处理你的需求",
  "prompt": "帮我做个番茄钟网页",
  "result_url": "",
  "revision": 3
}
```

快照**不包含 view**。status 为 ready / creating / sending / running / idle / terminated / error；revision 进程内单调递增；可选 error 为最近控制或产物错误。设备自主选择视图，在 result_url 出现或变化时展示结果，新轮次 result_url 清空后回到过程展示。

事件帧：

```text
id: evt_...
event: agent_event
data: {"id":"evt_...","type":"agent.message","content":[{"type":"text","text":"正在创建页面"}]}

```

data 完整保留上游 JSON（含未知字段），类型取 data.type。首次全量回放；重连可用 Last-Event-ID 从已知事件后恢复，未知游标全量回放，设备仍按 data.id 去重。当前内存日志无跨进程游标保证。

## 开发控制台

仅 DEV_TOOLS=true 暴露 POST /api/commands；设备展示模块不使用。

```json
{"action":"message","prompt":"按钮改红"}
```

新项目使用 `{"action":"new_project","prompt":"做个计算器"}`（HTTP 层映射到 session 的 start action）；start 也可直接提交。旧 interrupt / continue / show action 已移除。返回 HTTP 202 和 receipt；无效指令 / 过载返回 400。视图按钮只在浏览器本地操作，不产生控制命令。查进度读快照。

## AgentAPI 适配边界

本次以 Mock 打通主流程，保留现有 Provider 和 HTTP 适配器，不扩展真实交互。以下现有协议留待实际部署验证，服务端注入 x-api-key：

1. POST /v1/sessions，`{ "agent": "agent_id", "environment_id": "env_id" }`。
2. POST /v1/sessions/{id}/events，`{ "events": [{ "type": "user.message", "input": { "parts": [{ "type": "text", "text": "需求" }] } }] }`。
3. 同一 events 接口发送 `{ "events": [{ "type": "user.interrupt" }] }`。
4. GET /v1/sessions/{id} 查询真实状态，停止确认后才发送修改需求。
5. GET /v1/sessions/{id}/events/stream 读取历史与实时 SSE；断线后重连去重。
6. GET /v1/files?scope_id={session_id} 分页列举；GET /v1/files/{id}/content 下载结果。

参考仓库仅作协议依据，不作构建依赖。硬件与投屏实现不属于本服务范围。
