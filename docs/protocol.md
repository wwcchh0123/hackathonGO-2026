# 模块接口协议

## 云端 MCP

工具语义以 [issue #8](https://github.com/wwcchh0123/hackathonGO-2026/issues/8) 为准。使用官方 `modelcontextprotocol/go-sdk`，同一 `/mcp` 地址支持 Streamable HTTP 和旧版 SSE。

### Streamable HTTP（当前灵矽管理端与语音端）

- `POST /mcp` 接收 JSON-RPC，`Accept` 包含 `application/json, text/event-stream`；请求与响应均使用该地址。
- 支持管理端的 `server/discover`，以及 `initialize → notifications/initialized → tools/list → tools/call`。实际观察到管理端初始化版本为 `2025-11-25`，语音端为 `2025-06-18`；管理端也会探测 `2026-07-28` 的 `server/discover`。
- 使用无状态 MCP 传输，响应使用 JSON；不创建或要求 `Mcp-Session-Id`。编程会话和五秒去重仍由共享控制服务维护，不受传输连接生命周期影响。
- 带协议头或会话头的 `GET /mcp`、`DELETE /mcp` 由 Streamable HTTP 处理器按无状态协议返回 405；这是不提供额外订阅流或会话删除的正常行为。`POST /mcp` 必须可用。
- 管理端获取工具时没有设备 ID，网页体验端可能携带虚拟设备 ID。因此 `server/discover`、`initialize`、`notifications/initialized`、`tools/list`、`ping` 只校验 Bearer，不要求设备匹配；`tools/call` 仍必须通过设备校验。网页能列出工具不代表其虚拟设备已获准控制 Mac，应使用绑定的 Mac 语音客户端验证工具调用。

### 旧版 SSE（兼容）

1. `GET /mcp` 建流，首个 `endpoint` 帧通告 `POST /mcp/message?sessionid=...`。
2. 向该 endpoint 发送 JSON-RPC：initialize → notifications/initialized → tools/list → tools/call；响应通过 GET 的 SSE `message` 帧返回，POST 返回 HTTP 202。
3. endpoint 的 `sessionid` 是 **MCP 传输会话**，不同于灵矽对话 ID 和 AgentAPI session id。断线后重新建流、初始化；不复用失效 endpoint。

| 请求头 | GET /mcp | POST /mcp/message |
| --- | --- | --- |
| Authorization: Bearer CONTROL_TOKEN | 校验，错误 401 | 校验，错误 401 |
| X-Linx-Device-Id | 不要求、不绑定身份 | 必须等于 DEVICE_ID，否则 403；仅 mock 可省略 |
| X-Linx-Session-Id | 不用于调用身份 | 灵矽对话 ID，仅日志和去重 |
| X-Linx-Agent-Id | 不用于调用身份 | 仅日志 |

MCP 两种传输都不接受浏览器 Cookie 作为鉴权。mock 的 CONTROL_TOKEN 可为空以便本机演示；配置后所有 MCP HTTP 请求都必须带 Bearer。工具调用的身份以每次 POST 为准，工具参数不接受设备或会话 ID。`tools/call` 中缺失设备 ID 仅在 mock 模式兼容；显式传入不匹配的设备 ID 始终拒绝。

### 工具

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

快照**不包含 view**。status 为 ready / creating / sending / running / idle / terminated / error；revision 进程内单调递增；可选 error 为最近控制错误，stream_error 表示上游事件流重连状态，preview_error 表示静态产物发布错误。设备自主选择视图，在 result_url 出现或变化时展示结果，新轮次 result_url 清空后回到过程展示。

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

AgentAPI 的真实 HTTP Provider 使用其文档推荐的 `github.com/anthropics/anthropic-sdk-go v1.72.0`；Session、Events、事件 SSE 和 Files API 均通过 SDK 调用。SDK 返回的事件使用 `RawJSON()` 保存，未知字段不会被业务层重写。Mock 用于无密钥自动测试；HTTP Provider 是真实部署路径。服务启动时只读校验预配置的 claudecode agent 和 ready cloud environment，然后服务端注入 x-api-key：

1. GET `/v1/agents/{id}` 与 `/v1/environments/{id}` 只读校验配置；POST /v1/sessions，`{ "agent": {"type":"agent", "id":"agent_id", "version": 1}, "environment_id": "env_id" }`（不固定版本时兼容字符串 agent）。
2. POST /v1/sessions/{id}/events，`{ "events": [{ "type": "user.message", "input": { "parts": [{ "type": "text", "text": "需求" }] }, "payload": {"client_request_id":"cmd_...", "source":"linxi-control"} }] }`；回执优先记录 accepted user.message event ID。
3. 同一 events 接口发送 `{ "events": [{ "type": "user.interrupt" }] }`。
4. GET /v1/sessions/{id} 查询真实状态，停止确认后才发送修改需求。
5. GET /v1/sessions/{id}/events/stream 读取历史与实时 SSE；断线后重连去重。
6. GET /v1/files?scope_id={session_id} 分页列举；GET /v1/files/{id}/content 下载结果。文件延迟可见时有限重查；只在有 HTML 且本轮内容指纹变化时发布新预览。

参考仓库仅作协议依据，不作构建依赖。硬件与投屏实现不属于本服务范围。

## 设备侧扩展

Mac 软件终端通过独立的 `cmd/voice` 接入灵矽语音，启动和身份管理见 [语音客户端说明](voice-client.md)。设备身份是控制服务 `DEVICE_ID` 的值，不要使用灵矽对话 session id 替代。语音页面尚未实现端侧 MCP、投屏或上述设备展示通道，也不会声明 `features.mcp`。

灵矽协议依据：本机 `/Users/jicarl/qiniu/Xrobot-docs/docs/xrobot/mcp/software-mcp.md`、`hardware-mcp.md`、`platform/websocket.md`、`guide/mcp-store.md`。云 MCP 配置可在 `config.sse.header.Authorization` 中设置 Bearer；平台自动带入设备 ID。也可参考 [云端 MCP 接入指南](https://linx.qiniu.com/docs/xrobot/mcp/software-mcp)。

## Mac 本地语音桥（独立端口 8090）

`cmd/voice` 只监听 loopback；校验 Host，并要求 WebSocket Origin 与当前 HTTP 页面完全一致。一台设备同一时间只允许一个语音窗口，第二个连接返回 409。此处不使用控制服务的 `CONTROL_TOKEN`；灵矽设备 token 只在 Go 进程内使用，不下发浏览器、不写入 URL 或日志。

| 接口 | 内容 |
| --- | --- |
| `GET /` | 独立语音页面，不同于 8080 的 `/simulator` |
| `GET /api/status` | `device_id`、`stage`、可选激活 `code` / `error`、`audio_available` |
| `GET /api/voice` | 同源 WebSocket：浏览器 PCM 与 Go/灵矽语音的双向桥 |

`stage` 为 connecting / activation / paired / error；paired 仅表示 OTA 已绑定，真正语音就绪以 WebSocket 的 ready 事件为准。

浏览器发送 `{"type":"start"}` → 二进制 PCM16LE（单声道，16 kHz，每帧 960 samples / 1920 bytes / 60 ms）→ `{"type":"stop"}`。末帧补零，顺序发送完末帧后才发 stop。`{"type":"abort"}` 仅停止语音播报，不代表中断编程任务。网页单次录音最长 60 秒，停止/关闭页面会释放麦克风；发送积压过多会关闭连接。

服务端获取 OTA token 后以 WSS 连接灵矽，发送 Opus 16 kHz / 60 ms hello，等待最多 10 秒。ready 示例：

```json
{"type":"ready","session_id":"灵矽对话ID","sample_rate":24000,"input_sample_rate":16000,"frame_samples":960}
```

ready 的 sample_rate 是返回音频采样率；桥按云端 hello 的参数解码 Opus，向浏览器发送该采样率的单声道 PCM16LE 二进制帧。浏览器按每帧实际样本数安排播放，不假设返回音频也是 60 ms。下行文本事件包括 status / activation / ready / stt / tts / notify / ended / error；仅转发白名单语音字段，不转发原始 OTA 响应。正常 tts.stop 等缓冲播放完毕，is_aborted=true 立即清空；录音及本地打断期间丢弃旧回复。云端 goodbye 对应正常 ended，不提示配置错误。断线立即停止采集和播放，用户点击重新连接时重新获取 OTA 凭据，不自动补发上一段录音。
