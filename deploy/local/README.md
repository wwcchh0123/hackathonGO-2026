# 本地两 MCP 联调

本目录只描述本机模拟，不是树莓派固件安装脚本。它把两个本地 HTTP 服务合并到一个 ngrok HTTPS 端点：

| 公网路径 | 本机上游 | 职责 |
| --- | --- | --- |
| `/mcp` | `127.0.0.1:8080` | 控制 MCP，创建和驱动 AgentAPI Session |
| `/display/` | `127.0.0.1:8080` | Session Event 渲染网页 |
| `/device/mcp` | `127.0.0.1:8787/mcp` | 模拟树莓派设备 MCP，打开网页并 AirPlay |

先启动控制服务和设备服务，再启动 nginx；最后让 ngrok 暴露 `8000`：

```sh
# 终端一：先准备好 ngrok 域名，再让控制服务生成可投屏的页面 URL。
PUBLIC_URL=https://<ngrok-domain> \
AGENTAPI_MODE=mock \
CONTROL_TOKEN=<local-control-token> \
go run ./cmd/control

# 终端二：模拟树莓派。没有 BROWSER_BIN 时会自动寻找 Chromium。
go run ./cmd/device-display

# 终端三：反代两个本地服务。
nginx -c "$PWD/deploy/local/nginx.conf" -p "$PWD/deploy/local"

# 终端四：公网入口。
ngrok http 8000
```

真实 AgentAPI 联调时，把第一条命令的 `AGENTAPI_MODE=mock` 换成 `http`，并通过进程环境变量提供：

```sh
AGENTAPI_MODE=http \
AGENTAPI_BASE_URL=https://agent.sufy.com \
AGENTAPI_API_KEY=<server-only-key> \
AGENTAPI_AGENT_ID=<claudecode-agent-id> \
AGENTAPI_AGENT_VERSION=1 \
AGENTAPI_ENVIRONMENT_ID=<cloud-environment-id> \
CONTROL_TOKEN=<separate-control-token> \
PUBLIC_URL=https://<ngrok-domain> \
go run ./cmd/control
```

不要把 `AGENTAPI_API_KEY` 配到灵矽 Agent、设备 MCP、网页 URL 或日志中。`CONTROL_TOKEN` 只用于控制 MCP；设备 MCP 不需要 AgentAPI Key。

## 模拟设备默认值

`cmd/device-display` 的默认配置已经覆盖之前验证过的接收端：

```text
DEVICE_MCP_LISTEN_ADDR=127.0.0.1:8787
AIRPLAY_TARGET=192.168.1.4
AIRPLAY_PORT=7001
AIRPLAY_PASSWORD=3939
AIRPLAY_NAME=Windows AirPlay@MISAKI-DESKTOP
SCREEN_WIDTH=1920
SCREEN_HEIGHT=1080
AIRPLAY_FPS=20
AIRPLAY_BITRATE=3000
AIRPLAY_VIDEO_CODEC=h264
DOUBLETAKE_BIN=doubletake
BROWSER_USER_AGENT=LinxiDevice/0.1
```

可用 `DISPLAY_ALLOWED_HOSTS=公网域名` 限制设备浏览器只打开控制服务页面。`AIRPLAY_CREDENTIALS` 默认写入 `/tmp/doubletake/credentials.json`，只保留在设备本地。

## 灵矽 Agent 工具提示

给 Agent 配置两个自定义 MCP：

```text
控制 MCP：https://<ngrok-domain>/mcp
设备 MCP：https://<ngrok-domain>/device/mcp
```

调用控制 MCP 的 `new_task` 或 `send_message` 后，从 JSON 结果读取 `display_url`，立即把完整 URL 原样传给设备 MCP 的 `cast_screen`。当 `PUBLIC_URL` 是 ngrok 域名时，`display_url` 会自动附加 `ngrok-skip-browser-warning=true`；模拟设备 Chromium 还使用非标准 User-Agent `LinxiDevice/0.1`，可避免后续资源请求进入浏览器提示页；可用 `BROWSER_USER_AGENT` 覆盖。用户要求切换历史任务时先调用 `list_tasks`，根据标题、时间和摘要选择明确的 `task_ref`，再调用 `get_task` 取得对应 `display_url`，最后把地址交给 `cast_screen`。`get_task` 只查询任务，不改变消息发送目标；`stop_cast` 只停止投屏，不停止 AgentAPI 任务。不要把控制令牌或 AgentAPI Key 填入工具参数，也不要把灵矽语音 session_id 当成 AgentAPI 的 task_ref。

设备 MCP 的旧版 SSE endpoint 会由 nginx 的 `/device` 前缀正确回写为 `/device/mcp/message`；不要把设备 MCP 直接和控制 MCP 共用 `/mcp` 路径。
