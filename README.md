# 灵矽语音编程工作站

[![CI](https://github.com/wwcchh0123/hackathonGO-2026/actions/workflows/ci.yml/badge.svg)](https://github.com/wwcchh0123/hackathonGO-2026/actions/workflows/ci.yml)

基于 [issue #6 的整体设计](https://github.com/wwcchh0123/hackathonGO-2026/issues/6)：灵矽云理解语音，Go 控制服务编排七牛 AgentAPI，设备拉流渲染并投屏，大屏仅负责显示。

当前提供 **Go 控制服务、完整模拟链路、Mac 软件语音终端**，以及通过 Anthropic Go SDK 接入真实 AgentAPI 会话、事件流和 Files API 的 HTTP 适配器。语音终端可真实接入灵矽的 ASR / 智能体 / TTS；设备 MCP 和 AirPlay 发送端后续接入。两个程序的原生 HTML/JS 页面分别嵌入 Go 二进制，无需前端构建。

## Mac 语音客户端

首次安装、设备绑定、日常启动、参数配置和排错见 [Mac 语音客户端启动指南](docs/voice-startup.md)。

在 Mac 上运行一个独立软件设备，使用浏览器麦克风和扬声器连接灵矽智能体：

```sh
brew install opus pkgconf
make voice
# 使用 Chrome 打开 http://127.0.0.1:8090
```

首次启动会显示激活码。登录 [灵矽智控台](https://xrobo.qiniu.com/)，在目标智能体下添加该设备；绑定后点击「连接智能体」→「开始说话」，允许麦克风，说完点击「说完了，发送」。页面展示识别文字并播放智能体语音，可点击「停止播报」打断回复。

设备身份自动保存在 `.data/voice/identity.json`，重启会复用。此程序无需 AgentAPI Key，也无需先运行控制服务。当前先打通语音；编程工具仍由灵矽云端 MCP 对接控制服务。详细安装、协议与排错见 [语音客户端说明](docs/voice-client.md)。

## 控制服务模拟链路

只依赖 Go 1.26+，无需 Node.js、前端构建或云端密钥。

```sh
make install
make dev
```

- [模拟控制台](http://localhost:8080/simulator)：列出任务、选择消息目标、新建独立任务；展示目标单独选择。
- [模拟设备页](http://localhost:8080/device)：选择历史任务，按需接收状态和事件，可在另一窗口观察。
- [中心管理页](http://localhost:8080/admin)：从“待处理 / 设备 / 任务”三个入口查看设备健康、跨设备任务和排障详情（只读）。
- [健康检查](http://localhost:8080/healthz)。默认控制服务仅监听本机。

建议演示：新建 A → 在“展示任务”选择 A → 在“消息目标”选择 A 并发送修改（等待真实停止后复用）→ 新建 B（A 不被停止）→ 切换展示 A/B → 空闲后点击“看效果”。查询和展示选择不会改变消息目标。模拟模式使用固定番茄钟模板，不实际生成代码。

构建后由一个 Go 进程提供页面和接口：

```sh
make build
make run
# 打开 http://localhost:8080/simulator
```

## 工程结构

```text
cmd/control/               配置、依赖组装、HTTP 启动与优雅退出
cmd/voice/                 Mac 软件语音终端入口，独立监听 8090
internal/
  linx/                    持久化设备身份、OTA 激活、灵矽 WebSocket 协议
  voice/                   本地语音桥、单窗口连接、嵌入式麦克风/播放页面
  voicecodec/              可选的 libopus 编解码，使用 opus 构建标签
  agentapi/                云端执行接口、Anthropic Go SDK 适配、模拟执行器
  session/                 任务引用、历史查询、目标路由和异步命令结果
  controlplane/            中心设备注册、按设备路由和管理端聚合
  events/                  原始事件日志、去重、历史回放与实时分发
  device/                  可订阅的设备状态快照，与传输方式解耦
  progress/                按事件和停止原因生成一句话摘要
  artifacts/               静态 Web 产物托管
  config/                  环境变量配置与验证
  transport/mcp/           灵矽云端 MCP 工具，官方 Go SDK
  transport/httpapi/       HTTP、设备 WebSocket、SSE、访问鉴权
    devui/                 原生 HTML/JS 联调页面，go:embed，无构建步骤
docs/                      接口协议与外部接入约定
```

设备不持有 AgentAPI 凭据，也不包含编程执行逻辑。`DEV_TOOLS=false` 同时关闭模拟页面、静态联调资源和 `/api/commands`；硬件通过独立的设备协议接入。

中心管理页使用与控制台相同的 `CONTROL_TOKEN`，默认打开“待处理”视图，也可切换到设备列表和跨设备任务列表。全局搜索支持设备 ID、task_ref、AgentAPI session_id 和任务标题；任务详情始终带所属设备。页面只读展示进程内设备注册表和按 `metadata.device_id` 归属的 AgentAPI 任务。设备通过受鉴权的 MCP 请求被发现并刷新 `last_seen`；“在线”只表示最近触达 control，不是硬件心跳。注册表和 `task_ref` 仍是内存状态，重启后由设备重新触达并重新列出历史任务。该阶段 proposal 和实现记录在 [Issue #23](https://github.com/wwcchh0123/hackathonGO-2026/issues/23)。

## 接入真实 AgentAPI

复制 `.env.example` 到 `.env`，填入真实配置。启动命令不会自动读取 `.env`：

```sh
cp .env.example .env
# 编辑 .env：AGENTAPI_MODE=http，补齐 AgentAPI 的 host/key、agent、environment 和 CONTROL_TOKEN；可选固定 AGENTAPI_AGENT_VERSION
# 生产接入设 DEV_TOOLS=false
set -a
source .env
set +a
make build run
```

`AGENTAPI_AGENT_ID` 必须指向预先配置好的 **claudecode** 智能体；当前 AgentAPI 文档推荐的 Go SDK 版本为 `github.com/anthropics/anthropic-sdk-go v1.72.0`，`AGENTAPI_ENVIRONMENT_ID` 为云端环境。控制服务不会替你创建 agent/environment 或覆盖其模型。API Key 只由服务端读取，浏览器使用另一个 `CONTROL_TOKEN` 建立 HttpOnly 会话 Cookie；云 MCP 调用使用 `Authorization: Bearer <CONTROL_TOKEN>`。

云端 MCP 按 [issue #18](https://github.com/wwcchh0123/hackathonGO-2026/issues/18) 提供 `list_tasks`、`get_task`、`send_message`、`new_task` 四个工具，替换旧 `get_status/start_new_project`。成功结果为 JSON text content；异步回执返回 `task_ref/command_id`，用详情查询确认提交结果。列表按当前授权身份查询单页，历史任务不依赖本地缓存；实时 SSE 和预览下载均由设备按需触发。`AGENTAPI_VAULT_IDS` 可配置 GitHub credential 的 Vault 引用，由 Agent 在授权范围自行定位仓库。

在灵矽后台配置 MCP URL `https://你的服务域名/mcp`。同一地址支持当前灵矽使用的 Streamable HTTP（`POST /mcp`）和旧版 SSE（`GET /mcp` → `POST /mcp/message`）；后台即使将字段标为 SSE URL，也使用此地址。管理端初始化和列举工具无需设备 ID；真正调用工具时，每次请求的 `X-Linx-Device-Id` 决定设备路由，control 会在内存中登记该设备并隔离其任务引用。`DEVICE_ID` 仍是启动时的默认设备，适合单设备部署。网页体验的虚拟设备可发现工具，但不能替代已绑定的 Mac 设备调用工具。`X-Linx-Session-Id` 用于对话内默认发送目标、日志和五秒去重；所有 MCP HTTP 请求均使用独立的 CONTROL_TOKEN Bearer 鉴权。反向代理允许 WebSocket、关闭 SSE 缓冲，读超时至少十分钟；不启用要求客户端回复 ping 的 MCP KeepAlive。配置和智能体提示词见 [协议文档](docs/protocol.md#灵矽配置与部署)。

让 agent 把最终静态网站（`index.html` 和相对路径资源）导出到 `/mnt/session/outputs/`，设备明确请求预览时，服务端经 Files API 有界下载并托管。不启动任意沙箱端口，不执行构建，也不解压归档文件。需要 Node/Vite 的项目应由 agent 在沙箱构建后导出静态文件。

手机访问结果需要将 `PUBLIC_URL` 改为手机可访问的服务地址；若从局域网访问，也要相应设置 `LISTEN_ADDR`。二维码等硬件展示细节后续由设备端实现。

## 验证

```sh
make check test build
make coverage
# 改动语音模块时（需要 libopus）
make voice-test
# 浏览器重采样测试（仅开发测试需要 Node.js）
node --test internal/voice/test/pcm.test.mjs
```

包含 gofmt 检查、Go vet、Go race 测试、二进制构建和本地覆盖率报告。自动测试只使用本地临时目录和内存/HTTP 假上游，无外部密钥；手工真实联调使用私有配置，不纳入测试。覆盖打断后的等待顺序、同会话修改/继续、独立新任务与跨任务隔离、SSE 回放边界、原始 JSON 保留、MCP → 服务端 WebSocket/SSE → 结果托管。GitHub Actions 运行同一组检查，覆盖率结果输出到日志，无需 Codecov token。

实现接入说明见 [MCP 到 AgentAPI 设计](docs/mcp-agentapi-design.md)，接口约定见 [协议文档](docs/protocol.md)。外部依据为本机 `~/qbox/agentapi` 与 `/Users/jicarl/qiniu/Xrobot-docs`，无需将它们作为构建依赖。
