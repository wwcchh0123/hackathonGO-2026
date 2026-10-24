# 工程约定

## 范围

本项目实现 issue #6 的 Go 控制服务。硬件端技术栈尚未确定，不引入设备框架或 deck-proto。`internal/transport/httpapi/devui` 仅为嵌入式联调页面，不是硬件产品实现。

## 模块边界

- `cmd/control`：配置、依赖组装、启动和退出，不写业务流程。
- `internal/session`：唯一的编程控制入口；异步命令、会话绑定和 interrupt → idle → message 顺序。
- `internal/agentapi`：上游协议和 Mock；不引入设备展示逻辑。
- `internal/events`：原始 JSON 保留、事件 ID 去重、历史和实时订阅。
- `internal/device` / `progress` / `artifacts`：设备快照、一句话摘要、静态产物分别管理。
- `internal/transport`：HTTP/WS/SSE/MCP 编解码和鉴权，业务决策调用 session。

## 接口与安全

- AgentAPI Key 只在服务端环境变量中；CONTROL_TOKEN 与它分开。
- 新任务建新会话，修改/继续复用；不能凭 interrupt 请求成功或旧 idle 事件判断运行时停止。
- `session.status_idle` 不等于成功，按 stop_reason 判断。
- 灵矽对话 ID 与 AgentAPI session id 不混用。
- 原型为单设备、内存状态；不擅自扩展多租户、数据库、权限确认或 agent 执行层。
- 修改通信接口时同步更新 docs/protocol.md 和相关测试。

## Issue 与开发流程

- 每项开发工作必须关联一个 GitHub Issue；开始编码前，先在 Issue 中记录并评审 proposal 和设计。
- Issue 至少应说明背景与目标、范围与不做项、接口或数据流、验收标准、测试计划以及风险和回滚方式；设计结论和后续变更也继续记录在同一个 Issue 中。
- 设计文档、proposal 文档和设计草稿不入库，Issue 是这类内容的记录位置。协议、接口和使用说明等随实现维护的产品文档不属于设计文档，仍按接口约定同步更新。
- proposal 和设计达成结论后再开始实现；PR 必须链接对应 Issue，并说明实现内容、验证命令和结果。

## 测试与覆盖率

- 每个 PR 都必须为变更行为提供必要测试，并确保项目统一口径的测试覆盖率相对目标分支有所增长。
- PR 描述必须记录覆盖率检查命令、目标分支与当前分支的覆盖率结果、覆盖率差值和测试范围；覆盖率没有增长时，补充测试后才能合并。

## 验证与参考

运行 `make check test build coverage`。默认 Mock 不需要外部服务，测试不得依赖真实云密钥。

本机参考仓库：`~/qbox/agentapi`、`/Users/jicarl/qiniu/Xrobot-docs`；仅作协议依据，不能成为本项目的构建路径依赖。
