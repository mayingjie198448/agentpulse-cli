# 本机裸 Agent CLI 进程自动挂接机制 (Bare CLI Attach)

> **对应 Issue**：#32「本机裸 agy/opencode 进程自动挂接进 JOBS（不经 dispatch）」  
> **适用版本**：AgentPulse v0.8.0+  
> **状态**：已落地  

---

## 1. 概述与背景

在开发者的日常工作中，除了通过 `agentpulse dispatch` 从远程或本机派发任务外，经常会在终端中直接启动 Agent CLI（例如在当前 Shell 中直接执行 `agy -p "..."` 或 `opencode run ...`）。在 v0.6.0 及以前，只有通过 dispatch 触发并由 thin wrapper 包装的进程才会在全网大盘（JOBS / watch）中可见。

为了实现对开发机所有活跃 Agent 工作流的全量可见性，AgentPulse 常驻守护服务（`agentpulse` 双角色守护进程或 `agentpulse listen` 监听模式）新增**本机裸进程自动挂接 (Bare CLI Attach)** 机制：
- 自动扫描并发现本机新启动或正在运行的裸 Agent CLI 进程；
- 按照 `~/.config/agentpulse/config.yaml` 中启用的 Agent 配置进行白名单匹配；
- 采集其进程生命周期状态并捕获控制台日志/进度流；
- 实时上报至 MQTT Broker，使 `agentpulse jobs`、`agentpulse watch` 以及全网 TUI 看板立即可见。

---

## 2. 核心工作原理

### 2.1 进程发现与白名单匹配
守护进程通过后台周期性扫描（默认每 2 秒一次）系统进程表：
1. **身份剔除**：
   - 排除系统级根进程（PID <= 1）；
   - 排除当前 `agentpulse` 守护进程自身及其子进程；
   - 排除由 `agentpulse dispatch` 执行器派发并跟踪的子进程（避免双重上报）；
   - 严格排除常见系统工具、编辑器与终端（如 `grep`、`vim`、`git`、`cat`、`go`、`zsh` 等），防止误收包含相关关键字的无关命令；
   - 排除任何显式包含 `agentpulse` 的命令行。
2. **白名单匹配**：
   - 读取本机配置中注册且处于启用状态（`IsEnabled() == true`）的 Agent CLI（如 `agy`、`opencode`）；
   - 检查目标进程的执行程序名或绝对路径是否匹配 Agent 的 `id`、`alias` 或 `path`；
   - **未在配置中启用（或配置了 `enabled: false`）的 Agent CLI 绝对不会被挂接**。

### 2.2 任务标识与生命周期
- **稳定 Job ID**：对检测到的裸进程生成唯一且稳定的任务标识：`job-attach-{engine}-{pid}-{startTime}`。在进程运行期间多次扫描均保持幂等，不会重复建任务。
- **任务标题推断 (Title)**：解析命令行参数，优先提取 `-p`、`--prompt`、`--task` 等提示词参数；若无参数则截取命令行摘要。
- **工作目录探测 (Cwd)**：在 macOS 下通过 `lsof` 检索文件描述符 cwd，在 Linux 下通过 `/proc/{pid}/cwd` 符号链接自动解析真实工作目录。
- **生命周期跟踪**：
  - 发现存活进程：向 `agentpulse/v1/machines/{machine_id}/jobs/{job_id}/status` 发布 `status: "running"`, `progress: 0.0`（QoS 1, Retained true）；
  - 进程存活期间：维护心跳并实时刷新 `updated_at`；更新节点心跳的 `job_count`；
  - 进程退出：探测到进程终止后，自动更新 `duration_str`。严格执行可靠终态判定机制，禁止将不确定当成 `done`：
    - 若能可靠判定退出码且退出码为 0（且日志流无异常错误），标记为 `done`，`progress: 100.0`；
    - 若退出码非 0（例如 exit=7），坚决标记为 `failed`，在 `error` 字段载明非零退出码原因，且不将进度虚标为 100%；
    - 若无法取得退出码且无可靠成功证据（非子进程、无退出码提供器或观测受限），按保守原则标记为 `failed`，在 `error` 字段说明「无法判定退出码（依据不足，非正常可观测退出）」；
    - 若进程监控被外部中断或停止（如守护进程退出），标记为 `failed` 并说明被中断。
    向 Broker 发布最终状态与 `agentpulse/v1/notifications/alerts` 告警。

### 2.3 日志与进度流捕获
针对不同 Agent CLI 的原生日志机制，自动挂接器进行无侵入式流式追踪：
- **Google Antigravity (`agy`)**：
  - 自动匹配 `~/.gemini/antigravity-cli/log/` 下由该 PID 生成的 `cli-*.log`；
  - 追踪日志流中的会话标识 `Created conversation <uuid>`，联动追踪 `~/.gemini/antigravity-cli/brain/<uuid>/.system_generated/logs/transcript.jsonl`；
  - 自动解析 Agent 思考、工具调用与进度信息，并实时按 `model.LogEntry` 协议向 `agentpulse/v1/machines/{machine_id}/jobs/{job_id}/logs` 发布流式日志。
- **OpenCode (`opencode`)**：
  - 自动定位 `~/.local/share/opencode/log/` 下由当前进程产生的日志文件进行实时追踪。

---

## 3. 裸挂接相对 Dispatch 的采集差异分析

这是裸挂接与远程派发在系统语义上的明确边界：

| 维度 | 远程派发 (Dispatch) | 裸进程自动挂接 (Bare Attach) | 说明 |
| :--- | :--- | :--- | :--- |
| **能采到什么 (Collected)** | PID、退出码、完整的真实标准输出与错误输出 (stdout/stderr)、耗时、节点 IP | 真实 PID、完整命令行 (argv)、启动时间、运行时长、操作系统工作目录 (cwd)、Agent 引擎类型、日志流与进度 | 裸进程依靠系统进程表与 Agent 本地日志落地文件进行采集，保证基础监控与流式看流体验对齐 |
| **猜什么 (Inferred)** | 无需猜测（全部由调度者指令显式定义） | **稳定 Job ID**（由 PID+启动时间生成）、**任务说明**（从 -p/--prompt 参数推断）、**工作目录**（通过 lsof/procfs 探测推断）、**完成状态**（依据可靠退出码判定；无法判定或退出码≠0 则保守标记为 failed） | 裸进程不由 AgentPulse 创生，其元数据由挂接器在扫描时结合操作系统环境与参数进行确定性推断 |
| **缺什么 (Missing)** | 无缺失 | **请求者身份 (`requested_by`)**（仅能标记为本地终端触发）、**超时控制 (`timeout_seconds`)**（不强杀用户本地进程）、**环境变量注入与 Worktree 隔离**（完全运行在用户原有 shell 环境中） | 裸挂接不替代 dispatch 的远程调度、权限控制与自动隔离能力，仅承担被动观测职责 |

---

## 4. 安全、隔离与依赖注入约束

### 4.1 生产运行安全防护
1. **绝对不误收无关进程**：白名单机制严格拦截非 Agent 命令，坚决不误收诸如 `grep agy`、`vim agy.go`、`go test` 或 `agentpulse` 自身。
2. **绝对不污染/不修改用户工作区**：挂接器为纯只读观测者，不创建、不修改、不删除用户的任何代码目录或 worktree。
3. **严格遵守配置启用开关**：只有在 `config.yaml` 中配置且 `enabled: true` 的 Agent CLI 才会被挂接；配置未启用的命令绝不上报。
4. **与 Dispatch 严格去重**：Dispatch 派发的子进程在启动时直接注册至内部排除表，挂接器扫描到时自动忽略，保证不会产生两条重复任务。
5. **支持显式关闭**：运行 `agentpulse listen --no-attach` 可按需完全禁用裸进程挂接功能。

### 4.2 内部测试与隔离依赖注入 (Testing & Isolation Injection)
为了避免自动化验收或集成测试扫到开发机上其他并行编码进程的命令行与提示词（如宿主机正在运行的其他 `agy` 或 `opencode` 会话），系统提供内部依赖注入。名单非空时，默认 OS 发现在读取命令行之前就限定到指定 PID。名单未设置或为空时，生产全表扫描行为不变：
1. **`ProcessLister`**：支持注入自定义进程列表枚举器，测试完全无需执行系统级 `ps -A`；
2. **`PIDAllowlist` / 环境变量 `AGENTPULSE_ATTACH_PID_ALLOWLIST`**：名单非空且使用默认发现时，在调用操作系统读取命令行之前就限定为这些 PID。Linux 只读 `/proc/<pid>/cmdline` 与 `/proc/<pid>/stat`（以及该 PID 自己的 cwd、它自己打开的日志 fd），不运行 `ps -A`，也不读取名单外 PID 的命令行或日志。其他系统只运行 `ps -p <pid列表>`，同样不先做全表采集。日志只打开文件名精确含有该 PID 的文件，或该 PID 自己打开且位于日志目录中的日志，不会为了匹配去读旁路 PID 的日志内容。调用方注入了 `ProcessLister` 时，仍由注入方提供数据，名单只跳过未列入的条目；
3. **`AgyLogDir` / `AgyBrainDir` / 环境变量 `AGENTPULSE_TEST_AGY_LOG_DIR`**：支持指定临时隔离的日志目录，彻底杜绝测试读取宿主机真实用户 `~/.gemini` 下的历史日志；
4. **`OpenCodeLogDir` / 环境变量 `AGENTPULSE_TEST_OPENCODE_LOG_DIR`**：支持指定隔离的 OpenCode 测试日志目录；
5. **`ProcessExitChecker`**：支持注入子进程退出码探测器，供受限合成测试精确校验 exit≠0（如 exit=7）或退出码无法判定时的终态转换；
6. **`StartTimeResolver` / `CwdResolver`**：支持注入模拟启动时间与工作目录解析器，避免测试时触发宿主机 `lsof`。

---

## 5. 验收与运维验证步骤

在真实环境交付或运维验证时，请按如下步骤执行（无需部署生产系统，完全安全）：

1. **环境准备与配置校验**：
   确保测试配置文件 `~/.config/agentpulse/config.yaml` 或专用测试配置中包含启用的 `agy` 项，同时保留未启用的 CLI 项作为对照。
2. **启动常驻守护服务**：
   在终端 1 执行：
   ```bash
   agentpulse
   # 或在独立测试环境运行：
   agentpulse listen --mqtt-url tcp://<broker-placeholder>:1883
   ```
3. **触发裸 CLI 运行**：
   在终端 2 直接执行（不经 dispatch）：
   ```bash
   agy -p "测试自动挂接任务"
   ```
4. **验证大盘与日志流**：
   - 在终端 3 执行 `agentpulse jobs` 或在看板 TUI 的 JOBS 列表中观察：应立刻出现一个状态为 `RUNNING`、标题为 `"测试自动挂接任务"` 的任务；
   - 执行 `agentpulse watch` 或进入该任务详情：能够正常看到实时的日志与进度流动；
   - 待 `agy` 进程退出后，按可靠终态判定更新看板并记录运行时长：只有可靠确认退出码为 0 才标为 `DONE`（进度 100%）；退出码非 0（例如 7）标为 `failed` 并写明退出码，进度不得为 100%；无法取得退出码时标为 `failed`，错误说明无法判定退出码，不得标为 `DONE` 或 100%；
   - 验证执行 `grep agy` 或 `vim agy.go` 等非 Agent 进程时，JOBS 列表中不会产生异常误收任务。
