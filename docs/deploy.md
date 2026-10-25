# 运行与部署

## 选择运行方式

| 方式 | 依赖 | 可验证的体验 |
| --- | --- | --- |
| 本地 Mock | Go 1.26+ | 控制、任务切换、事件和固定示例产物；无需云端密钥 |
| 真实 AI + 浏览器 | Go、AgentAPI agent/environment、CONTROL_TOKEN | 真实 Session 编程、同任务修改、事件和静态产物 |
| 真实语音 + 投屏 | 上述配置、Mac/Opus、灵矽绑定、Chromium、doubletake、AirPlay 接收端 | 语音发起并在目标屏幕查看；需分别验收语音与物理接收画面 |

项目无需 Node.js 或前端构建。Node.js 仅用于浏览器音频开发测试。

## 本地模拟

```sh
make install
make dev
```

默认只监听 `127.0.0.1:8080`。[控制台](http://localhost:8080/simulator) 发起/修改任务，[设备页](http://localhost:8080/device) 选择展示任务，[管理页](http://localhost:8080/admin) 只读排障。默认 Mock 无访问令牌；设置 `CONTROL_TOKEN` 后在页面登录。

构建及运行：

```sh
make build
make run
```

会生成 `bin/control` 和 `bin/device-display`。语音程序独立构建，见 [Mac 语音指南](voice.md)。

## 真实 AgentAPI

准备固定版本的 Agent、cloud environment 和 AgentAPI Key。兼容配置只启用 claudecode；多 runtime 使用下文结构化 profiles。控制服务不创建 Agent/environment，仅按 allowlist 在新 Session 创建时覆盖 model/effort。把密钥留在运行主机：

```sh
cp .env.example .env
# 编辑 .env：使用 http 模式并填写下表配置
set -a
source .env
set +a
make build run
```

启动命令不会自动读取 `.env`。真实模式要求 `PUBLIC_URL` 可由灵矽访问，不能使用 localhost/回环地址。构建时无需有效云端密钥，真实模式启动时对所有 profile 只读检查精确 Agent/version/runtime/default model、environment 和 Vault 可用性；任何失败都不监听 HTTP。

| 环境变量 | 用途 |
| --- | --- |
| `AGENTAPI_MODE=http` | 真实执行；`mock` 为固定示例 |
| `AGENTAPI_BASE_URL`、`AGENTAPI_API_KEY` | 上游地址与服务端凭据 |
| `AGENTAPI_AGENT_ID`、`AGENTAPI_AGENT_VERSION` | 预配置 claudecode agent 与固定版本（默认 1） |
| `AGENTAPI_ENVIRONMENT_ID` | 云端环境 |
| `AGENTAPI_VAULT_IDS` | 可选逗号分隔 Vault 引用，供云端 git/gh 使用；无需在设备传凭据 |
| `CONTROL_TOKEN` | 独立控制访问令牌，真实模式必填 |
| `PUBLIC_URL` | 对外 HTTPS 地址，控制工具用它生成完整 `display_url` |
| `LISTEN_ADDR` | 默认 `127.0.0.1:8080`；仅在需要局域网接入时改监听地址 |
| `DEVICE_ID` | 默认设备 ID，语音联调可与绑定的软件设备 ID 一致 |
| `DEV_TOOLS` | 真实模式默认 false；true 才开放开发控制台和命令接口 |
| `DATA_DIR` | 本地静态预览缓存，默认 `.data/results` |

如先用浏览器验证真实编程，可以临时设 `DEV_TOOLS=true`，用受令牌保护的控制台提交需求；这一步不等于真实语音验证。Agent 应将静态网页导出至 `/mnt/session/outputs/`，需要框架时先在沙箱完成构建。

## Runtime profiles

设置 `AGENTAPI_RUNTIME_PROFILES_JSON` 为以下 JSON 对象（也接受 profiles 数组），并移除旧的 `AGENTAPI_AGENT_ID/AGENTAPI_AGENT_VERSION/AGENTAPI_ENVIRONMENT_ID/AGENTAPI_VAULT_IDS`。两套配置并存拒绝启动。最多 16 个 profile，每个 runtime 同时只允许一个 create_enabled profile；版本轮换时给旧版本独立 key、关闭 create_enabled，保留 read_enabled。`AGENTAPI_DEFAULT_RUNTIME` 可覆盖 JSON 的默认值，必须指向可创建 profile。

```json
{
  "default_runtime": "codex",
  "profiles": [{
    "key": "codex-v1",
    "runtime": "codex",
    "agent_id": "agt_<configured>",
    "agent_version": 1,
    "environment_id": "env_<configured>",
    "vault_ids": [],
    "default_model": {"id": "<exact-agent-model>", "effort": "high"},
    "allowed_models": [{"id": "<exact-agent-model>", "effort": "high"}],
    "capabilities": {"continue": true, "interrupt": true, "events": true, "files": true, "preview": true},
    "create_enabled": true,
    "read_enabled": true
  }]
}
```

同样可配置 `claudecode/native`。模型 ID 使用部署真实值，effort 是允许的 low/medium/high/xhigh/max 之一。每个默认 `(id,effort)` 必须在 allowlist；省略 model 使用默认值，显式 model 不改变 Agent 的 system/tools/MCP/skills/Vault。环境与 Vault 默认隔离；跨 runtime 共用必须由双方显式设置 `shared_resources:true`，并各自通过上游归属/可用性校验。Mock 无需密钥，内置三个独立 runtime fixture。

当前 API Key 接口没有可验证任意模型/能力的权威目录。默认模型与精确 Agent version 比较，allowlist 与 capabilities 是部署者明确的策略声明；配置其他模型组合前必须运行真实预检，创建响应仍会核对实际 model/effort。控制服务不会为了启动校验创建收费 Session，也不静默切换模型或退回 Mock。此边界不同于宣称所有 provider 都支持某个模型。

可选真实验收（会创建云端 Session 并消耗额度，普通测试始终不需要密钥）：

```sh
# 已在环境中提供 Key、base URL 和三个 profile 的 JSON；不要提交这些真实配置。
AGENTAPI_LIVE_TEST=1 go test -count=1 -timeout=20m \
  -run '^TestLiveRuntimeProfiles$' -v ./internal/transport/httpapi
```

场景包括三类 runtime 的网页生成、同 Session 修改、运行中 interrupt 后续聊、Files/隔离预览和 Native → Codex 接力；可用 `AGENTAPI_LIVE_EVIDENCE=<本地绝对路径>` 保存脱敏 Session/event/file 元信息。这覆盖控制 MCP 的灵矽参数映射，不替代真实麦克风 ASR/TTS 或物理 AirPlay 验证。

回滚仅关闭非 claudecode profile 的创建能力并选回 claudecode 默认项，保留旧 profile 的读取和历史查询；不迁移/删除 Session，不将旧 task_ref 重指向新任务。进程重启后重新列出获取新引用。

## 本地语音与两个 MCP

控制服务和设备展示程序运行在本机，nginx 合并路径后通过 ngrok 提供灵矽可访问的 HTTPS 入口。也可将控制服务部署到云主机；设备 MCP 仍需在能启动现场浏览器和投屏进程的设备上运行。

准备 nginx、ngrok、Chromium 和设备上可用的 doubletake。仓库不提供 Raspberry Pi 固件，也不内置 doubletake。安装来源和目标平台兼容性需由演示环境确认；仅启动 sender 不能证明接收屏已显示。

按以下顺序在独立终端启动（先取得 ngrok 域名，并同步到 `.env` 的 `PUBLIC_URL`）：

```sh
# 终端一：已载入真实 AgentAPI 环境变量；或使用 AGENTAPI_MODE=mock
make run

# 终端二：设备模拟程序；替换为当前屏幕的配置
AIRPLAY_TARGET=<receiver-ip> AIRPLAY_PASSWORD=<pairing-password> \
BROWSER_BIN=<chromium-path> DISPLAY_ALLOWED_HOSTS=<ngrok-domain> \
./bin/device-display

# 终端三：从仓库根目录启动反代
nginx -c "$PWD/deploy/local/nginx.conf" -p "$PWD/deploy/local"

# 终端四：公网入口
ngrok http 8000
```

`nginx.conf` 将控制 `/mcp`、`/display/`、`/results/` 和 `/preview-assets/` 转到 8080，将 `/device/mcp` 转到 8787。云端控制服务应保留同样的路径和 SSE/WebSocket 转发规则，完整接口见 [protocol.md](protocol.md)。

灵矽智控台配置：

1. 控制 MCP：`https://<ngrok-domain>/mcp`，Header `Authorization: Bearer <CONTROL_TOKEN>`。
2. 设备 MCP：`https://<ngrok-domain>/device/mcp`，当前原型不配置控制令牌。
3. 发现并保存控制工具 `list_tasks/get_task/send_message/new_task` 和设备工具 `cast_screen/scroll_screen/stop_cast/scan_screens`。
4. 按 [协议中的角色配置](protocol.md#灵矽配置与部署) 设置工具使用方式；保存后让语音设备断开并重连。
5. 按 [语音指南](voice.md) 启动并绑定 Mac。控制工具返回 `display_url` 后，智能体必须把完整地址原样交给设备 `cast_screen`。

ngrok 域名会自动附加跳过提示参数，设备 Chromium 默认使用 `LinxiDevice/0.1` User-Agent。页面每 3 秒刷新任务列表，选中任务通过 SSE 显示事件，空闲后尝试预览本轮导出的 HTML。

设备主要配置：

| 环境变量 | 默认值 / 用途 |
| --- | --- |
| `DEVICE_MCP_LISTEN_ADDR` | `127.0.0.1:8787` |
| `BROWSER_BIN` | 空时查找 Chromium |
| `DISPLAY_ALLOWED_HOSTS` | 允许打开的主机列表，建议明确填写控制服务域名 |
| `AIRPLAY_TARGET`、`AIRPLAY_PORT` | 当前接收端 IP 和端口；代码中保留本地模拟值，使用前明确覆盖 |
| `AIRPLAY_PASSWORD`、`AIRPLAY_NAME` | 当前接收端配对密码和显示名，使用前明确覆盖 |
| `SCREEN_WIDTH`、`SCREEN_HEIGHT` | `1920`、`1080` |
| `AIRPLAY_FPS`、`AIRPLAY_BITRATE` | `20`、`3000` |
| `DOUBLETAKE_BIN` | `doubletake` |
| `AIRPLAY_CREDENTIALS` | `/tmp/doubletake/credentials.json`，仅设备本地保留 |
| `BROWSER_DEBUG_PORT` | `9222`，仅本机浏览器控制 |
| `BROWSER_USER_AGENT` | `LinxiDevice/0.1` |

控制 MCP 按每次请求的 `X-Linx-Device-Id` 路由设备；工具发现不要求设备 ID。网页体验端的虚拟设备不能代替已绑定设备执行真实调用。设备 MCP 和公开展示接口属于演示原型：任务索引/展示不要求控制令牌，不要在公开入口展示敏感任务，也不要把控制/设备服务直接当成通用多用户公网服务。

## 检查、停止与排错

健康检查 `GET /healthz` 只检查本机 HTTP，不证明云端可用。先确认健康，再确认 MCP 工具、真实 Session、浏览器画面，最后在接收屏核对像素和交互。逐层验证清单见 [评估说明](evaluation.md)。

反代需关闭 SSE 缓冲/缓存，支持 WebSocket，并保持至少 600 秒读超时。转到 loopback 上游时使用 `$proxy_host`，否则 MCP SDK 的 Host 校验可能返回 403。`nginx.conf` 已提供这些配置。

在相应终端按 Ctrl+C 停止 control、voice、device-display 和 ngrok。nginx 使用同一配置执行 `nginx -s quit -c "$PWD/deploy/local/nginx.conf" -p "$PWD/deploy/local"`。停止画面或本地进程不会自动停止云端 Session；需要停止编程时明确向目标任务发送需求。

| 现象 | 检查 |
| --- | --- |
| MCP 能发现工具但调用 403 | 核对绑定设备 ID、每次请求头和当前语音连接 |
| 调用 401 | 确认控制 MCP Bearer 与 control 的 `CONTROL_TOKEN` 完全相同 |
| 语音使用旧工具 | 保存工具/角色配置后断开重连 |
| 无预览 | 查看本轮事件与 stop_reason，确认静态文件已导出；动态后端或未构建源码无法作为网页预览 |
| sender 重试、屏幕无画面 | 检查接收端地址、密码、在线状态和实际接收画面 |
| 重启后旧链接 404 | 重新列出任务获取新 `task_ref`，不要重新提交旧需求 |
