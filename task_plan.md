# 项目初始化计划

## 目标
按 issue #6 初始化模块清晰、能编译、可在无外部凭据时演示完整链路的工程。

## 阶段
- [x] 读取设计、检查空仓库与环境
- [x] 核实 AgentAPI / MCP 合约，确定模块边界
- [x] 实现控制服务、设备展示与模拟适配器
- [x] 编译、关键流程测试、浏览器验证、交付文档

## 决策
- 以 issue #6 云端 AgentAPI 执行为准。
- 用户确认 Go，硬件端技术栈暂不考虑；正式工程只用 Go。浏览器界面降为 Go embed 的原生 HTML/JS 联调工具，不引入 Node 构建依赖。
- 模拟和真实依赖使用统一接口；不将未验证的硬件能力标为已完成。

## 待确认
- 用户明确无需对齐 deck-proto。硬件与投屏实现后续确认。
- AgentAPI 依据 ~/qbox/agentapi，灵矽依据 /Users/jicarl/qiniu/Xrobot-docs，已核对云端 SSE/请求头和端侧 MCP 消息封装。
- AgentAPI 部署地址、agent id、凭据（不得写入仓库）。

## 错误与处理
- GitHub 页面匿名访问 404：通过已登录的 gh API 成功读取 issue。
- 初次前端构建缺失 CSS 类型声明：增加 Vite client 类型引用。
- 初次查找 Go 模块使用了默认路径：改用 go env GOMODCACHE 返回的实际路径。
- HTTP 协议测试发现 MCP 无方法路由与 GET 静态回退冲突：将 MCP 注册为明确的 GET /mcp 和 POST /mcp/message。
- 浏览器首轮过早发起看效果导致预览未就绪：联调页在结果 URL 就绪前禁用看效果；服务端仍明确返回未就绪错误。

## 当前状态
完成。Go-only 的 make check test build 通过；浏览器验证开始、暂停、同会话修改、红色按钮预览交互、只读模拟设备页和刷新恢复均通过，无控制台错误。
