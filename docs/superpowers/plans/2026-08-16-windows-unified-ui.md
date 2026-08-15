# Windows Unified UI Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将现有自包含 Windows `.exe` 安装器改为带中文可视化界面的统一操作程序，覆盖控制面管理员、游戏房主和游戏成员，并提供可复制、可导出的调试日志。

**Architecture:** 保留 Go 安装器负责 UAC 提权、载荷解压和命令行兼容路径；普通双击路径启动载荷内的 Windows PowerShell WinForms UI。UI 通过现有 `control-server.exe`、`vpnctl.exe`、`install.ps1` 和 Named Pipe 协议完成控制面、网络邀请和节点操作，不重复实现控制面协议或数据面逻辑。控制面进程的标准输出/错误输出和 UI 操作结果统一进入内存日志窗口，邀请令牌只放在专用输入框中，不写入日志。

**Tech Stack:** Go 1.x、`golang.org/x/sys/windows`、Windows PowerShell 5.1 WinForms、现有 IPv6Mesh control-plane HTTP API、Windows service/Named Pipe IPC、PowerShell 打包脚本和 GitHub Actions。

---

### Task 1: UI 启动入口与错误边界

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows.go`
- Test: `cmd/ipv6mesh-installer/main_windows_test.go`

- [ ] **Step 1: 为交互模式定义 UI 启动参数**

保留 `-non-interactive` 的现有命令行安装/入网路径；普通双击模式不再通过标准输入读取 URL、邀请或设备名，而是将临时载荷目录、安装目录、版本号和已有 flag 初值传给 `ui.ps1`。

- [ ] **Step 2: 在管理员进程中启动 WinForms UI**

新增 `runGraphical`，使用现有 `findPowerShell`，以 `-STA -ExecutionPolicy Bypass -File <temp>\ui.ps1` 启动 UI；UI 退出后由安装器清理临时目录，UI 失败时保留临时目录并显示 Windows 错误消息框。

- [ ] **Step 3: 为 GUI 子系统增加可见错误提示**

新增 `showInstallerError`，使用 `user32.MessageBoxW` 或 `windows.MessageBox` 显示中文错误；普通交互模式不再调用不可见的 stdin 等待逻辑，非交互模式继续返回非零退出码。

- [ ] **Step 4: 锁定 payload 必需文件的回归检查**

测试要求 UI 载荷必须包含 `ui.ps1`，并确认 GUI 启动参数中传递了 `-PackageDirectory`、`-InstallDirectory` 和可选的 `-ControlUrl`、`-Invite`、`-DeviceName`、`-Network`。

### Task 2: 中文 WinForms UI 和角色操作

**Files:**
- Create: `packaging/windows/ui.ps1`
- Modify: `packaging/windows/build.ps1`
- Modify: `cmd/ipv6mesh-installer/main_windows.go`

- [ ] **Step 1: 建立统一窗口和角色选择**

使用 Windows 内置 `System.Windows.Forms`，窗口标题为 `IPv6Mesh 远程组网`，包含角色下拉框：`控制面管理员`、`游戏房主`、`游戏成员`。窗口包含控制面 URL、安装路径、设备名、网络名、IPv4 地址池、邀请有效期、管理员令牌、房主邀请令牌、成员邀请令牌等中文字段。

- [ ] **Step 2: 实现控制面管理员操作**

管理员按钮执行以下现有流程：

1. 以环境变量 `CONTROL_LISTEN_ADDRESS`、`CONTROL_BOOTSTRAP_TOKEN`、`CONTROL_SESSION_TTL`、`CONTROL_INVITE_TTL` 和 `CONTROL_REPOSITORY_MODE=memory` 启动 `control-server.exe`；
2. 检查 `<控制面 URL>/healthz`；
3. 调用 `vpnctl.exe network create --name <名称> --pool <CIDR>`；
4. 调用两次 `vpnctl.exe invite create --network <ID> --expires <时长>`，分别填入房主邀请框和成员邀请框；
5. 提供“复制邀请”按钮，但日志只记录“已生成邀请”，不记录令牌正文。

控制面进程使用重定向标准输出/错误输出和 `BeginOutputReadLine`，退出码、健康检查响应和命令结果均写入日志窗口。

- [ ] **Step 3: 实现房主和成员安装入网操作**

房主与成员按钮共用节点流程：

1. 调用载荷内 `install.ps1`，使用当前控制面 URL，停止旧服务、复制二进制、注册/启动 `IPv6Mesh` 服务；
2. 调用 `vpnctl.exe join --invite <令牌> --name <设备名>`；
3. 调用 `vpnctl.exe status`，解析 JSON 显示网络 ID、虚拟 IPv4、路径和最近错误；
4. 调用 `vpnctl.exe connect --network <网络 ID>`；
5. 提供“刷新状态”“连接”“断开”“离开网络”按钮。

房主和成员共享节点操作区，但角色切换时令牌标签、提示和日志说明保持中文区分。邀请正文不进入普通日志。

- [ ] **Step 4: 实现日志窗口功能**

日志区使用只读多行文本框，追加时间戳、级别和来源；提供“清空日志”“复制日志”“导出日志”按钮。导出的 UTF-8 文件默认保存到桌面或用户选择的路径，导出前显示不包含令牌的提醒。

- [ ] **Step 5: 实现错误和退出清理**

网络请求、子进程失败、服务未启动、邀请已使用和 JSON 解析失败都显示中文错误并记录可诊断信息；退出时停止由 UI 启动且仍在运行的控制面进程，已安装的 Windows 服务不自动删除。

### Task 3: 打包和回归验证

**Files:**
- Modify: `packaging/windows/build.ps1`
- Modify: `packaging/windows/build-installer.ps1`
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`

- [ ] **Step 1: 将 UI 脚本加入 payload**

让 `build.ps1` 将 `ui.ps1` 复制到 Windows 包输出；让 `verifyPayload` 检查 `ui.ps1`；安装器载荷仍包含现有服务、CLI、控制面、WireGuardNT DLL、许可证和脚本。

- [ ] **Step 2: 以 Windows GUI 子系统构建普通安装器**

在安装器构建的 linker flags 中加入 `-H=windowsgui`，避免双击时出现孤立控制台窗口；`-non-interactive` 继续支持开发者从 PowerShell 调试。

- [ ] **Step 3: 加入脚本内容回归检查**

检查 `build.ps1` 包含 `ui.ps1`，UI 脚本包含三个角色中文文本、`System.Windows.Forms`、`network create`、`invite create`、`status`、`connect` 和日志导出入口。

- [ ] **Step 4: 执行验证**

运行 `go test -count=1 ./...`、`go vet ./...`、`gofmt -l .`、`git diff --check`、Windows PowerShell 解析检查、`build.ps1` 载荷构建、`build-installer.ps1 -verify-payload` 和最终 `.exe -version`/`-verify-payload`。

### Task 4: 文档、Memory 和发布

**Files:**
- Modify: `README.md`
- Modify: `packaging/windows/README.md`
- Modify: `records/IPv6MeshVPN/0811_首个控制面里程碑实施记录_v1.md` in `C:\Users\Eser\Documents\Codex\2026-08-11\memory-record`
- Modify: `INDEX.md` in `C:\Users\Eser\Documents\Codex\2026-08-11\memory-record`

- [ ] **Step 1: 更新 README**

把普通用户入口改为双击新版 UI `.exe`，说明角色切换、管理员启动控制面/创建网络/生成令牌、房主和成员安装入网、日志窗口、复制/导出日志、令牌安全和当前 Steam 发现边界，并保留开发者 CLI 兼容说明。

- [ ] **Step 2: 更新 Windows 打包说明**

记录新版 UI 的实际按钮顺序、首次运行 UAC、三种角色的最小输入、日志文件位置和旧版本安装器兼容边界。

- [ ] **Step 3: 记录并同步 Memory**

记录源码提交、PR、CI、Release 资产 SHA-256、UI 能力、验证范围和未完成的真实双机/Steam 实测；不记录管理员令牌、邀请令牌或私钥。

- [ ] **Step 4: 提交、推送、合并和发布**

从 `agent/windows-unified-ui` 提交并推送，等待 Linux/Windows push 与 PR 检查；合并到 `main` 后创建递增的 `v0.1.0-debug.5` Release，上传 `.exe` 与 `.sha256`，并用 GitHub Release 资产重新核对 SHA-256。
