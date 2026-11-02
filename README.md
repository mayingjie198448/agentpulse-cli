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

### 4. Diagnostics (`doctor`)

Validates terminal capabilities, TTY detection, engine CLI presence, and transport readiness:

```bash
./agentpulse doctor

# Check connection intent to a live MQTT broker:
./agentpulse doctor --mqtt-url tcp://100.98.0.1:1883
```

---

### 5. Switching Backend (Mock ↔ MQTT)

All CLI commands (`watch`, `list`, `logs`, `doctor`) support seamlessly toggling between mock simulation and a live MQTT broker:

```bash
# 1. Run against a live MQTT broker via CLI flag:
./agentpulse watch --mqtt-url tcp://100.98.0.1:1883
./agentpulse list --mqtt-url tcp://100.98.0.1:1883 --mqtt-settle-timeout 200ms

# 2. Or configure via environment variables:
export AGENTPULSE_MQTT_URL="tcp://100.98.0.1:1883"
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
