# AgentPulse 文档索引

欢迎查阅 AgentPulse 项目技术文档。本项目旨在提供一套轻量、终端优先的跨机器 AI Agent 监控与派活体系。

## 文档目录

- [MQTT 传输协议规范 (v1)](mqtt-protocol.md)：跨机通信主题树、QoS/保留消息约定、消息 Payload 契约、`store.Store` 映射指南、安全加固与 TLS 运维设计（v0.4.0 落地端到端 TLS 传输与证书鉴权）。
- [Mosquitto ACL 最小权限样例配置](mosquitto-acl.example)：Operator 监控调度端与 Worker 节点按机器主题隔离的最小权限 ACL 规则样例。
- [本机裸 Agent CLI 进程自动挂接机制 (Bare CLI Attach)](bare-cli-attach.md)：本机直接运行 agy/opencode 时，守护进程自动发现、生命周期追踪、流式日志捕获与 JOBS 大盘挂接设计。
- [macOS App 打包指南 (Apple Silicon)](macos-app.md)：构建脚本、Bundle 内部结构、双击交互 Launcher 原理及 Gatekeeper 说明。
