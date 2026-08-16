# IPv6Mesh

IPv6-first P2P Mesh VPN with a virtual IPv4 overlay.

The first release targets node-to-node connectivity on Windows. The control plane
manages membership, virtual IPv4 allocation, endpoint observations, and versioned
snapshots; WireGuard carries the data plane.

## Documentation

- [Architecture design](docs/architecture/2026-08-11-ipv6-first-mesh-vpn-design.md)
- [Operator notes](docs/operator.md)
- [Host-bound room design](docs/superpowers/specs/2026-08-16-host-bound-room-flow-design.md)
- [Host-bound room implementation plan](docs/superpowers/plans/2026-08-16-host-bound-room-flow.md)

## Windows primary workflow: host-bound room

Both people open the same Windows installer. The normal UI has one room workflow
and does not require an administrator token, a Network ID, or an invitation token.

1. The host selects **创建网络**. The installer detects a preferred, non-SkipAsSource
   global IPv6 address in 2000::/3, starts an in-memory room control plane, creates
   one room, installs the node service, and joins the host node.
2. The host waits until the page shows the host IPv6, assigned **房主虚拟 IPv4**,
   and the current path. The host can use **复制房主 IPv6** to share only that
   IPv6 address.
3. The member selects **加入网络**, enters the host IPv6, and clicks join. The
   member page needs no room ID, invitation, or administrator credential.
4. Both pages show the assigned virtual IPv4 and the current Direct/Relay path. Use
   the diagnostics panel only when troubleshooting.
5. Closing the host ends the in-memory room. Reopening the installer creates a new
   room and a new set of assignments.
6. Knowing the host IPv6 grants join access while the room is open. Treat the host
   IPv6 as the room access boundary.
7. The host must allow TCP 8080 to the control plane and both peers must allow
   WireGuard UDP 51820. The host and member must have reachable global IPv6.

Opening the UI does not require a global IPv6 on the current computer. Without one,
the host create action remains unavailable until a valid host address is detected,
while a member can still open **加入网络** and enter the host's IPv6 address.

The room endpoint is always derived from the host IPv6 as
http://[host-ipv6]:8080. Link-local, private, loopback, multicast, unspecified, and
ULA addresses are rejected. The UI does not display the internal bootstrap token,
session material, private keys, or invitation material.

## Developer compatibility workflow

The legacy administrator and invitation commands remain supported for deployments
that need persistent PostgreSQL-backed networks, explicit membership approval, or
a separately operated control plane. They are not required by the primary room UI.

A developer may configure a control client with placeholder credentials:

~~~powershell
$env:IPV6MESH_CONTROL_URL = 'https://control.example.invalid'
$env:IPV6MESH_ADMIN_TOKEN = '<bootstrap-token>'
~~~

The compatibility workflow is:

~~~text
vpnctl network create --name friends --pool 10.42.0.0/24
vpnctl invite create --network <network-id> --expires 1h
vpnctl join --invite <one-time-invite> --name <device-name>
vpnctl status
vpnctl connect --network <network-id>
vpnctl disconnect --network <network-id>
vpnctl leave --network <network-id>
~~~

Invitation values are returned only by an explicit developer command and must not
be written to logs, issues, screenshots, or chat. The legacy paths and the room
paths use the same enrollment, snapshot, and node-service boundaries.

## Developer room commands

The room flow can be exercised without the graphical installer:

~~~text
vpnctl room endpoint --host-ipv6 <ipv6>
vpnctl room create --name <name> --pool 10.42.0.0/24
vpnctl room join --host-ipv6 <ipv6> --name <device>
~~~

Room creation requires a control plane started with room mode enabled. A room-mode
control plane may use an internal bootstrap credential for the authenticated
create call; the graphical host flow generates it in memory and never exposes it.

Room mode is intentionally bounded:

- one active room per control-plane process;
- public join is available only while room mode is enabled and a room exists;
- join is rate-limited and creates a short-lived internal invitation that is never
  returned to the caller;
- the in-memory repository is not durable; stopping the process drops the room,
  memberships, and sessions;
- the legacy invitation workflow remains available when room mode is disabled.

## Windows node and data plane

The Windows service stores its node identity using protected local storage. The
CLI and Named Pipe response never return a private key. IPv6 endpoints are used
for direct connectivity first; IPv4 endpoints and the trusted Relay are fallback
paths. The node service reconciles only virtual IPv4 host routes and does not
replace the system default route.

The official WireGuardNT DLL is intentionally excluded from source control. A
release package must provide the official DLL, its license, and provenance
separately.

## Troubleshooting and acceptance boundary

For a room join failure, check the stable error code in the diagnostics panel:
room_not_ready, room_mode_disabled, node_already_joined, room_full,
join_rate_limited, or enrollment_recovery_pending. Do not copy raw credentials or
session data into a report.

A control-plane restart ends an in-memory room. For a long-lived deployment, use
the legacy persistent repository and its explicit invite workflow.

Automated tests cover the room lifecycle, parser and IPC boundaries, Windows
conditional compilation, and package-script parsing. Real two-computer public IPv6
WireGuard acceptance, administrator permissions, firewall policy, and a complete
installer run must still be performed on the target Windows machines.

## Implemented milestones

- Control-plane enrollment with stable virtual IPv4 allocation and versioned snapshots.
- Opt-in host-bound room creation, public room enrollment, bounded rate limiting,
  and invite cleanup on enrollment failure.
- Strict CLI, IPC, and privileged service room workflow boundaries.
- Windows create-or-join UI with global IPv6 filtering and resource cleanup.
- WireGuardNT ABI adapter, Windows route reconciliation, endpoint discovery, and
  Direct/Relay path hysteresis.
- Trusted Relay configuration validation and Linux execution boundaries.
