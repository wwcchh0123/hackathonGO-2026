# 硬件接入边界（技术栈待定）

本次只初始化 Go 控制服务和协议。浏览器联调页不作为硬件产品实现，也不限定硬件使用浏览器或 JavaScript。

## 三条独立连接

| 连接 | 方向 | 责任 |
| --- | --- | --- |
| 灵矽设备协议 | 硬件 ⇄ 灵矽云 | 音频输入/播放、ASR 文本、TTS 打断、设备端 MCP |
| 设备控制通道 | 硬件 ⇄ 本项目控制服务 | WebSocket 接收完整状态：AgentAPI session id、状态、摘要、结果地址（无 view 字段） |
| 编程事件流 | 本项目控制服务 → 硬件 | 按 AgentAPI session id 拉取 SSE，先历史后实时，自行渲染 |

`X-Linx-Session-Id` 是灵矽对话 ID，**不是**本服务下发的 AgentAPI `session_id`。设备 ID 是两条链路关联的依据，不能混用会话 ID。

## 语音模块

依据 `Xrobot-docs/docs/xrobot/platform/websocket.md`：设备完成平台绑定，握手携带 Authorization、Device-Id、Client-Id、Protocol-Version，发送 hello 并等待回应；声明 `features.mcp=true` 后支持端侧 MCP。音频格式、采样率和帧时长按 hello 协商。

音频与模型工具选择留在灵矽链路。控制服务不接收原始音频，也不自建 ASR/TTS。平台地址优先通过 OTA 返回值获取，不在本工程硬编码。

## 投屏模块与设备端 MCP

依据 `Xrobot-docs/docs/xrobot/mcp/hardware-mcp.md`，MCP JSON-RPC 封装在设备协议消息中：

```json
{
  "type": "mcp",
  "payload": {"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}
}
```

建议设备提供的工具合约：

| 工具 | 入参 | 返回/行为 |
| --- | --- | --- |
| scan_screens | `{}` | 本地可投屏目标列表，每项含 id/name |
| cast_screen | `{ "screen_id": "本地发现的ID" }` | 启动向指定屏幕镜像设备渲染画面 |
| stop_cast | `{}` | 停止投屏 |

这些是项目约定的待实现工具，**不是灵矽内置工具**，也不应在云端控制服务上操作局域网屏幕。发现和 AirPlay 实现由目标硬件确定后完成。

## 展示模块

1. 从控制服务建立带鉴权的 WebSocket，收到完整快照。
2. `session_id` 改变时关闭旧事件流、清空当前展示并订阅新会话。
3. SSE 数据保留上游原始 JSON；按事件 ID 去重，按 type 渲染，未知类型可以忽略或保留为调试信息。
4. idle 不关闭连接；一轮结束不代表所有任务成功，需看 stop_reason。
5. 设备自主渲染过程 / 代码 / 终端；result_url 出现或变化时自动打开结果，新轮次清空 result_url 后恢复过程展示。云端不提供画面切换指令，也不提供投屏工具；本地视图选择不发控制命令。二维码由硬件渲染实现生成。
6. 断线重建 WebSocket 获取快照，重建 SSE 回放历史；不要由硬件创建/中断/修改 AgentAPI 会话。

本地验证时 `DEV_TOOLS=true` 的 `/device` 模拟以上流程；`/simulator` 额外模拟云端意图。生产可关闭这两个页面，设备协议仍可用。

## 云端工具边界

灵矽 LLM 通过旧版 SSE 调用 send_message / start_new_project / get_status，控制服务按执行时 Provider 状态决定会话复用或打断，不让 LLM 判断运行时状态。GET /mcp 仅校验控制令牌，不依赖设备头；POST /mcp/message 每次校验设备头（仅 mock 可缺省）。X-Linx-Session-Id 仅作日志和五秒去重，不能用于订阅编程事件。详见 [接口协议](protocol.md)。

工具结果仅一句中文，不含 structuredContent、链接或内部 ID；结果地址仅通过设备快照展示。get_status 为被动查询，服务不向灵矽主动推送播报。语音硬件、设备端 MCP 与真实投屏不在本次实现范围。
