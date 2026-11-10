# macOS (Darwin) Fail-Closed 隔离生命周期与合成护栏设计

本文档说明 AgentPulse 在 macOS (Darwin) 平台上的写入隔离架构、生命周期契约及合成原生验收测试设计。

## 1. 设计目标与边界

- **Fail-Closed 默认安全**：未通过原生安全探针验证前，绝不降级为裸进程或允许放行真实写入；出现任何异常（工具缺失、标识不符、探针失败、管道截断）均拒绝启动（`ErrRefused`）。
- **受限临时根（Owned Canonical Run Root）**：仅允许向本 run 独占且规范化的临时工作区目录读写；默认拒绝访问宿主真实 HOME、用户配置、SSH、云同步目录及 Keychain，既防写亦防读。
- **真实 Seatbelt 沙箱强制执行**：基于 `/usr/bin/sandbox-exec` 结合严密的编译期/运行期 Seatbelt Profile（`(deny default)`、`(deny file-write*)`、`(deny file-read*)`、最小必需系统运行时读取许可、禁止 Mach securityd IPC），无 broad `/System`、`/usr` 或 `/private/var` 通配读，不以 profile 文本或跨平台编译作为验证通过证明。
- **探针在用户命令前裁决**：隔离子进程在接管用户命令前，必须在沙箱内核中完成正向写（run root 内）与负向拦截（外部 synthetic sentinels 读写拒绝），并通过确认管道通知父进程；写入确认或管道关闭失败绝不 exec 用户命令（fail-closed）。
- **句柄相对与保护路径前置判定**：所有目录创建（base、registry、哨兵目录）在产生任何文件系统副作用前必须完成现存祖先与受保护 HOME 判定；随后仅通过已校验文件描述符进行 handle-relative 创建，严防符号链接逃逸与误创。
- **不跟随符号链接的安全清理**：清理过程基于创建时的独占 RunRecord 与内核 Inode/Device 校验，逐层解链并拒绝跨越符号链接，保护 ambiguous root 与宿主资产；注册表删除失败如实上报 partial 状态，不谎称完全清理。
- **未验证表面保持 BLOCKED**：文件级沙箱读写拒绝属于文件系统隔离，不等于 Mach IPC 级别隔离；Keychain 与 securityd 真实 IPC 在未经硬件实机独立验证前，严格保持 `BLOCKED`，不作虚假声称。

## 2. 隔离生命周期 (Lifecycle)

```
[Prepare]
   │
   ├─ 1. SelectBackend() 检查 /usr/bin/sandbox-exec 可执行性 (缺失则 ErrBackendUnavailable)
   ├─ 2. 规范化路径与创建独占 run root (/private/tmp/agentpulse-runs/<run_id>/...)
   ├─ 3. 写入内部 .agentpulse-run-marker，固化 Dev/Ino/Nonce/RunID/ProfileHash
   ├─ 4. 生成 DarwinSeatbeltProfile(run_root)，参数化嵌入严格子路径写权限
   └─ 5. 构建 /usr/bin/sandbox-exec -p <profile> <self> --agentpulse-internal-isolate ...
   │
[Start & Confirmation Probe]
   │
   ├─ 6. 启动沙箱子进程，传递匿名确认管道写端 (FD 3)
   ├─ 7. 子进程验证自身的 exe SHA256、run root identity (O_NOFOLLOW / F_GETPATH)
   ├─ 8. 子进程执行探针：
   │     ├─ 尝试写入/删除外部哨兵 -> 必须被沙箱拒绝 (EPERM / Access Denied)
   │     └─ 写入 work-root/probe-ok -> 必须成功
   ├─ 9. 探针通过后，完整写入 "OK\n" 并关闭管道；写或关失败直接拒绝，仅成功时 unix.Exec 用户命令
   └─ 10. 父进程读取确认；若为 FAIL 或管道关闭，立即中断并记录 refused 审计
   │
[CleanupRun]
   │
   ├─ 11. 读取注册表与 marker，校验 Dev、Ino、Nonce 与 Protected Homes 边界
   ├─ 12. 递归深度遍历删除目录子项，遇到符号链接仅删除链接本身，绝不跟随
   └─ 13. 校验父目录与根 Inode 一致性，最终 unlinkat 移除空根并注销注册表（注销失败如实返回 partial）
```

## 3. 合成原生护栏测试 (Synthetic Native Fixture)

- 锚定入口：`TestDarwinNativeFixture` (`internal/safety/darwin_fixture_test.go`，`//go:build darwin`)。
- 验收命令：
  ```bash
  go test -c -mod=readonly -o /tmp/agentpulse-safety-darwin.test ./internal/safety/
  /tmp/agentpulse-safety-darwin.test -test.run ^TestDarwinNativeFixture$ -test.v -test.count=1 -test.timeout=30s
  ```
- 护栏特征：
  1. 仅使用进程内部自行创建的合成测试目录与哨兵文件（`.ssh/id_ed25519`、`.config/agentpulse/config.yaml`、`Library/Keychains/login.keychain-db` 假对象），验证外部资产默认读写拒绝，绝不触碰真实宿主文件与凭据。
  2. 同一测试生命周期内构建外部合成目标与工作区符号链接，按每一条 path 绑定其实际 write/remove/read 结果（缺失或冲突拒绝），验证跨目录链接越界读写拒绝（`EPERM`）、根内写允许。
  3. 外部目录及 payload 创建与回收后身份严格执行 no-follow（lstat）校验，配合真实 root 缺失与链接缺失的真实 OS 核验，只有完整证据才判定成功。
  4. 真实 Keychain/securityd IPC 保持 `BLOCKED`，不作虚假声称。
