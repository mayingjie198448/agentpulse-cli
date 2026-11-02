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

```bash
# Dispatch command to target machine:
./agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio -- agy run --prompt "Refactor database pool"

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

All CLI commands (`watch`, `list`, `logs`, `doctor`) support seamlessly toggling between mock simulation and a live MQTT broker:

```bash
# 1. Run against a live MQTT broker via CLI flag:
./agentpulse watch --mqtt-url tcp://broker.example:1883
./agentpulse list --mqtt-url tcp://broker.example:1883 --mqtt-settle-timeout 200ms

# 2. Or configure via environment variables:
export AGENTPULSE_MQTT_URL="tcp://broker.example:1883"
export AGENTPULSE_MQTT_USERNAME="agentpulse_user"          # optional
export AGENTPULSE_MQTT_PASSWORD="secret"                   # optional
export AGENTPULSE_MQTT_SETTLE_TIMEOUT="200ms"              # optional retained settle window
./agentpulse watch

# 3. Explicit fallback to mock (overriding env vars):
./agentpulse watch --store=mock
```

---

## 📚 文档

- [AgentPulse MQTT 协议设计规范 (v1)](docs/mqtt-protocol.md)：主题树、QoS、消息 JSON 契约、`store.Store` 映射与安全运维约定。
- [文档索引](docs/README.md)

## 🧪 Testing

Run all unit tests:

```bash
go test -v ./...
```
