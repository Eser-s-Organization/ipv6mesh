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
    -Version '0.1.0-debug.1'
```

生成的 `ipv6mesh-installer.exe` 会自动请求 UAC 管理员权限；运行时可直接传入控制面地址：

```powershell
.\ipv6mesh-installer.exe `
    -control-url 'http://[2001:db8::1]:8080'
```

不传 `-control-url` 时，安装器会在控制台中提示输入。默认安装后自动启动 `IPv6Mesh` 服务；使用 `-start-service=false` 可只安装不启动。`-keep-temp` 可保留解压目录，便于检查安装脚本和载荷。该调试安装器目前未进行代码签名，Windows SmartScreen 可能显示未识别发布者提示；发布前应使用正式证书签名。

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

在控制面所在电脑放行 TCP 8080，并把该电脑的公网 IPv6 写成客户端地址，例如：

```text
http://[2001:db8::1]:8080
```

## 安装节点

以管理员身份执行：

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

## 双机验收

两台设备都加入并连接后，在其中一台执行：

```powershell
.\acceptance.ps1 -NetworkId '<network-id>' -PeerVirtualIPv4 '<另一台设备的虚拟IPv4>'
```

诊断信息可用以下命令导出；分享前请检查报告中是否含有不希望公开的主机信息：

```powershell
.\diagnose.ps1 -OutputPath .\ipv6mesh-diagnose.json
```

当前验收范围是虚拟 IPv4 的 ping/TCP 互访。Steam 游戏联机需要在此基础上再确认具体游戏的监听端口、Windows 防火墙和 Steam 的网络发现行为。
