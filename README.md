# IPv6Mesh

IPv6-first P2P Mesh VPN with a virtual IPv4 overlay.

The first release targets node-to-node connectivity on Windows. The design and discussion record are maintained in the [Memory repository](https://github.com/Eser-Tired/Memory).

## Current documentation

- [Architecture design](docs/architecture/2026-08-11-ipv6-first-mesh-vpn-design.md)
- [Implementation plan](docs/superpowers/plans/2026-08-11-ipv6-first-mesh-vpn-implementation.md)

The v0.1 scope is virtual IPv4 node-to-node access, IPv6 Direct connectivity, and trusted Relay fallback. Subnet routing, LAN broadcast replication, exit nodes, mobile clients, and opaque Relay encryption are outside v0.1.

## Windows 用户使用说明：使用 `.exe` 加入虚拟局域网

当前 Windows 调试版可以直接通过单文件安装器加入 IPv6Mesh。它的作用是：在每台 Windows 电脑上安装并启动 IPv6Mesh 服务，再通过控制面加入同一个网络，为设备分配虚拟 IPv4 地址。安装器不会自动创建网络，也不会自动生成邀请；管理员需要提前提供控制面地址、网络 ID，以及每台设备各自的一次性邀请令牌。

当前调试安装器可从 [GitHub v0.1.0-debug.1 发布页](https://github.com/Eser-s-Organization/ipv6mesh/releases/tag/v0.1.0-debug.1)下载，直接文件地址是 [ipv6mesh-installer-debug-0.1.0.exe](https://github.com/Eser-s-Organization/ipv6mesh/releases/download/v0.1.0-debug.1/ipv6mesh-installer-debug-0.1.0.exe)。该版本未进行代码签名，首次运行可能出现 SmartScreen 的“未识别发布者”提示；确认下载来源和 SHA-256 后再继续。

### 1. 每台电脑安装节点

将 `.exe` 下载到本地后，可以先在 PowerShell 中验证它内嵌的安装载荷：

```powershell
Get-FileHash .\ipv6mesh-installer-debug-0.1.0.exe -Algorithm SHA256
.\ipv6mesh-installer-debug-0.1.0.exe -verify-payload
```

然后传入控制面 URL 安装。IPv6 地址必须使用方括号包围：

```powershell
.\ipv6mesh-installer-debug-0.1.0.exe `
    -control-url 'http://[控制面IPv6]:8080'
```

安装器会自动请求 UAC 管理员权限，默认安装并启动 `IPv6Mesh` 服务。安装后检查：

```powershell
Get-Service IPv6Mesh
```

如果不希望安装后立即启动服务，可以使用 `-start-service=false`；如果需要保留临时解压目录进行调试，可以使用 `-keep-temp`。

当前版本的本地 Named Pipe 只允许 `Administrators` 组访问，因此后续 `vpnctl join/status/connect` 命令也请在“管理员 PowerShell”中执行。

### 2. 加入网络并连接

在每台电脑上使用不同的设备名和不同的一次性邀请令牌。不要把同一个令牌用于两台电脑，也不要重复执行已经成功的 `join`：

```powershell
$vpnctl = 'C:\Program Files\IPv6Mesh\vpnctl.exe'

& $vpnctl join `
    --invite '<本设备的一次性邀请令牌>' `
    --name 'win11-pc-1'

& $vpnctl status

& $vpnctl connect `
    --network '<管理员提供的 network-id>'

& $vpnctl status
```

第二台电脑执行相同流程，但使用第二个邀请令牌和不同的名称，例如 `win11-pc-2`。`status` 输出中的 `virtual_ipv4` 是本机在虚拟局域网内的地址。

### 3. 验证两台电脑互访

在第一台电脑上 ping 第二台 `status` 中显示的虚拟 IPv4：

```powershell
ping.exe -4 -n 4 <第二台电脑的 virtual_ipv4>
```

再在第二台电脑上反向 ping 第一台地址。也可以使用 TCP 验收脚本：

```powershell
& 'C:\Program Files\IPv6Mesh\acceptance.ps1' `
    -NetworkId '<network-id>' `
    -PeerVirtualIPv4 '<对端 virtual_ipv4>'
```

只有在两台电脑的虚拟 IPv4 ping/TCP 互通后，才适合继续测试具体 Steam 游戏。不同游戏可能还需要自己的监听端口、Windows 防火墙规则或局域网发现机制；当前版本保证的是加入 VPN 的设备彼此 IP 互访，不保证所有 Steam 游戏自动显示为 LAN 游戏。

### 常见问题

- `open \\.\pipe\ipv6mesh: The system cannot find the file specified.`：服务没有成功安装或没有运行。检查 `Get-Service IPv6Mesh`，必要时重新运行 `.exe`，并确认 UAC 已授权。
- 控制面连接失败：确认控制面进程仍在运行、TCP 8080 已放行、IPv6 地址可达，并且 URL 使用了 `http://[IPv6]:8080` 格式。
- `invite already used`：邀请令牌是一次性的，需要管理员生成新的令牌。
- 服务正常但 ping 失败：确认两台电脑都执行了 `connect`，使用的是对端 `virtual_ipv4`，并检查 Windows 防火墙和诊断报告：

  ```powershell
  & 'C:\Program Files\IPv6Mesh\diagnose.ps1' -OutputPath .\ipv6mesh-diagnose.json
  ```

### 卸载

以管理员 PowerShell 运行安装目录中的卸载脚本：

```powershell
& 'C:\Program Files\IPv6Mesh\uninstall.ps1'
```

默认保留 `C:\ProgramData\IPv6Mesh` 中的节点身份和数据；确认不再需要时可以追加 `-RemoveData`。

## Implemented milestones

- Control-plane enrollment, stable virtual IPv4 allocation, scoped authorization, and versioned snapshots.
- Windows service boundary with protected identity storage and strict Named Pipe IPC.
- WireGuardNT ABI adapter and Windows IP Helper route reconciler with host-only overlay routes. The official `wireguard.dll` is intentionally not committed; see [runtime provenance](third_party/wireguardnt/README.md).
- Windows IPv6 candidate discovery, authenticated control-plane heartbeats, strict HTTPS/WebSocket client decoding with bounded reconnect, and generation-safe WireGuard/IPv4 snapshot reconciliation.
- Trusted Relay configuration validation, Linux `wg`/`ip` execution boundaries, owned overlay-route cleanup, and Direct/Relay path hysteresis.

Live Relay failover, Linux network-namespace validation, live two-node WireGuardNT validation, and administrator-level route/DLL acceptance tests still require the later implementation tasks and multi-node acceptance tests.
