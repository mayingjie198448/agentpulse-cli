# AgentPulse CLI

Cross-machine monitor CLI for **Google Antigravity (`agy`)** and **OpenCode** autonomous agent workflows.

Licensed under the [MIT License](LICENSE).

---

> [!IMPORTANT]
> **Dual Backend Architecture: Mock (Default) & MQTT Store (v0.2.0)**
> AgentPulse runs out-of-the-box with an internal in-memory simulation engine (`internal/mock`).
> To connect to a live Mosquitto / MQTT broker (lands in release/0.2.0), specify `--mqtt-url` or set `AGENTPULSE_MQTT_URL`.

---

## ⚡ Architecture: Mock vs. MQTT Store

```
                    ┌────────────────────────────┐
                    │    AgentPulse CLI / TUI    │
                    │  (bubbletea / lipgloss)    │
                    └─────────────┬──────────────┘
                                  │
                                  ▼
                    ┌────────────────────────────┐
                    │     store.Store (API)      │
                    └──────┬──────────────┬──────┘
                           │              │
        ┌──────────────────┴──┐        ┌──┴──────────────────┐
        │  [Default: Mock]    │        │  [Active: MQTT]     │
        │ internal/mock.Store │        │ internal/store/mqtt │
        └─────────────────────┘        └──────────┬──────────┘
                                                  │
                                                  ▼
                                       ┌─────────────────────┐
                                       │    MQTT Broker      │
                                       │ (agy / OpenCode bus)│
                                       └─────────────────────┘
```

- **Mock Store (Default)**:
  - `internal/mock.MockStore` implements `store.Store` and simulates 4 distributed nodes (`ccdq`, `mac-mini`, `home-pc`, `macbook`).
  - Running, done, and failed jobs across both `agy` and `OpenCode`.
  - Realistic agent logs, subagent invocations, tool outputs, and progress bar ticks.
- **MQTT Store (New in v0.2.0)**:
  - `internal/store/mqtt.Store` connects to a remote MQTT broker (e.g. iStoreOS Mosquitto over Tailscale).
  - Subscribes to `agentpulse/v1/machines/+/status`, `.../jobs/+/status`, and `.../jobs/+/logs`.
  - In-memory aggregation with active job log ring buffer (~500 lines) and bounded event streaming (1024 cap) with non-blocking backpressure mitigation.
  - Automatic cold-start settle (~200ms configurable via `--mqtt-settle-timeout`) ensuring retained node and job states are populated on one-shot CLI commands (`list`, `logs`).

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

### 构建 / 获取 Mac .app（Apple Silicon）

为 macOS 用户提供了一键打包 Apple Silicon (`darwin/arm64`) 原生 App Bundle 的脚本：

- **环境要求**：Apple Silicon Mac (M1/M2/M3/M4 系列) + Go 1.22+
- **构建命令**：
  ```bash
  bash scripts/build-macos-app.sh
  ```
- **产物路径**：`dist/AgentPulse.app`
- **使用方式**：
  - **Finder 双击**：自动唤起 Terminal.app 执行 `agentpulse version` 与帮助说明，并驻留交互终端以便随时输入命令。
  - **终端直接执行**：
    ```bash
    # 启动器（兼容 CLI 参数并支持终端交互）：
    ./dist/AgentPulse.app/Contents/MacOS/agentpulse version
    # 或直接运行底层 Mach-O 二进制：
    ./dist/AgentPulse.app/Contents/MacOS/agentpulse-bin version
    ```
- **Gatekeeper 与安全性说明**：
  - 本地离线构建未接入 Apple 商业付费开发者证书公证（Notarization）。
  - 若在其他 Mac 上通过网络分发打开提示拦截，可在 Finder 中右键点击 App 选择「打开」，或在终端使用中性命令解除隔离属性：
    ```bash
    xattr -cr /path/to/AgentPulse.app
    ```

详细 Bundle 内部结构与原理解析参见 [macOS App 打包设计文档](docs/macos-app.md)。

---

## 🚀 Usage

### 1. Interactive Watch Dashboard (`watch`)

Launches a cyberpunk-styled terminal dashboard (Bubble Tea & Lipgloss) showing multi-machine heartbeats, active agent jobs, real-time progress bars, and scrolling logs:

```bash
./agentpulse watch
```

#### TUI Keyboard Shortcuts

| Key | Action |
| :--- | :--- |
| `↑` / `↓` or `j` / `k` | Navigate and select jobs |
| `Tab` | Switch focus between Jobs list and Log Viewer |
| `Space` | Pause / Resume live telemetry simulation |
| `a` | Toggle log autoscroll |
| `c` | Clear log buffer for selected job |
| `PgUp` / `PgDn` | Scroll through logs |
| `q` / `Ctrl+C` | Exit dashboard |

#### Plain-text / Piped Mode
If stdout is not a TTY (e.g. redirected to a pipe or log file) or when `--plain` is specified, `watch` gracefully downgrades to a plain-text periodic snapshot:

```bash
./agentpulse watch --plain
./agentpulse watch | grep "job-agy-01"
```

---

### 2. List Machines and Jobs (`list`)

Prints registered nodes, heartbeats, active jobs, engines, and status:

```bash
# Standard table output
./agentpulse list

# Filter to only show online machines
./agentpulse list --online

# JSON output
./agentpulse list --json
```

---

### 3. Inspect Job Logs (`logs <job-id>`)

Retrieve historical logs or stream live events:

```bash
# View last 50 log entries
./agentpulse logs job-agy-01

# Stream real-time logs (equivalent to tail -f)
./agentpulse logs job-agy-01 -f

# Specify log count
./agentpulse logs job-opencode-04 --tail 10
```

---

### 4. Thin Wrapper: Monitor Local Agent Runs (`wrap`)

`agentpulse wrap` is a thin local process that executes an AI agent command (`agy` or `opencode`), streams console output locally, writes local status snapshots (`.agentpulse/status.json`), and publishes real-time node heartbeats, job lifecycle transitions, and log streams to an MQTT broker per [MQTT Protocol Spec](docs/mqtt-protocol.md).

By default, missing binaries fail explicitly (never silently stubbed). Passing `--stub` enables simulated execution for testing or automated CI without requiring the engine to be installed.

> [!NOTE]
> **Single-Machine Slice (Step 3)**:
> - If `AGENTPULSE_MQTT_URL` or `--mqtt-url` is specified, telemetry is actively published to the MQTT broker (LWT, QoS 1 retained heartbeats and job statuses, QoS 0 log streaming).
> - If no broker URL is provided, the wrapper executes locally, mirrors output, and writes local status snapshots without requiring a broker.
> - **Security Note**: Use `--dangerously-skip-permissions` only in this repo / trusted workspace to permit unattended execution.

```bash
# Wrap agy in local mode (writes .agentpulse/status.json, no broker needed):
./agentpulse wrap -- agy --dangerously-skip-permissions -p "Fix bug in store"

# Or pass --dangerously-skip-permissions as a wrapper flag:
./agentpulse wrap --dangerously-skip-permissions agy -p "Refactor auth layer"

# Wrap agy and stream live telemetry to an MQTT broker:
./agentpulse wrap --mqtt-url tcp://broker.example:1883 -- agy --dangerously-skip-permissions -p "Run test suite"

# Run simulated execution (explicit --stub required for simulation):
./agentpulse wrap --engine opencode --stub
```

---

### 5. Edge Listener Daemon (`wrap --listen` / `listen`)

`agentpulse wrap --listen` (or `agentpulse listen`) runs an edge worker daemon on a target machine that connects to an MQTT broker, registers node LWT and periodic heartbeats, subscribes to incoming dispatch commands, and executes jobs serially:

- **Topic**: Subscribes to `agentpulse/v1/machines/{machine_id}/commands/dispatch` (QoS 1).
- **Concurrency**: Jobs are processed sequentially via an internal serial queue (one job at a time) to prevent resource contention.
- **Resilience**: Malformed or invalid payloads are rejected with clear local logs without crashing the daemon.
- **Clean Shutdown**: On `Ctrl+C` (SIGINT/SIGTERM), the daemon stops accepting new commands, cancels any running job, publishes offline status (`online: false, job_count: 0`), and disconnects cleanly.

```bash
# Start edge listener on this machine:
./agentpulse listen --mqtt-url tcp://broker.example:1883

# Or via wrap --listen with explicit node identifier:
./agentpulse wrap --listen --mqtt-url tcp://broker.example:1883 --machine mac-studio

# Start listener with simulated execution mode for testing:
./agentpulse listen --mqtt-url tcp://broker.example:1883 --machine linux-gpu --stub
```

---

### 6. Remote Job Dispatch (`dispatch`)

`agentpulse dispatch` sends a remote execution instruction to a target machine over MQTT per [MQTT Protocol Spec §4.4](docs/mqtt-protocol.md#44-派活指令-job-dispatch--command):

- **Topic**: Publishes to `agentpulse/v1/machines/{machine_id}/commands/dispatch` with QoS 1, Retained `false`.
- **Payload**: Includes `command_id`, `job_id`, `engine`, `cwd`, `command`, `args`, `title`, `timeout_seconds`, `env`, `requested_by`, and `timestamp` (RFC 3339).
- **Progress Tracking & Timeouts**:
  - `--wait`: Subscribes to the job's status topic, prints status and progress updates, and exits non-zero if the job fails or wait times out.
  - `--follow`: Streams real-time stdout/stderr logs in addition to waiting for completion.
  - `--timeout-seconds <sec>`: Remote job execution timeout (passed in dispatch payload; remote daemon enforces this).
  - `--wait-timeout <sec>`: Local wait wall-clock timeout in seconds (0 for no limit; stops local waiting without killing the remote job).
- **Target Machine Gate**:
  - In MQTT mode, `dispatch` inspects the target machine status on the broker before publishing. If the machine is unseen or `online: false`, dispatch fails immediately with a clear diagnostic message.
  - `--force`: Bypasses this gate to dispatch even if the target machine is unknown or currently offline.

```bash
# Dispatch command to target machine:
./agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio -- agy run --prompt "Refactor database pool"

# Force dispatch to a machine not currently reported as online:
./agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio --force -- agy -p "Bootstrap agent"

# Dispatch via explicit flags:
./agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine linux-gpu --command opencode --arg run --arg "--prompt=Fine-tune model"

# Dispatch with wait and real-time log streaming:
./agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio --follow -- agy -p "Run test suite"

# Dispatch with remote execution timeout and local wait wall-clock timeout:
./agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio --timeout-seconds 1800 --wait-timeout 600 -e ENV=production_test --wait -- agy -p "Verify migrations"
```

---

### 7. Inspect Local Status (`status`)

Inspects the latest local agent execution snapshot written by `agentpulse wrap`:

```bash
# Human-readable formatted snapshot
./agentpulse status

# Structured JSON output
./agentpulse status --json
```

---

### 8. Diagnostics (`doctor`)

Validates terminal capabilities, TTY detection, engine CLI presence, and transport readiness:

```bash
./agentpulse doctor

# Check connection intent to a live MQTT broker:
./agentpulse doctor --mqtt-url tcp://broker.example:1883
```

---

### 9. Switching Backend (Mock ↔ MQTT)

All CLI commands (`watch`, `list`, `logs`, `doctor`, `dispatch`) support seamlessly toggling between mock simulation and a live MQTT broker:

```bash
# 1. Run against a live MQTT broker via CLI flag (supports tcp:// and tls://):
./agentpulse watch --mqtt-url tls://broker.example:8883
./agentpulse list --mqtt-url tcp://broker.example:1883 --mqtt-settle-timeout 200ms

# 2. Or configure via environment variables (with optional TLS / mTLS):
export AGENTPULSE_MQTT_URL="tls://broker.example:8883"
export AGENTPULSE_MQTT_USERNAME="agentpulse_user"          # optional
export AGENTPULSE_MQTT_PASSWORD="secret"                   # optional (never logged)
export AGENTPULSE_MQTT_CA_CERT="/path/to/ca.pem"           # optional custom CA
export AGENTPULSE_MQTT_CLIENT_CERT="/path/to/client.crt"   # optional mTLS
export AGENTPULSE_MQTT_CLIENT_KEY="/path/to/client.key"    # optional mTLS
export AGENTPULSE_MQTT_SETTLE_TIMEOUT="200ms"              # optional retained settle window
export AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT="45s"             # optional stale heartbeat timeout
./agentpulse watch

# 3. Security hardening (TLS & Mosquitto ACL):
# See docs/mqtt-protocol.md §6.2 and docs/mosquitto-acl.example for detailed TLS options & ACL rules.

# 4. Explicit fallback to mock (overriding env vars):
./agentpulse watch --store=mock
```

---

### 10. 多机怎么跑 (Multi-Machine Quickstart)

AgentPulse 支持跨多台物理机/虚拟机分布式协同跑 AI Agent。假设所有节点均接入 **Tailscale**（或等价私有网络），经私网均可直达集中 MQTT Broker：

```
                ┌───────────────────────────────────────┐
                │          Tailscale / 私网              │
                │     MQTT Broker (Mosquitto)           │
                │     tcp://broker.example:1883         │
                └──────────────────┬────────────────────┘
                                   │
         ┌─────────────────────────┼─────────────────────────┐
         ▼                         ▼                         ▼
 ┌───────────────┐         ┌───────────────┐         ┌───────────────┐
 │ Node: mac-mini│         │ Node: gpu-box │         │ Operator CLI  │
 │ agentpulse    │         │ agentpulse    │         │ agentpulse    │
 │ listen        │         │ listen        │         │ dispatch/watch│
 └───────────────┘         └───────────────┘         └───────────────┘
```

#### 步骤 1：每台工作机启动 Listener

在需要承载 Agent 任务的机器上启动监听守护进程。
机器身份默认解析规则：显式 `--machine <id>` > 环境变量 `AGENTPULSE_MACHINE_ID` > 本机 hostname（自动规范化为小写字母与连字符）：

```bash
# 在 Machine A (如 mac-studio) 上启动：
export AGENTPULSE_MQTT_URL="tcp://broker.example:1883"
export AGENTPULSE_MACHINE_ID="mac-studio"
agentpulse listen

# 或在 Machine B (如 linux-gpu) 上通过 wrap --listen 启动：
agentpulse wrap --listen --mqtt-url tcp://broker.example:1883 --machine linux-gpu
```

- Listener 默认每 15 秒上报一次心跳（`--heartbeat-interval`）。
- 建立连接时托管 LWT 遗嘱消息；进程崩溃或断网时 Broker 自动广播离线状态。

#### 步骤 2：主控端查看集群节点在线状态

在任意节点或主控机上查看拓扑：

```bash
# 查看所有已见机器与作业
agentpulse list --mqtt-url tcp://broker.example:1883

# 只看当前在线节点（过滤掉心跳超时或离线节点）
agentpulse list --mqtt-url tcp://broker.example:1883 --online

# 启动全屏 Cyberpunk TUI 实时看盘
agentpulse watch --mqtt-url tcp://broker.example:1883
```

- 心跳超时保底机制（§6.4）：若某节点超过 45 秒未上报心跳（可配置 `--mqtt-heartbeat-timeout` / `AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT`，默认 45s），Store 主动将其标记为 `OFFLINE`。

#### 步骤 3：谁派谁盯 (Dispatch & Follow)

主控端向指定机器派活并实时盯盘：

```bash
# 向 mac-studio 派发任务，并在本地等待完成、同步流式打印实时日志：
agentpulse dispatch --mqtt-url tcp://broker.example:1883 \
  --machine mac-studio --follow \
  -- agy --dangerously-skip-permissions -p "Refactor payment module"

# 派发门禁 (Dispatch Gate)：
# dispatch 会在派发前查询 Broker 上目标机的在线状态。
# 若目标机未曾注册或处于 OFFLINE 状态，dispatch 默认直接失败并报错提示。
# 如需忽略目标机当前心跳状态强制派发，可追加 --force：
agentpulse dispatch --mqtt-url tcp://broker.example:1883 \
  --machine mac-studio --force \
  -- agy -p "Emergency bootstrap"
```

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
