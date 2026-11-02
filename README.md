# AgentPulse CLI

Cross-machine monitor CLI for **Google Antigravity (`agy`)** and **OpenCode** autonomous agent workflows.

Licensed under the [MIT License](LICENSE).

---

> [!IMPORTANT]
> **Phase 1 Release (First Slice): Interactive Mock Only**
> This repository currently implements Phase 1 of AgentPulse. All machine states, agent jobs, heartbeats, and streaming logs are driven by an internal in-memory simulation engine (`internal/mock`).
> **No live MQTT broker, agy sidecar, or OpenCode network daemon is connected in this release.**

---

## ⚡ Architecture: Mock vs. Future Real Path

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
        │  [Phase 1: Active]  │        │ [Phase 2: Planned]  │
        │ internal/mock.Store │        │  MQTT Store Adapter │
        └─────────────────────┘        └──────────┬──────────┘
                                                  │
                                                  ▼
                                       ┌─────────────────────┐
                                       │    MQTT Broker      │
                                       │ (agy / OpenCode bus)│
                                       └─────────────────────┘
```

- **Phase 1 (Current)**:
  - `internal/mock.MockStore` implements `store.Store` and simulates 4 distributed nodes (`ccdq`, `mac-mini`, `home-pc`, `macbook`).
  - Running, done, and failed jobs across both `agy` and `OpenCode`.
  - Realistic agent logs, subagent invocations, tool outputs, and progress bar ticks.
- **Phase 2 (Future Road)**:
  - Retains identical CLI commands and TUI components.
  - Implement real `store.Store` backend connecting to remote agent runners via lightweight MQTT topics (e.g. `agentpulse/nodes/+/heartbeat`, `agentpulse/jobs/+/logs`).

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
```

---

## 📚 文档

- [AgentPulse MQTT 协议设计规范 (v1)](docs/mqtt-protocol.md)（**协议草案，实现后置**）：主题树、QoS、消息 JSON 契约、`store.Store` 映射与安全运维约定。
- [文档索引](docs/README.md)

## 🧪 Testing

Run all unit tests:

```bash
go test -v ./...
```
