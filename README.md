# agentpulse-cli

CLI to monitor agy and OpenCode dispatch/progress across machines via MQTT.

## 架构与传输协议

- [AgentPulse MQTT 协议设计规范 (v1)](docs/mqtt-protocol.md)（**协议草案，实现后置**）：定义跨节点轻量 Thin Wrapper 与 CLI 监控端之间基于 MQTT 的主题树、QoS 策略、消息 JSON 契约、`store.Store` 映射指南与安全运维规范。

