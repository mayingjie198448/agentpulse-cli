# PR #55 收尾进度快照（Mac mini 侧，2026-11-20）

状态：**本地进度提交，未推送**。基线 HEAD 前快照 `e491088b4dfce46baedaf17289fd2d3087946f3e`；
PR #55 head 基 `f96bf91` / `release/0.8.0`；冻结起点 `e5573d0`。本提交只记录调查结果与
缺口清单，**未**含任何驱动/产品代码修复。

## 已完成（可验证）

1. 独立评审 10 项已取（issuecomment-7027192064）。
2. 产品侧脱敏链路定位：helper `cmd/agentpulse-test-helper/main.go:72` 打印
   `password=<测试占串>`；wrapper 终端镜像 `internal/wrapper/wrap.go:141/142` 是原字节；
   落盘路径已红act（MQTT `wrapper.go:736`、audit、status `SanitizeJob`）。
   结论：明文出现在 E2E **evidence artifact**（`listener.stdout.log`）→ 修复在驱动 artifact 写入
   层（复用 `RedactSecrets`），产品代码 **无改动**。
3. x/sys v0.49.0 Windows 事实核对（`GetProcessTimes` / `OpenProcess` /
   `GetExitCodeProcess` / `GetProcessId` / `TerminateProcess` / `windows.Handle`），
   产品侧 `internal/wrapper/process_windows.go` 的 bound-handle + creation-time 绑定确认作为驱动
   参考实现。
4. 统计核对：`f96bf91..HEAD` = 50 提交；`f96bf91 HEAD` 范围 = 109 文件；
   `e5573d0..HEAD` = 1 提交（差异主要在 `docs/`）。

## 已知缺陷位置（驱动，当前代码 = 未修改）

- `cmd/c4-e2e-driver/main.go:138`、`:272` — 历史 PID `listener.Process.Kill()` → NOT_FIXED
- `cmd/c4-e2e-driver/main.go:118` — `os.MkdirAll(root)` 未验证；`--root` 已帮助文本提到 workspace
  但无解析/边界检查、无 `--workspace` 参数 → NOT_FIXED
- exit code 改写 `main.go:265`（`ListenerExitCode = 0` 超时分支）→ NOT_FIXED
- artifact 写入 `main.go:280-285` 无 redact；`observe()`（`:393+`）MQTT 失败与 job/helper 原始 exit
  观测诚实性（`jobProof`/`checkJob`，`DispatchRC` vs helper 7）→ NOT_FIXED

## 本次运行记录（命令 + 真实结果）

- `go build -o <tmp>/c4-darwin ./cmd/c4-e2e-driver` → exit 0（基线可编译，darwin/arm64）
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o <tmp>/c4-win.exe ./cmd/c4-e2e-driver` → exit 0
- 仓库状态：`git status --porcelain` 空；临时产物只在 `/tmp/opencode*`

## 未运行（如实）

- 任何 `go test` 用例运行：NOT_RUN（本提交含修复，因此测试未重新执行）
- 4 驱动修复：NOT_STARTED → NOT_RUN
- Windows 真机器事项（PE hash rebind / wrapper_all_tests 重run / ccdq 全局 Git proxy / 新 E2E 原始）：
  NOT_RUN，待 Windows 真机器
- CI：仓库无 `.github/workflows` → CI GAP，未新增 workflows
- 审计/映射文档（PR 50 提交·109 文件 vs `release/0.8.0`）：NOT_STARTED
- audit 报告修正（把 Windows-side FAIL 记录改为 NOT_RUN）：NOT_STARTED

## 边界

不修改 PR base、不 merge/release/tag；不创建 GitHub comments；不触碰 Mac PR #54；不 bump Go 版本；
不触碰用户真实个人文件、无 production service / real home-route MQTT broker。
