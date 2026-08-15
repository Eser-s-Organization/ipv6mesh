# IPv6Mesh

IPv6-first P2P Mesh VPN with a virtual IPv4 overlay.

The first release targets node-to-node connectivity on Windows. The design and discussion record are maintained in the [Memory repository](https://github.com/Eser-Tired/Memory).

## Current documentation

- [Architecture design](docs/architecture/2026-08-11-ipv6-first-mesh-vpn-design.md)
- [Implementation plan](docs/superpowers/plans/2026-08-11-ipv6-first-mesh-vpn-implementation.md)

The v0.1 scope is virtual IPv4 node-to-node access, IPv6 Direct connectivity, and trusted Relay fallback. Subnet routing, LAN broadcast replication, exit nodes, mobile clients, and opaque Relay encryption are outside v0.1.

## Windows 用户使用说明：双击 `.exe` 加入虚拟局域网

普通用户只需要双击单文件安装器，按程序提示输入信息。程序会自动请求 UAC 权限、安装并启动服务、加入网络、连接虚拟适配器，并显示本机虚拟 IPv4。管理员需要提前提供控制面地址和每台设备各自的一次性邀请令牌；令牌相当于一次性密码，不要公开或截图分享。

当前调试安装器可从 [GitHub v0.1.0-debug.2 发布页](https://github.com/Eser-s-Organization/ipv6mesh/releases/tag/v0.1.0-debug.2)下载，直接文件地址是 [ipv6mesh-installer-0.1.0-debug.2.exe](https://github.com/Eser-s-Organization/ipv6mesh/releases/download/v0.1.0-debug.2/ipv6mesh-installer-0.1.0-debug.2.exe)。该版本未进行代码签名，首次运行可能出现 SmartScreen 的“未识别发布者”提示；确认下载来源和 SHA-256 后再继续。

### 1. 双击安装并加入网络

将 `.exe` 下载到本地后，直接双击运行。程序会依次提示：

1. Windows 弹出 UAC 时，点击“是”。
2. 在 `Control-plane URL` 中输入管理员提供的控制面地址，例如 `http://[2001:db8::1]:8080`；IPv6 地址必须使用方括号。
3. 在 `One-time invite token` 中粘贴本机专属的一次性邀请令牌。
4. 在 `Device name` 中输入设备名称；直接按 Enter 会使用当前 Windows 计算机名。
5. 程序自动安装服务、使用令牌加入网络并连接虚拟网卡。
6. 成功后程序显示 `Network`、`Virtual IPv4` 和 `Path`。记下 `Virtual IPv4`，它就是本机在虚拟局域网中的地址。

如果程序发现本机已经加入网络，则不会再次要求邀请令牌，而是直接显示当前成员信息并连接。成功后按 Enter 关闭向导；不要重复使用已经成功的邀请令牌。普通用户不需要打开 PowerShell，也不需要输入 `vpnctl` 命令。

可选的安全检查是在双击前运行 `.exe -verify-payload` 验证内嵌文件；普通联机用户不需要执行该检查。

### 2. 两台电脑联机

在第二台电脑上重复双击同一个 `.exe` 的流程，但必须使用第二个、不同的一次性邀请令牌和不同的设备名。两台电脑都显示 `IPv6Mesh is connected` 后，再启动 Steam 游戏。

不同游戏可能还需要自己的监听端口、Windows 防火墙规则或局域网发现机制；当前版本保证的是加入 VPN 的设备彼此 IP 互访，不保证所有 Steam 游戏自动显示为 LAN 游戏。

开发者需要进行 ping/TCP 验收时，可以使用安装目录中的脚本：

```powershell
& 'C:\Program Files\IPv6Mesh\acceptance.ps1' `
    -NetworkId '<network-id>' `
    -PeerVirtualIPv4 '<对端 virtual_ipv4>'
```

### 常见问题

- `open \\.\pipe\ipv6mesh: The system cannot find the file specified.`：服务没有成功安装或没有运行。重新双击 `.exe` 并确认 UAC 已授权。
- 控制面连接失败：确认控制面进程仍在运行、TCP 8080 已放行、IPv6 地址可达，并且 URL 使用了 `http://[IPv6]:8080` 格式。
- `invite already used`：邀请令牌是一次性的，需要管理员生成新的令牌。
- 服务连接成功但游戏无法发现：先确认两台程序都显示 `IPv6Mesh is connected`，再检查具体游戏的监听端口和 Windows 防火墙。
- 需要导出诊断信息时，使用安装目录中的脚本：

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
