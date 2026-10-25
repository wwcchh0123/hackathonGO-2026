# 模块接口协议

## 云端 MCP

多任务工具语义以 [issue #18](https://github.com/wwcchh0123/hackathonGO-2026/issues/18) 为准，替换 #8 的三个旧工具。使用官方 `modelcontextprotocol/go-sdk`，同一 `/mcp` 地址支持 Streamable HTTP 和旧版 SSE。

### Streamable HTTP（当前灵矽管理端与语音端）

- `POST /mcp` 接收 JSON-RPC，`Accept` 包含 `application/json, text/event-stream`；请求与响应均使用该地址。
- 支持管理端的 `server/discover`，以及 `initialize → notifications/initialized → tools/list → tools/call`。实际观察到管理端初始化版本为 `2025-11-25`，语音端为 `2025-06-18`；管理端也会探测 `2026-07-28` 的 `server/discover`。
- 使用无状态 MCP 传输，响应使用 JSON；不创建或要求 `Mcp-Session-Id`。任务引用、对话发送目标和五秒去重由共享控制服务维护，不受传输连接生命周期影响。
- 带协议头或会话头的 `GET /mcp`、`DELETE /mcp` 由 Streamable HTTP 处理器按无状态协议返回 405；这是不提供额外订阅流或会话删除的正常行为。`POST /mcp` 必须可用。
- 管理端获取工具时没有设备 ID，网页体验端可能携带虚拟设备 ID。因此 `server/discover`、`initialize`、`notifications/initialized`、`tools/list`、`ping` 只校验 Bearer，不要求设备匹配；`tools/call` 仍必须通过设备校验。网页能列出工具不代表其虚拟设备已获准控制 Mac，应使用绑定的 Mac 语音客户端验证工具调用。

### 旧版 SSE（兼容）

1. `GET /mcp` 建流，首个 `endpoint` 帧通告 `POST /mcp/message?sessionid=...`。
2. 向该 endpoint 发送 JSON-RPC：initialize → notifications/initialized → tools/list → tools/call；响应通过 GET 的 SSE `message` 帧返回，POST 返回 HTTP 202。
3. endpoint 的 `sessionid` 是 **MCP 传输会话**，不同于灵矽对话 ID 和 AgentAPI session id。断线后重新建流、初始化；不复用失效 endpoint。

| 请求头 | GET /mcp | POST /mcp/message |
| --- | --- | --- |
| Authorization: Bearer CONTROL_TOKEN | 校验，错误 401 | 校验，错误 401 |
| X-Linx-Device-Id | 不要求、不绑定身份 | 每次工具调用的设备身份；合法 ID 会登记并路由到该设备 controller；仅 mock 可省略并使用默认设备 |
| X-Linx-Session-Id | 不用于调用身份 | 灵矽对话 ID，用于发送目标、日志和去重 |
| X-Linx-Agent-Id | 不用于调用身份 | 仅日志 |

MCP 两种传输都不接受浏览器 Cookie 作为鉴权。mock 的 CONTROL_TOKEN 可为空以便本机演示；配置后所有 MCP HTTP 请求都必须带 Bearer。工具调用的身份以每次 POST 为准，工具参数不接受设备或会话 ID。`tools/call` 中缺失设备 ID 仅在 mock 模式兼容；显式传入非法设备 ID 拒绝，已登记设备之间互不共享内存任务引用和对话发送目标。

### 四个业务工具

| 工具 | 参数 | 行为 |
| --- | --- | --- |
| `list_tasks` | `limit`（默认 20，上限 50）、`page`、可选 `status/include_archived/created_after/created_before` | 查询当前配置 Agent 的单页任务和消息摘录；不会带入同一 API Key 下其他 Agent 的 session |
| `get_task` | 必填 `task_ref`，可选 `command_id/events_limit/events_page/files_page` | 查询云端状态、本地命令结果、最近消息、停止原因、原始事件和文件元信息 |
| `send_message` | `text`，可选 `task_ref` | 原话发送到指定任务；运行中先 interrupt，再轮询真实 Status 至 idle，最后在同 Session 发送 |
| `new_task` | `text`，可选 `parent_task_ref` | 新建独立 Session 并发送首条需求；不停止其他任务。接力时附上实际可读取的消息/文件元信息及 parent_session_id |

`text` 去首尾空白后为 1–2000 个 Unicode 字符。未知参数拒绝。不保留 `get_status/start_new_project` 别名，需同步更新灵矽工具配置。`get_task.events_limit` 默认 30，上限 100，设为 0 不附带普通事件；状态判断仍按需读取有限的控制事件。事件默认按尾部倒序查询、返回本页内正序，`events_next_page` 用于获取更早一页，历史页不更新当前状态。

成功结果以 MCP `content[0].text` 中的 **JSON** 返回；不设置 `structuredContent/outputSchema`。灵矽读取 JSON 后生成一句口语回复，不朗读编号、JSON 或链接。业务拒绝为 `isError=true` 和简短原因。修改性工具只做有界内存受理，不等待上游，延迟目标仍为 P99 < 300 ms；查询会访问上游，单次读取预算默认 10 秒。

配置 `PUBLIC_URL` 后，`new_task`、`send_message` 和 `get_task` 的 JSON 结果附带 `display_url`。这是按 `task_ref` 绑定的公开只读展示页，灵矽 Agent 必须把完整值原样传给独立设备 MCP 的 `cast_screen`。当 `PUBLIC_URL` 使用 `*.ngrok-free.dev`、`*.ngrok.app` 或 `*.ngrok.io` 时，服务自动附加 `ngrok-skip-browser-warning=true`；模拟设备 Chromium 同时使用非标准 User-Agent `LinxiDevice/0.1`，避免后续资源请求再次进入 ngrok 提示页；可通过 `BROWSER_USER_AGENT` 覆盖。创建任务是异步的，设备可以先打开地址；展示页会在云端 Session 建立后自动重连。`display_url` 不包含 AgentAPI Key、CONTROL_TOKEN 或灵矽身份。

```json
{"accepted":true,"message":"已受理，请查询任务确认提交和执行结果","task_ref":"task_...","command_id":"cmd_..."}
```

受理不表示发送或执行成功。`get_task.command` 与云端 `status` 分别呈现：命令状态为 `queued/submitting/confirmed/failed/unknown`；`phase` 表示 checking/interrupt/create/send/created，附 error、input_event_id 和时间。`confirmed` 仅证明需求已被上游接受，不代表任务完成。通过 `command_id` 查询旧命令；每个引用最多保留 32 条记录，未决记录不被静默覆盖。

`task_ref` 是进程内随机引用，空闲有效期 30 分钟，成功访问/重新列出可刷新引用，实时观察连接持有期间保留引用；最多保留 256 个引用。过期/重启后旧引用失效，必须重新列出，绝不把旧引用重新分配给其他 Session。`new_task` 在创建前就返回引用；创建失败或未知仍能查询。新建失败不因后续 send_message 自动再次创建。

省略发送引用时，只能复用同一非空 `X-Linx-Session-Id` 最近一次明确新建或发送的任务；新对话、缺失对话头、引用失效时拒绝猜测。列表、详情、展示选择均不改变发送目标。入队即固定目标，设备切屏不会改写队列里的目标。

同一非空灵矽对话、工具、目标引用和规范化 text，在首次受理后五秒内返回同一回执；`new_task` 在分配新引用前去重。无对话头不去重；去重缓存和对话发送目标各最多 1024 条。Create/Send 结果未知时，只做有限的 metadata/输入事件回查，不重发。未核对的旧写入阻止同任务后续写入；其他任务仍可执行。找回创建的 Session 时，明确报告“首条需求尚未发送”，由用户明确继续。本期不提供跨进程恰好一次投递。

### 查询字段与停止原因

`list_tasks` 返回 `data/next_page/has_more/sort/scope/queried_at/filters/partial`。当前公共上游只支持创建时间倒序，`sort=created_at_desc`，不能称为全局最近活跃。`scope=current_configured_agent`；服务端始终使用 `AGENTAPI_AGENT_ID` 过滤，不允许调用方切换到同一 API Key 下的其他 Agent。时间过滤使用 RFC3339，分别对应 created_at[gte/lte]。不支持自然语言检索或无限翻页。

任务卡片含 task_ref、标题、创建/更新时间、原始状态、archived、runtime、can_continue 和 continue_reason。summary 只来自最近非空 agent.message 的多文本块合并摘录（上限 400 字），带来源 event_id 和消息原时间；每次最多检查最近 8 条 Agent 消息，尚有更早候选时以 summary_unavailable 标记不完整。列表并发至多 4，单项摘要失败保留其他任务并标记 partial。旧摘要不表示新轮次已完成，工具/错误事件不覆盖摘录。

`get_task` 额外含 queried_at、stop_reason、messages、events、事件分页、files（单页最多 50 个元信息）、files_unavailable、command、可选 result_url。没有本地预览 URL 不代表没有云端产物。详情不透传内部 Agent 配置、资源认证数据或 Vault 凭据。

| 云端状态 | 语义 |
| --- | --- |
| running / rescheduling | 执行 / 恢复中；旧 idle 不提供本轮停止原因 |
| idle + end_turn | 本轮结束，不证明 GitHub 操作或整个任务成功 |
| idle + interrupted / requires_action | 已打断 / 等待进一步操作 |
| idle + retries_exhausted / budget_reached | 重试耗尽 / 达到预算 |
| idle + unknown | 无法从有限事件确认对应停止原因 |
| terminated | 原环境无法继续，需明确新建或接力 |
| archived | 与状态独立；首版禁止直接发送 |

仅兼容 claudecode 的历史任务可继续；不因默认 Agent 版本变化自动换 Session。sandbox 丢失不能恢复未提交文件。列表消息摘录不是独立的 GitHub 成功证明。

## 灵矽配置与部署

私有自定义 MCP：URL `https://<域名>/mcp`，Header `Authorization: Bearer <CONTROL_TOKEN>`。DEVICE_ID 必须与 Mac 绑定身份匹配；选择“统一意图识别（Function Call）”，重新发现控制 MCP 的四个工具和设备 MCP 的四个工具并保存关联。工具结果 JSON text 在目标灵矽部署联调通过前，不声称真实语音端已经验证。

```text
你是灵汐编程搭档。把用户原话交给云端 AgentAPI，并把对应 Session 页面投到大屏；你不是编程执行器，不能声称没有编程工具，也不能编造结果。控制 MCP 工具是 new_task、list_tasks、get_task、send_message；设备 MCP 工具是 cast_screen、scan_screens、stop_cast。

用户明确要做新项目或无关新任务时调用 new_task，text 原样传递。new_task 返回 display_url 后，立即把完整值传给设备 MCP 的 cast_screen，不等待任务完成。用户说继续、修改、完善、修复、暂停时复用目标 task_ref 调用 send_message，返回 display_url 后同样立即 cast_screen；不要调用旧的 start_new_project。

用户问进度、状态或效果时，已知 task_ref 就调用 get_task；说“最近那个/刚才那个/某个任务”时先 list_tasks，按标题、时间和摘要选出明确任务，再 get_task。用户要求切换展示时也是 list_tasks → get_task → cast_screen，不发送消息、不恢复任务。多个 Session 可以同时存在，展示页每 3 秒轮询左侧列表；不要把一个任务的状态当成全部任务。用户说往上或往下滚动当前投屏页面时调用设备 MCP 的 scroll_screen，direction 传 up/down，pages 默认 1。停止投屏只调用 stop_cast，不停止云端任务；默认目标已配置，不要无故 scan_screens。

accepted 只表示排队，confirmed 只表示需求已接受；只有 get_task 明确证明完成时才能说完成。查询结果或写入结果未知时先核对，不重复提交。工具失败要如实说明失败环节。回复保持简洁、自然、完整：直接回应用户，保留必要的状态、原因和下一步；避免重复复述、连续过程播报和无关寒暄。不朗读 JSON、task_ref、command_id、设备编号或 URL；灵矽 session_id 与 AgentAPI task_ref 不是同一个标识。
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

设备展示选择独立于云端四工具和灵矽对话发送目标。控制服务提供一张无需控制令牌的任务展示页，模拟树莓派设备 MCP 负责让本地 Chromium 打开它，再由 AirPlay sender 镜像浏览器画面。设备不访问 AgentAPI，也不持有控制服务令牌；`devui` 只是受保护的联调页面，`internal/voice/ui` 仍是独立语音客户端。

| 接口 | 用途 |
| --- | --- |
| GET /api/bootstrap | 设备 ID、模式、联调入口 |
| POST /api/connect | `{token}` 换 HttpOnly/SameSite Cookie |
| GET /api/tasks | 同 list_tasks 的查询参数和返回 |
| GET /api/tasks/{task_ref} | 同 get_task 的可选查询参数和返回 |
| POST /api/device/view | `{task_ref}` 明确选择展示目标，不发消息、不 interrupt、不更改发送目标 |
| GET /api/device | 已选择任务的最近设备快照 |
| GET /api/device/channel | WebSocket 推送快照变化；实时事件仍需设备显式建立 SSE |
| GET /api/tasks/{task_ref}/events | 有界最近历史加实时 SSE；建连触发观察，断连取消观察 |
| GET /api/sessions/{id}/events | 兼容 #12：只允许已明确选择展示的 Session |
| POST /api/tasks/{task_ref}/preview | 空闲时有界下载 HTML 导出文件并托管，返回 result_url |
| GET /results/{随机发布ID}/... | 已发布静态预览；不暴露目录列表 |
| GET /healthz | 无凭据健康检查，不检查云依赖 |

给设备浏览器使用的公开展示接口：

| 接口 | 用途 |
| --- | --- |
| GET /display/sessions?limit=20 | 公开只读任务索引；只返回列表展示字段，不返回事件内容或凭据 |
| GET /display/?task_ref=... | 1920×1080 深色大屏页面；有 task_ref 时选中指定任务，没有时自动选择最新任务 |
| GET /display/events?task_ref=... | 先发送任务快照和页面元信息，再回放有界原始事件并持续推送 |

展示页每 3 秒轮询一次 `/display/sessions`，所以页面打开后创建的新任务会自动出现在左侧列表。点击列表项只更新当前页面的 `task_ref` 并重建该任务的事件 SSE，不发送消息、不 interrupt、不改变灵矽对话的默认发送目标；设备也可以重新调用 `cast_screen` 投屏另一个任务的 `display_url`。展示页的 `task_ref` 只允许当前进程仍保留的任务引用；未知/过期引用的事件流返回 404。设备浏览器应直接使用 `display_url`，不要拼接内部 API 地址或附带控制令牌。

设备 API 使用 Bearer 或 Cookie；浏览器 WebSocket/EventSource 用 Cookie，密钥不进入 URL。MCP 不接受 Cookie。快照中的 task_ref/session_id 只表示展示选择，command_id/command_state/error 表示该任务的本地控制结果；status 是最近查询或实时观察得到的云端状态，stop_reason 独立返回，summary_event_id/summary_time 保留摘录来源。

SSE 每次先读取最近 100 条原始事件（窗口上限 8 MiB），再连接上游；上游历史回放追到该尾部边界前不再次转发或推进状态。重连使用最后已交付的事件边界跳过旧前缀，不重发用户需求。原始 JSON 含未知字段保持不变，按 `(session_id,event.id)` 识别事件。设备可传 Last-Event-ID，或首次从详情接流时传 `after=<事件ID>`。游标不在最近窗口时返回 HTTP 409，重新加载窗口后建立新连接；不能继续沿用失效游标。详情历史页由设备作为旧历史插入，不应用为实时状态。

```text
id: evt_...
event: agent_event
data: {"id":"evt_...","type":"agent.message","content":[{"type":"text","text":"正在创建页面"}]}
```

最多 4 个实时观察连接；退出/断连释放，不停止 Agent。连接跨 idle 保持；上游重连不代表重新执行任务。无法恢复时发送 stream_error/resync_required；设备应新建连接重新同步。联调页面最多保留 512 条展示事件；内存事件 Hub 每 Session 最多 512 条/8 MiB，淘汰游标要求重新同步。

展示页在每一轮会话进入 idle 后自动调用 `POST /display/preview`。这个接口只接受展示 URL 中的短期 `task_ref`，不需要控制令牌；服务端优先查找 `/mnt/session/outputs/index.html`，找不到时再选择其他 HTML 文件。接口返回 `available: false` 时展示页最多每 3 秒重试 4 次，仍没有 HTML 就保留事件时间线，不阻塞任务展示。找到结果后，页面在右侧隔离 iframe 中打开托管的静态页面，设备浏览器投屏的就是完整展示页。

预览下载有界：单文件上限 10 MiB、总计 64 MiB、最多 100 个最新路径，扫描历史导出最多 500 条，预算 30 秒；任务代际改变时丢弃迟到发布，A 的预览不覆盖 B；最多同时下载 2 份，磁盘保留最近 32 份预览，已淘汰的本地链接通过详情失效后可重新请求。不会因没有 HTML 隐藏其他文件元信息。控制服务仍保留 `POST /api/tasks/{task_ref}/preview` 供受保护的显式调用，公开展示接口只用于设备页面的自动预览。

## 中心设备管理链路

control 进程通过 `internal/controlplane` 维护一个进程内设备注册表。启动时登记配置的 `DEVICE_ID`；带有合法 `X-Linx-Device-Id` 的受鉴权 MCP 工具请求会登记/刷新对应设备的 `last_seen`，并路由到该设备独立的内存 `session.Controller`。创建 AgentAPI Session 时写入 `metadata.device_id`，管理端按该字段过滤任务；默认设备还兼容没有此 metadata 的旧历史任务。设备注册、任务引用和在线判定不跨进程持久化。

管理页和接口独立于设备展示选择，均为只读：

| 接口 | 用途 |
| --- | --- |
| `GET /admin` | 受控制服务访问令牌保护的中心管理页面 |
| `GET /api/admin/bootstrap` | 管理页服务信息和默认设备 |
| `GET /api/admin/overview` | 待处理设备/任务和设备数、在线数、运行任务数等聚合统计 |
| `GET /api/admin/devices` | 设备列表、`first_seen` / `last_seen` / 来源、在线判定、快照和任务计数；支持 query/health/sort/limit/page |
| `GET /api/admin/devices/{device_id}` | 单台设备摘要和当前快照 |
| `GET /api/admin/devices/{device_id}/snapshot` | 指定设备快照 |
| `GET /api/admin/devices/{device_id}/tasks` | 指定设备的任务分页，支持任务列表的状态/时间过滤 |
| `GET /api/admin/devices/{device_id}/tasks/{task_ref}` | 指定设备任务详情，支持有界事件、文件 metadata 和命令查询参数 |
| `GET /api/admin/tasks` | 跨设备任务列表；支持 query/device_id/status/limit/page，结果携带 device_id |
| `GET /api/admin/tasks/{device_id}/{task_ref}` | 跨设备入口的任务详情，task_ref 必须和 device_id 一起使用 |

管理端只使用 `CONTROL_TOKEN` 的 Bearer 或 HttpOnly `linxi_control` Cookie，不接触 AgentAPI Key、灵矽设备 token 或 MCP 凭据。查询设备/任务不会切换设备视图、不发送消息、不 interrupt、不下载预览；`online` 只代表最近窗口内收到过 control 请求，不代表硬件心跳。未知设备或不属于该设备的任务返回 404。

## 开发控制台

仅 DEV_TOOLS=true 暴露 POST /api/commands。新建使用 `{ "action":"new_project", "prompt":"做个页面" }`；修改使用 `{ "action":"message", "task_ref":"task_...", "prompt":"按钮改红" }`。返回 HTTP 202 和完整受理回执。消息目标和展示任务在联调页面分别选择，不能从当前画面猜测发送目标。任务进入 idle 后，展示页会自动尝试下载并打开本轮 HTML 产物；下一轮收到新的 `user.message` 或重新进入运行状态时，页面清空旧预览，等待新一轮结束后再尝试。没有 HTML 产物时继续显示事件时间线。

## AgentAPI 适配与 credential

使用 `github.com/anthropics/anthropic-sdk-go v1.72.0`；Session、Events、事件 SSE、Files 均通过 SDK。SDK 自动写入重试关闭，由控制器做有界回查；不会重试不确定的外部副作用。

- 创建使用预配置 agent/version 和 environment_id，以及 title、source/device/client_request_id metadata 和可选 vault_ids。`AGENTAPI_VAULT_IDS` 为逗号分隔的 Vault ID 引用；不向设备下发秘密，不固定 github_repository。
- 列表适配实际部署的单值 status、创建时间过滤和 page；不发送 SDK 的 statuses[]/order，不冒用 Portal 的 environment_id/q。
- Events.Send 使用 input.parts，payload.client_request_id 关联受理事件（关联 ID 不等于上游幂等保证）；Status 真实 idle 才释放打断等待。
- Events.List 通过 types[]、order、page 有界查询；SSE 的全历史回放不依赖上游 Last-Event-ID。
- Files.List 使用 scope_id 与分页；get_task 只读元信息，显式 preview 才下载。
- GitHub git/gh 能力由上游 Vault 注入和 Claude Code 提供。API/克隆/推送目标须在目标部署分别验证；控制服务不新增 GitHub provider 或 Issue/PR 状态机。

Mac 语音终端身份、OTA 和语音协议保持独立，当前不声明 features.mcp，也未接入端侧展示。绑定信息保存在忽略的 `.data/voice`。灵矽协议依据：[云端 MCP](https://linx.qiniu.com/docs/xrobot/mcp/software-mcp)。

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
