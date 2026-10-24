# 灵矽语音编程工作站

[![CI](https://github.com/wwcchh0123/hackathonGO-2026/actions/workflows/ci.yml/badge.svg)](https://github.com/wwcchh0123/hackathonGO-2026/actions/workflows/ci.yml)

基于 [issue #6 的整体设计](https://github.com/wwcchh0123/hackathonGO-2026/issues/6)：灵矽云理解语音，Go 控制服务编排七牛 AgentAPI，设备拉流渲染并投屏，大屏仅负责显示。

当前提供 **Go 控制服务、完整模拟链路**，以及已打通真实 AgentAPI 会话、事件流和 Files API 的 HTTP 适配器。硬件端暂不选择技术栈，只定义协议边界；真实 ASR/TTS、设备 MCP 传输和 AirPlay 发送端后续接入。浏览器页面仅用于开发联调，随 Go 二进制嵌入。

## 快速运行

只依赖 Go 1.26+，无需 Node.js、前端构建或云端密钥。

```sh
make install
make dev
```

- [模拟控制台](http://localhost:8080/simulator)：发送消息 / 开个新项目；视图在设备本地切换。
- [模拟设备页](http://localhost:8080/device)：只接收状态和事件，可在另一窗口观察。
- [健康检查](http://localhost:8080/healthz)。默认控制服务仅监听本机。

建议演示：发送“帮我做一个番茄钟网页” → 运行中发送“按钮改成红色”（服务自行打断并复用会话）→ 等待自动显示结果 → 点击结果页按钮 → “开个新项目”验证会话切换。继续和暂停也通过发送原话表达。模拟模式使用固定番茄钟模板，**不实际生成代码**；没有模拟物理语音或投屏。

构建后由一个 Go 进程提供页面和接口：

```sh
make build
make run
# 打开 http://localhost:8080/simulator
```

## 工程结构

```text
cmd/control/               配置、依赖组装、HTTP 启动与优雅退出
internal/
  agentapi/                云端执行接口、Anthropic Go SDK 适配、模拟执行器
  session/                 单设备会话绑定、异步命令队列与执行顺序
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

云端 MCP 按 [issue #8](https://github.com/wwcchh0123/hackathonGO-2026/issues/8) 提供 `send_message`、`start_new_project`、`get_status` 三个工具，快速返回一句中文，不返回结构化快照。Mock 用于无密钥测试；HTTP 模式会启动前只读校验 agent/environment，并实际创建 Session、发送事件、消费 SSE 和下载 Files API 产物。

在灵矽后台配置 SSE URL `https://你的服务域名/mcp`；POST 调用时平台带入 `X-Linx-Device-Id`，须与 `DEVICE_ID` 一致，GET 建流不要求该头。`X-Linx-Session-Id` 仅用于日志和五秒去重；MCP GET / POST 均使用独立的 CONTROL_TOKEN Bearer 鉴权。当前原型仅支持一个配置的设备。反向代理允许 WebSocket、关闭 SSE 缓冲，读超时至少十分钟；不启用要求客户端回复 ping 的 MCP KeepAlive。配置和智能体提示词见 [协议文档](docs/protocol.md#灵矽配置与部署)。

让 agent 把最终静态网站（`index.html` 和相对路径资源）导出到 `/mnt/session/outputs/`，服务端在 `end_turn` 后经 Files API 下载并托管。不启动任意沙箱端口，不执行构建，也不解压归档文件。需要 Node/Vite 的项目应由 agent 在沙箱构建后导出静态文件。

手机访问结果需要将 `PUBLIC_URL` 改为手机可访问的服务地址；若从局域网访问，也要相应设置 `LISTEN_ADDR`。二维码等硬件展示细节后续由设备端实现。

## 验证

```sh
make check test build
make coverage
```

包含 gofmt 检查、Go vet、Go race 测试、二进制构建和本地覆盖率报告。自动测试只使用本地临时目录和内存/HTTP 假上游，无外部密钥；手工真实联调使用私有配置，不纳入测试。覆盖打断后的等待顺序、同会话修改/继续、新会话替换、SSE 回放边界、原始 JSON 保留、MCP → 服务端 WebSocket/SSE → 结果托管。GitHub Actions 运行同一组检查，覆盖率结果输出到日志，无需 Codecov token。

实现设计见 [MCP 到 AgentAPI 设计](docs/mcp-agentapi-design.md)，接口约定见 [协议文档](docs/protocol.md)。外部依据为本机 `~/qbox/agentapi` 与 `/Users/jicarl/qiniu/Xrobot-docs`，无需将它们作为构建依赖。
