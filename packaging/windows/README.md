# Windows MVP package

这个目录提供 Windows 双机验收所需的脚本。`wireguard.dll` 不在仓库中，必须从官方 WireGuardNT 发布包取得，并与 `vpn-service.exe` 放在同一目录。

## 构建

在源码仓库根目录执行：

```powershell
.\packaging\windows\build.ps1 -GoCommand 'C:\Users\Eser\.cache\codex-go\go\bin\go.exe' -WireGuardDll 'C:\path\to\wireguard.dll' -WireGuardLicense 'C:\path\to\LICENSE.txt'
```

如果暂时没有 DLL，构建脚本仍可生成控制面和客户端二进制，但安装脚本会拒绝安装数据面。

## 单文件调试安装器

可以把节点运行所需的 Windows 二进制、官方 WireGuardNT DLL、许可证和安装脚本封装为一个自包含的 `.exe`。构建时必须提供官方 SDK 中的 `wireguard.dll` 和对应许可证：

```powershell
.\packaging\windows\build-installer.ps1 `
    -GoCommand 'C:\Users\Eser\.cache\codex-go\go\bin\go.exe' `
    -WireGuardDll 'C:\path\to\wireguard.dll' `
    -WireGuardLicense 'C:\path\to\LICENSE.txt' `
    -Version '0.1.0-debug.8'
```

生成的 `ipv6mesh-installer.exe` 面向普通用户的默认操作是双击运行。程序会自动请求 UAC 管理员权限，然后打开中文 WinForms 界面。界面顶部统一提供“控制面管理员”“游戏房主”“游戏成员”三种角色；管理员可以自动检测本机 IPv6、输入端口生成控制面 URL、启动控制面、检查健康、创建网络、生成并复制两种邀请，房主/成员可以安装服务、加入、连接、断开和离开网络。加入时虚拟 IPv4 由控制面从地址池随机分配并保持稳定。下方日志窗口会收集控制面、安装脚本和 `vpnctl` 输出，并提供复制/导出功能；管理员令牌和一次性邀请令牌不会写入日志。下面的带参数命令仅用于开发者调试：

```powershell
.\ipv6mesh-installer.exe `
    -control-url 'http://[2001:db8::1]:8080'
```

双击时不需要传 `-control-url`，程序会在 UI 中自动检测并填写本机 IPv6、默认端口 `8080` 和控制面 URL；选择“游戏房主”或“游戏成员”后点击“安装并加入 VPN”，默认会停止旧服务、安装/更新 `IPv6Mesh` 服务、加入网络和连接。正常关闭 UI 时，会停止本机节点服务并清理 WireGuard 适配器、虚拟 IPv4 地址和路由，同时停止本窗口启动的控制面；安装文件和节点身份会保留。选择“控制面管理员”后可以启动控制面并生成邀请。`-non-interactive`、`-start-service=false`、`-connect=false` 和 `-keep-temp` 是开发者调试选项。该调试安装器目前未进行代码签名，Windows SmartScreen 可能显示未识别发布者提示；发布前应使用正式证书签名。

发布前或人工调试时，可以不提权只验证 `.exe` 内嵌的载荷：

```powershell
.\ipv6mesh-installer.exe -verify-payload
```

## 启动临时控制面

第一轮验收可以使用内存仓库；进程退出后网络、邀请和会话会丢失。生产环境应设置 `CONTROL_DB_DSN` 使用 PostgreSQL。

也可以直接使用脚本，它会在当前终端设置环境变量，并可选地放行 TCP 8080：

```powershell
.\start-control-server.ps1 -BootstrapToken '<只保存在本机终端的管理员令牌>' -OpenFirewall
```

脚本当前使用内存仓库；进程退出后网络、邀请和会话会丢失。

房主/管理员的完整角色分工、网络创建、邀请令牌分配，以及第一台和第二台 Windows 11 的具体输入示例，请先阅读仓库根目录的 [Windows 房主与成员联机说明](../../README.md#windows-用户使用说明房主与成员双击-exe-入网)。

在控制面所在电脑放行 TCP 8080，并把该电脑的公网 IPv6 写成客户端地址，例如：

```text
http://[2001:db8::1]:8080
```

## 安装节点：房主和成员的普通流程

普通用户只需使用 GitHub 发布页中的单文件安装器，不需要手动运行 `vpnctl`：

1. 在每台 Windows 电脑下载同一版本的 `ipv6mesh-installer-0.1.0-debug.8.exe`。
2. 双击运行，在 UAC 对话框中点击“是”。
3. 在顶部选择“游戏房主”或“游戏成员”，输入控制面 URL、该设备专属的一次性邀请令牌和设备名。
4. 点击“安装并加入 VPN”，在状态和日志中查看 Network ID、虚拟 IPv4 和 Path。
5. 第二台电脑重复上述流程，但使用不同的一次性邀请令牌和设备名。

控制面管理员在同一个 UI 中选择“控制面管理员”，确认自动检测到的本机 IPv6，输入端口（默认 `8080`），让 UI 生成控制面 URL 和监听地址，再填写管理员令牌并点击“启动控制面”；然后依次点击“检查健康”“创建网络”“生成房主邀请”“生成成员邀请”。如果控制面运行在当前电脑，UI 会尝试自动放行对应 TCP 端口；仍需确认路由器/校园网允许 IPv6 入站访问。

日志区支持“清空日志”“复制日志”“导出日志”。导出前请检查其中没有不希望公开的主机信息；邀请令牌只能通过专用令牌框复制，不要从命令行或日志中传播。

如果旧版 `.5`、`.6` 或 `.7` 弹出 `run Chinese IPv6Mesh UI: exit status 1`，请改用 `.8`。`.8` 同时包含 UTF-8 with BOM 修复、IPv6/端口自动生成 URL、完整退出清理和 WireGuard 虚拟适配器删除。

完整的面向用户的 `.exe` 联机流程、房主/成员角色、双机互访验证和故障排查请参见仓库根目录的 [Windows 房主与成员联机说明](../../README.md#windows-用户使用说明房主与成员双击-exe-入网)。

普通成员到这里已经完成，不需要执行下面的开发者命令。

## 开发者调试（普通成员不需要）

如果要手动检查服务或复现底层问题，才以管理员身份执行：

```powershell
.\install.ps1 -ControlUrl 'http://[控制面IPv6]:8080' -StartService
```

然后使用 `vpnctl`：

```powershell
$env:IPV6MESH_CONTROL_URL = 'http://[控制面IPv6]:8080'
vpnctl join --invite '<一次性邀请令牌>' --name 'device-a'
vpnctl status
vpnctl connect --network '<network-id>'
```

## 双机验收（管理员/开发者）

两台设备都加入并连接后，在其中一台执行：

```powershell
.\acceptance.ps1 -NetworkId '<network-id>' -PeerVirtualIPv4 '<另一台设备的虚拟IPv4>'
```

诊断信息可用以下命令导出；分享前请检查报告中是否含有不希望公开的主机信息：

```powershell
.\diagnose.ps1 -OutputPath .\ipv6mesh-diagnose.json
```

当前验收范围是虚拟 IPv4 的 ping/TCP 互访。Steam 游戏联机需要在此基础上再确认具体游戏的监听端口、Windows 防火墙和 Steam 的网络发现行为。
