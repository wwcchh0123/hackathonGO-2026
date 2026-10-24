# MCP 到 AgentAPI 实现接入说明

设计和评审记录在 [Issue #18](https://github.com/wwcchh0123/hackathonGO-2026/issues/18)。本文件只说明已实现接口的使用；完整字段和限制见 [protocol.md](protocol.md)。

1. 服务端配置 AgentAPI host/key、claudecode agent/version、cloud environment；GitHub 能力可配置 AGENTAPI_VAULT_IDS 引用。CONTROL_TOKEN 与上游 API Key 独立。
2. 灵矽重新发现 list_tasks/get_task/send_message/new_task 四个工具，按 protocol.md 更新工具提示词。返回 JSON text，灵矽读取字段后口语转述。
3. new_task 返回 task_ref/command_id。用 get_task 查询该 command_id；confirmed 表示上游接受需求，不能声称执行已完成。Create/Send unknown 时先查询，不重复调用写入工具。
4. 历史任务通过 list_tasks 分页定位，明确 task_ref 后 send_message。对 running/rescheduling 的修改先 interrupt，再通过实时 Status 确认 idle，最后在同 Session 发送。terminated/archived/不兼容任务明确拒绝，不静默新建。
5. 设备通过 /api/tasks 查询，再 POST /api/device/view 明确选择显示。展示和对话发送目标独立。设备显式连接 /api/tasks/{task_ref}/events 才消费 SSE，断连释放观察；详情查询不创建订阅。
6. 静态产物仍由 Agent 导出到 /mnt/session/outputs/。详情只返回 Files 元信息，POST /api/tasks/{task_ref}/preview 才下载并托管。历史任务无需先被当前进程观察。

联调页面仍是开发工具，不是 Mac 语音客户端；硬件显示/AirPlay 由 #12 负责。四工具查询不承担语音切屏，设备界面负责明确展示选择。

验证命令：`make check test build coverage`。自动测试用 Mock 和本地 HTTP 假上游，无真实密钥；真实 Session、Vault、GitHub 和灵矽四工具格式已按脱敏联调记录验证；真实 Mac 语音端需要设备绑定后另行验证。PR 需记录与目标分支统一口径的覆盖率差值。回滚代码和对应灵矽工具配置，不删除/中断云端任务或 Vault。
