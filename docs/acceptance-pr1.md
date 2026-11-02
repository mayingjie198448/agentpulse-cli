# PR #1 审后验收与冒烟测试报告

- **目标 PR**: [#1 feat(cli): initial AgentPulse CLI with interactive TUI and mock engine](https://github.com/mayingjie198448/agentpulse-cli/pull/1)
- **验收分支**: `feat/tui-mock-first-slice`
- **验证时间**: 2026-11-02
- **测试环境**: Windows 11 (amd64), Go 1.27.3
- **最终结论**: **PASS**

---

## 验收步骤执行记录

### 1. 单元测试 (`go test -v ./...`)
- **执行命令**: `go test -v ./...`
- **结果**: **PASS**
- **输出摘要**:
```text
=== RUN   TestRootCommands
=== RUN   TestRootCommands/version
=== RUN   TestRootCommands/doctor
=== RUN   TestRootCommands/list
=== RUN   TestRootCommands/list_json
=== RUN   TestRootCommands/logs
--- PASS: TestRootCommands (0.08s)
    --- PASS: TestRootCommands/version (0.01s)
    --- PASS: TestRootCommands/doctor (0.02s)
    --- PASS: TestRootCommands/list (0.01s)
    --- PASS: TestRootCommands/list_json (0.02s)
    --- PASS: TestRootCommands/logs (0.01s)
PASS
ok  	github.com/mayingjie198448/agentpulse-cli/internal/cli	0.105s
=== RUN   TestMockStore_ListMachines
--- PASS: TestMockStore_ListMachines (0.00s)
=== RUN   TestMockStore_ListJobs
--- PASS: TestMockStore_ListJobs (0.00s)
=== RUN   TestMockStore_GetJobAndLogs
--- PASS: TestMockStore_GetJobAndLogs (0.00s)
=== RUN   TestMockStore_Subscribe
--- PASS: TestMockStore_Subscribe (0.40s)
PASS
ok  	github.com/mayingjie198448/agentpulse-cli/internal/mock	0.421s
=== RUN   TestMachineHeartbeatAgo
--- PASS: TestMachineHeartbeatAgo (0.00s)
PASS
ok  	github.com/mayingjie198448/agentpulse-cli/internal/model	0.024s
=== RUN   TestTUIModel_Navigation
--- PASS: TestTUIModel_Navigation (0.01s)
PASS
ok  	github.com/mayingjie198448/agentpulse-cli/internal/tui	0.033s
```

---

### 2. 二进制构建 (`go build -o agentpulse.exe ./cmd/agentpulse`)
- **执行命令**: `go build -o agentpulse.exe ./cmd/agentpulse`
- **结果**: **PASS**（成功生成二进制文件 `agentpulse.exe`，退出码 0）

---

### 3. 版本指令验证 (`agentpulse.exe version`)
- **执行命令**: `.\agentpulse.exe version`
- **结果**: **PASS**
- **输出**:
```text
agentpulse 0.1.0-alpha (commit: dev, built: 2026-11-02)
Cross-machine monitor for Google Antigravity (agy) and OpenCode
License: MIT
```

---

### 4. 机器与任务列表冒烟 (`agentpulse.exe list --plain`)
- **执行命令**: `.\agentpulse.exe list --plain`
- **结果**: **PASS**
- **输出**:
```text
MACHINES:
NAME       STATUS    HEARTBEAT   PLATFORM        IP              ACTIVE JOBS
ccdq       ONLINE    1s ago      linux/amd64     10.0.1.42       2
mac-mini   ONLINE    2s ago      darwin/arm64    192.168.1.105   2
home-pc    ONLINE    3s ago      windows/amd64   192.168.1.180   1
macbook    OFFLINE   14m ago     darwin/arm64    192.168.1.201   1

JOBS:
JOB ID            MACHINE    ENGINE     STATUS    PROGRESS   TITLE                      CWD
job-agy-01        ccdq       agy        running    68.0%     Auth Middleware Refactor   /workspace/payment-service
job-opencode-02   ccdq       opencode   done      100.0%     API Documentation Sync     /workspace/docs-sync
job-agy-03        mac-mini   agy        failed     42.0%     Concurrency Benchmarks     /Users/mini/agent-tasks/eval-bench
job-opencode-04   mac-mini   opencode   running    24.0%     RAG Embedding Pipeline     /Users/mini/Repos/rag-agent
job-agy-05        home-pc    agy        running    88.0%     AST Codegen Throughput     D:\projects\code-gen-runner
job-opencode-06   macbook    opencode   done      100.0%     Ingress Manifest Plan      /Users/mbp/infra/k8s-agent
```

---

### 5. 短跑 / 管道模式冒烟 (`agentpulse.exe watch --plain`)
- **执行命令**:
```powershell
$p = Start-Process -FilePath ".\agentpulse.exe" -ArgumentList "watch", "--plain" -PassThru -RedirectStandardOutput "watch_out.tmp"
Start-Sleep -Seconds 3
Stop-Process -Id $p.Id -Force
Get-Content "watch_out.tmp"
```
- **结果**: **PASS**（未挂死 TUI，定期输出纯文本快照，任务进度与心跳动态演进正常）
- **输出截选**:
```text
=== [AgentPulse Watch Snapshot @ 13:30:19] ===
Machines (3 online / 4 total):
  - ccdq         [ONLINE ] heartbeat: 1s ago   jobs: 2
  - mac-mini     [ONLINE ] heartbeat: 2s ago   jobs: 2
  - home-pc      [ONLINE ] heartbeat: 3s ago   jobs: 1
  - macbook      [OFFLINE] heartbeat: 14m ago  jobs: 1
Jobs (6 total):
  - job-agy-01     [agy     ] @ccdq       [running]  68.0% : Auth Middleware Refactor
  - job-opencode-02 [opencode] @ccdq       [done   ] 100.0% : API Documentation Sync
  - job-agy-03     [agy     ] @mac-mini   [failed ]  42.0% : Concurrency Benchmarks
  - job-opencode-04 [opencode] @mac-mini   [running]  24.0% : RAG Embedding Pipeline
  - job-agy-05     [agy     ] @home-pc    [running]  88.0% : AST Codegen Throughput
  - job-opencode-06 [opencode] @macbook    [done   ] 100.0% : Ingress Manifest Plan

=== [AgentPulse Watch Snapshot @ 13:30:21] ===
Machines (3 online / 4 total):
  - ccdq         [ONLINE ] heartbeat: 0s ago   jobs: 2
  - mac-mini     [ONLINE ] heartbeat: 0s ago   jobs: 2
  - home-pc      [ONLINE ] heartbeat: 0s ago   jobs: 1
  - macbook      [OFFLINE] heartbeat: 14m ago  jobs: 1
Jobs (6 total):
  - job-agy-01     [agy     ] @ccdq       [running]  71.0% : Auth Middleware Refactor
  - job-opencode-02 [opencode] @ccdq       [done   ] 100.0% : API Documentation Sync
  - job-agy-03     [agy     ] @mac-mini   [failed ]  42.0% : Concurrency Benchmarks
  - job-opencode-04 [opencode] @mac-mini   [running]  28.0% : RAG Embedding Pipeline
  - job-agy-05     [agy     ] @home-pc    [running]  91.0% : AST Codegen Throughput
  - job-opencode-06 [opencode] @macbook    [done   ] 100.0% : Ingress Manifest Plan
```

---

### 6. 环境诊断 (`agentpulse.exe doctor`)
- **执行命令**: `.\agentpulse.exe doctor`
- **结果**: **PASS**
- **输出**:
```text
AgentPulse Diagnostics:
----------------------------------------
[OK] Runtime: Go go1.27.3 on windows/amd64
[INFO] Terminal: Non-interactive TTY / pipe (will fallback to plain text)
[OK] Engine 'agy': Detected at C:\Users\Administrator\AppData\Local\agy\bin\agy.exe
[INFO] Engine 'OpenCode': Not found in PATH (Local jobs will use simulated adapter)
[OK] Data Layer: In-memory simulation store active
[INFO] MQTT Transport: Stubbed for Phase 2 (Cross-machine broker adapter)
[INFO] Sidecar Agent: Stubbed for Phase 2 (Node telemetry daemon)
----------------------------------------
Status: All systems ready for Phase 1 Mock TUI.
```

---

## 结论

本次审后验收所有环节（`go test` 全量通过、二进制构建、`version`、`list --plain`、`watch --plain` 管道/短跑、`doctor` 诊断）均表现符合预期，无功能缺陷与运行时错误。

**验收结论**: **PASS**
