# macOS App 打包指南 (Apple Silicon)

本文档介绍如何将 AgentPulse CLI 打包为适用于 Apple Silicon (macOS `darwin/arm64`) 的标准 macOS App Bundle (`AgentPulse.app`)。

---

## 1. 概述与设计定位

AgentPulse CLI 是一款专为 Google Antigravity (`agy`) 与 OpenCode 多机工作流设计的轻量终端工具。为提升在 macOS 桌面环境下的可用性与开箱体验，项目提供了自动化打包脚本 `scripts/build-macos-app.sh`，生成标准 macOS 应用包（Application Bundle）。

- **目标架构**：Apple Silicon (`darwin/arm64`，M1/M2/M3/M4 系列芯片)
- **产物定位**：无需修改 Go 源码版本号，纯净产出独立 `.app` 包
- **无重型依赖**：无 Electron/SwiftUI/菜单栏常驻壳，保持 Go CLI 原生高效特性
- **免商业证书**：离线可构建，默认无需付费 Apple Developer 证书签名及公证流程

---

## 2. Bundle 目录结构

产物默认输出至 `dist/AgentPulse.app`，其内部目录结构如下：

```
dist/AgentPulse.app/
└── Contents/
    ├── Info.plist              # macOS 属性列表配置（Bundle 元数据、架构约束）
    ├── MacOS/
    │   ├── agentpulse          # 启动器脚本（兼顾终端透传与 Finder 双击交互）
    │   └── agentpulse-bin      # 真正的 Go 原生 Mach-O 64-bit arm64 二进制
    └── Resources/
        └── README.txt          # 资源占位文档
```

### `Info.plist` 关键键值

- `CFBundleIdentifier`: `com.mayingjie.agentpulse`
- `CFBundleName` / `CFBundleDisplayName`: `AgentPulse`
- `CFBundleExecutable`: `agentpulse`（指向 `Contents/MacOS/agentpulse`）
- `CFBundlePackageType`: `APPL`
- `LSMinimumSystemVersion`: `12.0`
- `LSArchitecturePriority`: 声明优先使用 `arm64` 架构
- `CFBundleShortVersionString` / `CFBundleVersion`: 动态提取自 `internal/version/version.go`（例如 `0.2.0`）

---

## 3. 构建步骤

### 3.1 前置要求

- Apple Silicon Mac（`arm64`）
- **Go 1.22+**
- Bash 环境与常用 macOS 命令行工具（`file`, `lipo`, `osascript`）

### 3.2 执行构建

在仓库根目录下运行：

```bash
bash scripts/build-macos-app.sh
```

构建脚本会自动完成以下操作：
1. 清理上一次的 `dist/AgentPulse.app`；
2. 读取版本号与 Git commit 信息；
3. 执行 `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath` 编译 Go 二进制；
4. 写入 Launcher 脚本并赋予执行权限；
5. 生成标准 `Info.plist` 与 `Resources` 目录；
6. 自动调用 `file` / `lipo -info` 校验产物架构为 `arm64`。

---

## 4. 运行与交互体验

### 4.1 Finder 双击行为（Launcher 原理）

由于 CLI 工具在没有终端附加的情况下被 Finder 启动时会因为没有分配 TTY 而瞬时退出，`AgentPulse.app` 内置了智能 Launcher（`Contents/MacOS/agentpulse`）：

- **无 TTY（Finder 双击）**：检测到未附加交互终端时，自动唤起系统的 Terminal.app，打印 `agentpulse version` 和 CLI 帮助手册，并使终端停留在交互 shell（如 zsh/bash），方便用户直接输入 `agentpulse watch` 或其它子命令。
- **已附加 TTY（终端执行）**：当用户在已打开的终端中调用该脚本时，直接通过 `exec` 透传参数，与直接运行 CLI 无缝一致。

### 4.2 终端直接调用

你也可以在终端中直接将 App Bundle 内的程序作为 CLI 使用：

```bash
# 通过启动器调用（支持任意 CLI 参数）：
./dist/AgentPulse.app/Contents/MacOS/agentpulse version
./dist/AgentPulse.app/Contents/MacOS/agentpulse list
./dist/AgentPulse.app/Contents/MacOS/agentpulse watch

# 或直接调用底层 Go 二进制：
./dist/AgentPulse.app/Contents/MacOS/agentpulse-bin version
```

---

## 5. 校验与测试

### 5.1 架构验证

使用 `file` 或 `lipo` 验证二进制架构确为 `arm64`：

```bash
file dist/AgentPulse.app/Contents/MacOS/agentpulse-bin
# 输出示例：Mach-O 64-bit executable arm64

lipo -info dist/AgentPulse.app/Contents/MacOS/agentpulse-bin
# 输出示例：Non-fat file: ... is architecture: arm64
```

### 5.2 运行测试套件

构建脚本不会改动 Go 源码，可随时执行完整测试：

```bash
go test -race -count=1 ./...
```

---

## 6. Gatekeeper 与安全隔离说明

- 本工程打包采用离线自建流程，**未包含付费 Apple 开发者证书签名与 Notarization（公证）**。
- 若将打包后的 `.app` 通过 AirDrop、网盘或聊天软件分发到其他 Mac，macOS Gatekeeper 可能会弹出「无法打开，因为无法验证开发者」的安全提示。
- **解决方法**：
  1. 在 Finder 中右键（或 Control + 单击）`AgentPulse.app`，选择「打开」，并在弹窗中确认允许运行；
  2. 或在终端中执行解除隔离命令（路径请根据实际情况替换为中性占位路径）：
     ```bash
     xattr -cr /path/to/AgentPulse.app
     ```
