# AgentPulse CLI

Cross-machine monitor CLI for **Google Antigravity (`agy`)** and **OpenCode** autonomous agent workflows.

Licensed under the [MIT License](LICENSE).

---

> [!IMPORTANT]
> **MQTT Store**
> Broker settings come from `config.yaml` or explicit flags such as `--mqtt-url`.
> Commands that read the network store exit with an error when no broker is configured and tell you to run `agentpulse config`.

---

## ⚡ Architecture

```
                    ┌────────────────────────────┐
                    │    AgentPulse CLI / TUI    │
                    │  (bubbletea / lipgloss)    │
                    └─────────────┬──────────────┘
                                  │
                                  ▼
                    ┌────────────────────────────┐
                    │     store.Store (API)      │
                    └─────────────┬──────────────┘
                                  │
                                  ▼
                    ┌────────────────────────────┐
                    │   internal/store/mqtt      │
                    └─────────────┬──────────────┘
                                  │
                                  ▼
                    ┌────────────────────────────┐
                    │        MQTT Broker         │
                    │    (agy / OpenCode bus)    │
                    └────────────────────────────┘
```

- **MQTT Store**:
  - `internal/store/mqtt.Store` connects to the broker configured in `config.yaml` or via `--mqtt-url` (for example `tls://broker.example:8883` or `tcp://127.0.0.1:1883`).
  - Subscribes to `agentpulse/v1/machines/+/status`, `.../jobs/+/status`, and `.../jobs/+/logs`.
  - In-memory aggregation with active job log ring buffer (~500 lines) and bounded event streaming (1024 cap) with non-blocking backpressure mitigation.
  - Automatic cold-start settle (~200ms configurable via `--mqtt-settle-timeout`) ensuring retained node and job states are populated on one-shot CLI commands (`list`, `logs`).
  - There is no in-memory simulation mode and no `AGENTPULSE_*` environment fallback. Without a broker URL the command returns: `未配置 MQTT Broker URL。请运行 'agentpulse config' 初始化配置，或通过 --mqtt-url 指定`.

---

## 🛠 Build & Installation

Prerequisites: **Go 1.22+**

```bash
# Clone the repository
git clone https://github.com/mayingjie198448/agentpulse-cli.git
cd agentpulse-cli

# Build binary
go build -o agentpulse ./cmd/agentpulse

# Verify build
./agentpulse version
```

### 发版构建 (Release Binaries + SHA256SUMS)

0.5.0 起，项目 Release 官方分发资产统一为各平台**裸二进制与 SHA256SUMS 校验文件**，废除 `.app` 必选项：

```bash
# 一键交叉编译全平台二进制并生成 SHA256SUMS
bash scripts/build-release.sh
```
产物将输出至 `dist/`：
- `agentpulse-0.7.0-darwin-arm64` / `agentpulse-0.7.0-darwin-amd64`
- `agentpulse-0.7.0-linux-arm64` / `agentpulse-0.7.0-linux-amd64`
- `agentpulse-0.7.0-windows-amd64.exe`
- `SHA256SUMS`

### 可选：本地打包 Mac .app（Apple Silicon）

项目保留了用于本地 macOS 桌面双击体验的打包脚本（非发版必选）：

- **构建命令**：`bash scripts/build-macos-app.sh`
- **产物路径**：`dist/AgentPulse.app`
- 详细说明参见 [macOS App 打包设计文档](docs/macos-app.md)。

---

## 🚀 Usage (0.7.0 命令面体系)

### 1. 常驻单进程双角色 (`agentpulse`)

直接运行 `agentpulse`（无子命令）即启动常驻单进程：
- **本机上报角色**：自动向 MQTT Broker 上报心跳（LWT 守护）、广播本机 Announce 拓扑，并在后台监听与串行执行下发到本机的 dispatch 作业；
- **全网监控角色**：订阅全网机器心跳与任务流式事件，在终端拉起 Cyber 风格实时监控大盘（TUI，在非 TTY 管道中自动退化为纯文本快照）。

```bash
# 启动常驻双角色守护服务（默认读取 ~/.config/agentpulse/config.yaml）
agentpulse

# 强制以纯文本快照模式运行
agentpulse --plain

# 指定特定配置文件
agentpulse --config /path/to/custom-config.yaml
```

---

### 2. 交互式配置向导 (`config`)

引导式完成客户端与 Broker 配置，写入 `config.yaml`：
- **步骤 1**：设定本机机器标识 `machine_id`；
- **步骤 2**：配置 MQTT Broker 地址与鉴权信息（支持 TLS / 账号密码）；
- **步骤 3**：配置本机 Agent CLI 路径、别名及启用状态（`agy`、`opencode`、`codex` 等）；
- **保存与自检**：安全保存 0600 权限 yaml 配置文件并立即运行连通性与本地 CLI 探测自检（复用 `doctor` 逻辑）。已存在配置时支持分段编辑（Broker / Agents / machine_id）并回车保留当前值。

```bash
# 运行配置向导
agentpulse config
```

- **配置文件规范**：
  - 默认路径：`~/.config/agentpulse/config.yaml`（Windows 为 `%AppData%\agentpulse\config.yaml`）
  - 安全权限：目录权限 `0700`，文件权限 `0600`
  - 仅支持 `--config <path>` 显式覆盖；不再使用环境变量指向配置路径。
  - MQTT、机器标识、状态文件等产品配置只来自该文件与显式 CLI 标志（如 `--mqtt-url`、`--config`）。不读取 `AGENTPULSE_*` 环境变量，启动时也不再扫描或告警。
- **配置示例 Schema**：
  ```yaml
  version: 1
  machine_id: macbook-pro
  broker:
    url: tls://broker.example:8883
    username: agentpulse
    password: "..."
    tls: true
  agents:
    - id: agy
      path: ""
      alias: [agy]
      enabled: true
    - id: opencode
      path: ""
      alias: [op, opencode]
    - id: codex
      path: ""
      alias: [codex]
  ```

---

### 3. 系统诊断与全网拓扑 (`doctor`)

对当前终端环境、配置文件、MQTT Broker 连通性以及本机 Agent CLI 进行全面探测。诊断完成后自动向 Broker 同步最新 Announce 广播，并拉取全网所有机器与 CLI 拓扑汇总为扁平表格：

```bash
agentpulse doctor
```

---

### 4. 远程任务调度 (`dispatch`)

向 MQTT 消息中枢下发作业调度指令：
- **自动选机与门禁**：若未指定 `--machine`，自动扫描网内唯一在线且声明支持该 Agent CLI 的机器；派发时进行门禁核验（目标节点心跳新鲜 + 对应 `agents[].ok: true`）。
- **实时跟踪与流式日志**：支持 `--follow` 直播远程标准输出与错误流，直至任务结束。

```bash
# 自动匹配支持 agy 的在线机器并派发
agentpulse dispatch agy -- run --prompt "优化数据库连接池"

# 指定目标机器派发任务
agentpulse dispatch --machine mac-studio op -- -p "重构认证模块"

# 派发并实时跟踪进度与输出日志
agentpulse dispatch --machine mac-studio --follow agy -- -p "运行单元测试集"

# 强制派发（绕过在线门禁）
agentpulse dispatch --machine gpu-box --force agy -- -p "预热模型"
```

---

### 5. 全网任务查看 (`jobs`)

从消息存储中检索并汇总跨机器的全部 AI Agent 任务（运行中、排队中、已完成与失败任务）：

```bash
# 表格展示全网任务列表
agentpulse jobs

# 输出机器可读的 JSON 格式
agentpulse jobs --json
```

---

### 6. 实时监控看板与单任务监听 (`watch`)

- **无参数**：启动交互式 Cyber TUI 监控大盘（快捷键：`Tab` 切换面板、`j`/`k` 选择任务、`Space` 暂停/恢复、`q` 退出）；
- **带任务 ID**：定向追踪该任务的实时进度与控制台日志流，直到任务完成。

```bash
# 启动全网 TUI 监控大盘
agentpulse watch

# 监听指定任务的实时日志流
agentpulse watch job-agy-01
```

---

### 7. 兼容子命令 (Advanced / Compatibility)

为保持版本平滑过渡，底层子命令保留为隐藏命令：
- `agentpulse listen`：单跑边缘监听守护进程（已收敛至主命令 `agentpulse`）
- `agentpulse wrap`：本地执行封装器（支持 `--stub` 模拟模式）
- `agentpulse list` / `agentpulse status` / `agentpulse logs`：历史兼容查询接口

---

### 8. 多机怎么跑 (Multi-Machine Quickstart in 0.7.0)

AgentPulse 支持跨多台物理机/虚拟机分布式协同跑 AI Agent。假设所有节点均接入 **Tailscale**（或等价私有网络），经私网均可直达集中 MQTT Broker：

```
                ┌───────────────────────────────────────┐
                │          Tailscale / 私网              │
                │     MQTT Broker (Mosquitto)           │
                │     tls://broker.example:8883         │
                └──────────────────┬────────────────────┘
                                   │
         ┌─────────────────────────┼─────────────────────────┐
         ▼                         ▼                         ▼
 ┌───────────────┐         ┌───────────────┐         ┌───────────────┐
 │ Node: mac-mini│         │ Node: gpu-box │         │ Operator CLI  │
 │  agentpulse   │         │  agentpulse   │         │  agentpulse   │
 │ (常驻双角色)  │         │ (常驻双角色)  │         │doctor/dispatch│
 └───────────────┘         └───────────────┘         └───────────────┘
```

#### 步骤 1：各计算节点初始化配置并启动常驻服务

在各机器上运行交互式向导完成配置：
```bash
agentpulse config
```
向导将自动写入 `~/.config/agentpulse/config.yaml`（0600 权限），测连 Broker 并探测本机已安装的 Agent CLI（如 `agy`、`opencode`）。

随后在各计算节点启动常驻服务（自动上报心跳、Announce 拓扑并就绪接收派活指令）：
```bash
agentpulse
```

#### 步骤 2：主控端查看集群拓扑与状态

在主控端或任意节点运行：
```bash
# 查看本机环境与全网各节点及 CLI 拓扑扁平表
agentpulse doctor

# 查看全网当前活跃任务
agentpulse jobs
```

#### 步骤 3：谁派谁盯 (Dispatch & Follow)

主控端派发任务并实时追踪：
```bash
# 自动匹配在线且装有 agy 的节点进行派发并实时跟踪日志
agentpulse dispatch --follow agy -- run -p "修复支付模块并发 Bug"

# 指定机器派发
agentpulse dispatch --machine mac-studio --follow op -- -p "重构数据迁移脚本"
```

> [!NOTE]
> **调度门禁 (Dispatch Gate)**：
> dispatch 在下发前会进行双重在线门禁核验：
> 1. 目标机器必须在线（心跳新鲜且未超时）；
> 2. 目标机器广播的 Announce 中必须声明该 Agent CLI 处于可用状态（`ok: true`）。
> 若需要对冷启动或特殊设备强制下发，可使用 `--force` 绕过门禁。

> [!NOTE]
> **机器标识规范化（SanitizeID）与破坏性说明**：
> `listen` 与 `dispatch` 现已双向统一使用同一套规范化逻辑（转小写、下划线 `_` 转连字符 `-`，去除非英数与非连字符号）。例如指定 `--machine Mac_Studio` 或 `--machine my_host` 时，双端均统一规范化为 `mac-studio` 与 `my-host`，确保门禁查找与指令路由严格一致。
> - **兼容性说明**：若以往曾直接使用带下划线的原始 ID 派发，现统一转换为连字符（例如 `my_host` → `my-host`），与 [MQTT 协议规范 §3.1](docs/mqtt-protocol.md#31-机器标识-machine-id)「建议使用小写字母与连字符」完全一致。

> [!CAUTION]
> **安全与网络约定**：
> - 文档和配置示例一律使用 `tcp://broker.example:1883` 占位符。
> - **严禁**将真实 MQTT 地址、真实凭据（用户名/密码）、家庭 IP 或真实 Tailscale IP（如 100.x）提交至代码仓库。

---

## 📚 文档

- [AgentPulse MQTT 协议设计规范 (v1)](docs/mqtt-protocol.md)：主题树、QoS、消息 JSON 契约、`store.Store` 映射与安全运维约定。
- [macOS App 打包指南 (Apple Silicon)](docs/macos-app.md)：构建脚本、Bundle 内部结构、双击交互 Launcher 原理及 Gatekeeper 说明。
- [文档索引](docs/README.md)

## 🧪 Testing

Run all unit tests:

```bash
go test -v ./...
```
