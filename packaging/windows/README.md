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

The UI itself starts even when the current computer has no global IPv6. In that
case the host create action stays unavailable until a valid host address is found,
but the member page remains usable for entering the host's IPv6. Closing the host
ends the in-memory room; reopening creates a new room.

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
