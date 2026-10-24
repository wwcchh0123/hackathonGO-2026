# 设计理解与接入依据

## 来源
- [架构设计 issue #6](https://github.com/wwcchh0123/hackathonGO-2026/issues/6)，2026-10-24 读取正文，暂无评论。
- 本机 `/Users/jicarl/qbox/agentapi` 的协议和示例仅作为接口依据，不修改该仓库。

## 边界
- 灵矽云：ASR/LLM/TTS，通过云 MCP 调工具。
- 控制服务：异步命令、设备与会话绑定、interrupt/idle/message 排序、SSE 中转、摘要、结果。
- 设备：WebSocket 接收快照，按 session id 订阅原始 SSE，渲染和投屏。API Key 只在控制服务。
- 大屏：被动显示，不部署应用。
- 新任务建会话；修改和继续复用会话。单设备原型，内存状态。

## 验证边界
- 本地模拟需要覆盖主流程及断线回放；真实云端和 AirPlay 需实际凭据及硬件联调。
- 用户明确无需处理 deck-proto，硬件端先不定技术栈。模拟页面只作联调工具，使用 Go embed 原生静态资源，不需要 Node。

## 已核实的协议
- AgentAPI 创建会话引用 agent + environment_id，消息走 user.message / input.parts，打断走 user.interrupt。
- /events/stream 先回放历史再推实时，并跨 idle 保持连接；不能将旧 idle 当作本轮完成。
- session.status_idle 的 stop_reason.type 区分 end_turn / interrupted / requires_action / retries_exhausted / budget_reached。
- Files API 的 scope_id 为 session id；最终文件需写入 /mnt/session/outputs/。
- 灵矽云 MCP 需要 GET /mcp、POST /mcp/message，以及 X-Linx-Device-Id 请求头。
- 使用官方 MCP Go SDK v1.8.0，支持灵矽所需的旧 SSE 传输。

## 验证
- make check test build 已通过，包括 race 和官方 SDK 的真实 MCP 客户端回环测试；调整为 Go-only 后重新执行。
- 真实 AgentAPI、真实语音与物理 AirPlay 尚无联调凭据/硬件；不能等同于本地模拟已通过。
- 最终浏览器验证：开始→暂停→原会话修改→预览，按钮颜色 rgb(230,77,92)，交互正常；模拟设备页无任务控制按钮；刷新恢复结果；控制台无错误。
- 最终工程不包含 React/TypeScript/npm 工程；Go embed 联调资源随二进制部署。
