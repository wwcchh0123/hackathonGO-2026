# 灵矽语音编程工作站

基于 [issue #6 的整体设计](https://github.com/wwcchh0123/hackathonGO-2026/issues/6)：灵矽云理解语音，Go 控制服务编排七牛 AgentAPI，设备拉流渲染并投屏，大屏仅负责显示。

当前提供 **Go 控制服务、完整模拟链路**，以及基于本机 AgentAPI 实际合约的 HTTP 适配器。硬件端暂不选择技术栈，只定义协议边界；真实 ASR/TTS、设备 MCP 传输和 AirPlay 发送端后续接入。浏览器页面仅用于开发联调，随 Go 二进制嵌入。

## 快速运行

只依赖 Go 1.26+，无需 Node.js、前端构建或云端密钥。

```sh
make install
make dev
```

- [模拟控制台](http://localhost:8080/simulator)：文字模拟灵矽工具调用，操作开始 / 暂停 / 修改 / 继续 / 切视图。
- [模拟设备页](http://localhost:8080/device)：只接收状态和事件，可在另一窗口观察。
- [健康检查](http://localhost:8080/healthz)。默认控制服务仅监听本机。

建议演示：开始“帮我做一个番茄钟网页” → 运行中暂停 → 输入“按钮改成红色”并点击修改需求 → 等本轮结束 → 看效果 → 点击结果页按钮。模拟模式使用固定番茄钟模板，**不实际生成代码**；没有模拟物理语音或投屏。

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
  agentapi/                云端执行接口、真实 HTTP/SSE 适配、模拟执行器
  session/                 单设备会话绑定、异步命令队列与执行顺序
  events/                  原始事件日志、去重、历史回放与实时分发
  device/                  可订阅的设备状态快照，与传输方式解耦
  progress/                按事件和停止原因生成一句话摘要
  artifacts/               静态 Web 产物托管
  config/                  环境变量配置与验证
  transport/mcp/           灵矽云端 MCP 工具，官方 Go SDK
  transport/httpapi/       HTTP、设备 WebSocket、SSE、访问鉴权
    devui/                 原生 HTML/JS 联调页面，go:embed，无构建步骤
docs/                      架构与协议、外部接入约定
```

设备不持有 AgentAPI 凭据，也不包含编程执行逻辑。`DEV_TOOLS=false` 同时关闭模拟页面、静态联调资源和 `/api/commands`；硬件通过独立的设备协议接入。

## 接入真实 AgentAPI

复制 `.env.example` 到 `.env`，填入真实配置。启动命令不会自动读取 `.env`：

```sh
cp .env.example .env
# 编辑 .env：AGENTAPI_MODE=http，补齐 AgentAPI 的四项配置和 CONTROL_TOKEN
# 生产接入设 DEV_TOOLS=false
set -a
source .env
set +a
make build run
```

`AGENTAPI_AGENT_ID` 必须指向预先配置好的 **claudecode** 智能体，`AGENTAPI_ENVIRONMENT_ID` 为云端环境。控制服务不会替你创建 agent/environment 或覆盖其模型。API Key 只由服务端读取，浏览器使用另一个 `CONTROL_TOKEN` 建立 HttpOnly 会话 Cookie；云 MCP 调用使用 `Authorization: Bearer <CONTROL_TOKEN>`。

在灵矽后台配置 SSE URL `https://你的服务域名/mcp`；平台带入 `X-Linx-Device-Id`，须与 `DEVICE_ID` 一致。当前原型仅支持一个配置的设备。真实环境必须配齐鉴权；反向代理应允许 WebSocket、SSE 长连接并关闭 SSE 缓冲。

让 agent 把最终静态网站（`index.html` 和相对路径资源）导出到 `/mnt/session/outputs/`，服务端在 `end_turn` 后经 Files API 下载并托管。不启动任意沙箱端口，不执行构建，也不解压归档文件。需要 Node/Vite 的项目应由 agent 在沙箱构建后导出静态文件。

手机访问结果需要将 `PUBLIC_URL` 改为手机可访问的服务地址；若从局域网访问，也要相应设置 `LISTEN_ADDR`。二维码等硬件展示细节后续由设备端实现。

## 验证

```sh
make check test build
```

包含 gofmt 检查、Go vet、Go race 测试和二进制构建。测试只使用本地临时目录和内存/HTTP 假上游，无外部密钥，通常数秒完成；依赖缺失直接失败。覆盖打断后的等待顺序、同会话修改/继续、新会话替换、SSE 回放边界、原始 JSON 保留、MCP → 设备 WebSocket/SSE → 结果托管。GitHub Actions 运行同一命令。

详细设计和后续接入点见 [架构说明](docs/architecture.md)、[接口协议](docs/protocol.md)、[硬件接入边界](docs/device-contract.md)。外部依据为本机 `~/qbox/agentapi` 与 `/Users/jicarl/qiniu/Xrobot-docs`，无需将它们作为构建依赖。
