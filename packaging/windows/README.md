# Windows package

This directory contains the Windows packaging scripts for the host-bound room
workflow. The official `wireguard.dll` is not committed; provide it from the
official WireGuardNT package together with its license when building.

## Build

Run from the repository root:

```powershell
.\packaging\windows\build-installer.ps1 `
    -GoCommand 'C:\path\to\go.exe' `
    -WireGuardDll 'C:\path\to\wireguard.dll' `
    -WireGuardLicense 'C:\path\to\LICENSE.txt' `
    -Version '0.1.0-dev'
```

The generated installer embeds the current Windows service, CLI, control server,
room UI, install scripts, WireGuard DLL, and license. Generated payload files and
the installer output are ignored by Git.

## Normal room workflow

Both users open the same installer. The welcome page has two actions:

1. **创建网络** detects a usable global IPv6, starts an ephemeral room control
   plane, creates the room, installs the node service, and joins the host.
2. **加入网络** asks only for the host's IPv6 address and a local device name. It
   does not ask for a Network ID, invitation, administrator token, or approval.

The create and join pages keep diagnostics and logs always visible. Node status
refreshes every two seconds, while the log updates immediately and does not repeat
unchanged status. The welcome page hides diagnostics and pauses status refresh.
The Windows room window can be resized. On the create and join pages, drag the
horizontal diagnostics divider to allocate more space to room settings or logs.
On a constrained display or with larger system text, the operation area scrolls
instead of covering diagnostics or other controls.

The UI itself starts even when the current computer has no global IPv6. In that
case the host create action stays unavailable until a valid host address is found,
but the member page remains usable for entering the host's IPv6. Closing the host
ends the in-memory room; reopening creates a new room.

### 房间成员列表

加入成功后，创建页和加入页都会显示房间成员列表，列出每个成员的**名称**、
**虚拟 IPv4**和固定状态**在线**。宽窗口使用设置区右侧的**右侧成员栏**；
窄窗口把成员列表下移到设置区下方、诊断区上方。成员列表复用每两秒一次的
状态刷新；临时读取失败时保留上一次成功的成员行。

成员安装节点服务前，UI 会先请求房主 `/healthz`。创建模式和加入模式不能同时
处于活动状态：房主必须先点击**结束房间**，成员必须先点击**离开房间**，然后
才能选择另一种模式。第二个 UI 进程会被拒绝；关闭房主窗口仍会结束内存房间。

房间流程不会自动重试、不显示离线状态，也不会自动恢复上一次房间。
遇到 `control_unreachable` 时确认房主窗口和 TCP 8080；遇到
`operation_timeout` 时检查网络后再重试。

The room endpoint is derived as `http://[host-ipv6]:8080`. The host must allow TCP
8080, both nodes must allow WireGuard UDP 51820, and the host's global IPv6 must be
reachable from the member. The normal UI never displays internal bootstrap tokens,
session material, private keys, or invitations.

## Developer compatibility commands

The legacy invite workflow remains available for persistent or separately operated
control planes. These commands are not part of the normal room UI:

```powershell
.\install.ps1 -ControlUrl 'http://[control-plane-ipv6]:8080' -StartService
$env:IPV6MESH_CONTROL_URL = 'https://control.example.invalid'
$env:IPV6MESH_ADMIN_TOKEN = '<bootstrap-token>'
vpnctl network create --name friends --pool 10.42.0.0/24
vpnctl invite create --network <network-id> --expires 1h
vpnctl join --invite <one-time-invite> --name <device-name>
```

For room-mode command-line testing, use:

```text
vpnctl room endpoint --host-ipv6 <ipv6>
vpnctl room create --name <name> --pool 10.42.0.0/24
vpnctl room join --host-ipv6 <ipv6> --name <device>
```

Use placeholders in documentation and keep real credentials out of logs, source
control, screenshots, and issue reports.
