# IPv6-first Mesh VPN Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.
>
> The design and decision record are maintained in the Memory repository. This file is the implementation plan for the source repository Eser-s-Organization/ipv6mesh.

**Goal:** Build a Windows-first, IPv6-first Mesh VPN that gives enrolled devices stable virtual IPv4 addresses, prefers direct IPv6 WireGuard paths, and falls back to a trusted Relay when direct connectivity fails.

**Architecture:** A privileged Windows service owns identity, WireGuardNT, virtual IPv4, routes, endpoint discovery, and path switching. A Control Server owns enrollment, membership, address allocation, Peer snapshots, and events but does not carry normal VPN data. A Linux Relay runs a standard WireGuard Hub and forwards only registered overlay IPv4 addresses.

**Tech Stack:** Go for the client service, control server, relay agent, and CLI; WireGuardNT and its embeddable Windows DLL API for the Windows data plane; Windows IP Helper API for addresses and routes; HTTPS/WebSocket plus a UDP rendezvous socket for control and endpoint discovery; PostgreSQL for production server storage; WPF or an equivalent small Windows frontend over the service Named Pipe after the CLI path is stable.

---

## Scope and implementation assumptions

The first implementation increment must provide:

- node-to-node access over virtual IPv4;
- Windows client service;
- local key generation and protected key storage;
- one-time enrollment invitations;
- IPv4 allocation with conflict reporting;
- direct IPv6 WireGuard connectivity;
- trusted WireGuard Relay fallback;
- Direct/Relay path status;
- no default-route takeover;
- ping, TCP, UDP, SSH/RDP, and Direct-IP validation.

It must not implement subnet routing, exit nodes, LAN broadcast or multicast, TAP/L2 bridging, mobile clients, opaque Relay encryption, or custom cryptography.

The source repository layout is:

~~~text
ipv6mesh/
├── cmd/
├── internal/
├── ui/
├── deploy/
├── test/
├── packaging/
├── go.mod
├── Makefile
└── .github/workflows/
~~~

The CLI is the first operator surface. The GUI uses the same service protocol and is added after the network core is testable.

## Task 1: Bootstrap the source repository and build gates

**Files:**

- Create: go.mod
- Create: cmd/control-server/main.go
- Create: cmd/vpn-service/main_windows.go
- Create: cmd/vpnctl/main.go
- Create: cmd/relay-agent/main_linux.go
- Create: .github/workflows/test.yml
- Create: Makefile
- Create: internal/build/build_test.go

- [ ] **Step 1: Create the module and platform-specific entrypoints**

Use the module path github.com/Eser-s-Organization/ipv6mesh. Keep the Windows service and Linux Relay command behind platform build constraints so common packages remain testable on the host platform.

~~~go
// cmd/vpn-service/main_windows.go
//go:build windows

package main

func main() {
    runService()
}
~~~

~~~go
// cmd/relay-agent/main_linux.go
//go:build linux

package main

func main() {
    runRelayAgent()
}
~~~

- [ ] **Step 2: Add the first package-level build test**

~~~go
package build_test

import "testing"

func TestRepositoryBootstrap(t *testing.T) {
    t.Helper()
}
~~~

Run:

~~~text
go test ./...
~~~

Expected: PASS with the common packages and host-platform entrypoints compiling.

- [ ] **Step 3: Add repeatable checks**

The Makefile must expose test, vet, fmt-check, and check targets. The check target must run gofmt, go test, go vet, and verify that gofmt has no remaining output.

- [ ] **Step 4: Add CI**

The workflow must run go test ./..., go vet ./..., and gofmt -l . on Linux. A Windows job must compile Windows packages and run the Windows unit tests.

- [ ] **Step 5: Commit the bootstrap**

~~~text
git add go.mod cmd internal/build .github/workflows/test.yml Makefile
git commit -m "build: bootstrap IPv6 mesh VPN source tree"
~~~

## Task 2: Define control-plane models and PostgreSQL persistence

**Files:**

- Create: internal/control/model.go
- Create: internal/control/validation.go
- Create: internal/db/schema.sql
- Create: internal/db/postgres.go
- Create: internal/db/repository.go
- Create: internal/control/model_test.go
- Create: internal/db/repository_test.go
- Create: test/testdata/schema.sql

- [ ] **Step 1: Define the shared models**

The model package must define Network, Node, Membership, EndpointCandidate, Invite, RelayAssignment, NetworkSnapshot, and AuditEvent. NetworkSnapshot must contain a generation number, the local node address, all eligible peers, and the current Relay assignment.

~~~go
type Network struct {
    ID            string
    Name          string
    IPv4Pool      string
    OwnerID       string
    ConfigVersion int64
    CreatedAt     time.Time
}

type Node struct {
    ID            string
    DisplayName   string
    PublicKey     string
    Platform      string
    ClientVersion string
    LastSeen      time.Time
}

type Membership struct {
    NetworkID   string
    NodeID      string
    VirtualIPv4 net.IP
    Role        string
    Status      string
}

type EndpointCandidate struct {
    NodeID     string
    Address    net.IP
    Port       uint16
    Family     string
    Interface  string
    Priority   int
    ObservedAt time.Time
}
~~~

- [ ] **Step 2: Write the schema**

Create tables for networks, nodes, memberships, invites, endpoint candidates, Relay assignments, and audit events. Add unique constraints for node public keys, network membership pairs, and network virtual IPv4 addresses. Store invite tokens only as cryptographic hashes.

- [ ] **Step 3: Implement repository methods**

The repository interface must expose create/get network, add/remove node, add/remove membership, consume invite, replace endpoints, and build a versioned snapshot. Invite consumption, membership insertion, address allocation, and config-version increment must occur in one transaction.

- [ ] **Step 4: Add repository tests**

Cover duplicate public keys, duplicate virtual IPv4, invite expiration, concurrent single-use invite consumption, stale endpoint filtering, and snapshot version increments.

Run:

~~~text
go test ./internal/control ./internal/db -race -v
~~~

Expected: PASS; only one concurrent invite consumer succeeds.

- [ ] **Step 5: Commit persistence**

~~~text
git add internal/control internal/db test/testdata/schema.sql
git commit -m "feat: add control-plane models and persistence"
~~~

## Task 3: Implement authentication, invitations, enrollment, and address allocation

**Files:**

- Create: internal/auth/tokens.go
- Create: internal/auth/tokens_test.go
- Create: internal/enrollment/service.go
- Create: internal/enrollment/service_test.go
- Create: internal/address/pool.go
- Create: internal/address/pool_test.go
- Create: internal/control/http.go
- Create: internal/control/http_test.go
- Create: cmd/control-server/config.go

- [ ] **Step 1: Define the v0.1 authentication boundary**

Use a bootstrap administrator token supplied through the Control Server environment, short-lived bearer sessions over HTTPS for administrative calls, and single-use invite tokens for device enrollment. A node registration request contains the node public key and device metadata.

- [ ] **Step 2: Test token behavior**

Test constant-time comparison, expiration, single-use consumption, wrong-network rejection, revoked-network rejection, and malformed bearer headers.

Run:

~~~text
go test ./internal/auth -race -v
~~~

- [ ] **Step 3: Implement address allocation**

The allocator must parse a configured CIDR, skip reserved addresses, consult the database uniqueness constraint, return the next available address, and return a typed exhaustion error. The allocation remains stable until the network owner explicitly changes the pool.

- [ ] **Step 4: Implement the HTTP endpoints**

Implement:

~~~text
POST   /v1/networks
POST   /v1/networks/{id}/invites
POST   /v1/enrollments
GET    /v1/networks/{id}/snapshot
POST   /v1/nodes/{id}/heartbeat
DELETE /v1/nodes/{id}
WS     /v1/events
~~~

Return 401 for missing credentials, 403 for insufficient permissions, 404 for unknown resources, 409 for duplicate enrollment or consumed invites, and 422 for invalid network data. Every response includes a request ID.

- [ ] **Step 5: Test the complete enrollment flow**

Using net/http/httptest and an in-memory repository, test create network, create invite, enroll node A, enroll node B, fetch snapshot, and delete node B.

Run:

~~~text
go test ./internal/control ./internal/enrollment ./internal/address -race -v
~~~

- [ ] **Step 6: Commit enrollment**

~~~text
git add internal/auth internal/enrollment internal/address internal/control cmd/control-server
git commit -m "feat: add enrollment and virtual IPv4 allocation"
~~~

## Task 4: Build the Windows service boundary and protected identity store

**Files:**

- Create: internal/identity/store_windows.go
- Create: internal/identity/store_test.go
- Create: internal/ipc/protocol.go
- Create: internal/ipc/server_windows.go
- Create: internal/ipc/client_windows.go
- Create: internal/service/service_windows.go
- Create: internal/service/service_test.go
- Modify: cmd/vpn-service/main_windows.go
- Modify: cmd/vpnctl/main.go

- [x] **Step 1: Define the IPC protocol**

The first commands are status, join, leave, connect, and disconnect. Responses contain network ID, virtual IPv4, path state, last handshake, last error, and configuration generation.

~~~json
{"type":"status"}
{"type":"join","invite":"one-time-token","display_name":"device-a"}
{"type":"leave","network_id":"network-id"}
{"type":"connect","network_id":"network-id"}
{"type":"disconnect","network_id":"network-id"}
~~~

- [x] **Step 2: Implement local identity storage**

Generate the WireGuard key pair on first service start. Protect the private key with Windows DPAPI and restrict its file ACL to the service account and local administrators. A service restart must return the same public key.

- [x] **Step 3: Implement the Named Pipe boundary**

The service owns the pipe, validates the caller token, limits message size, parses one JSON request at a time, and never returns the private key.

- [x] **Step 4: Add service lifecycle tests**

Use fake adapter and fake Control Server clients to test identity creation, duplicate join rejection, leave cleanup, malformed JSON rejection, unauthorized pipe access, and restart recovery.

Run on Windows:

~~~text
go test ./internal/identity ./internal/ipc ./internal/service -race -v
~~~

- [x] **Step 5: Commit the service boundary**

~~~text
git add internal/identity internal/ipc internal/service cmd/vpn-service cmd/vpnctl
git commit -m "feat: add Windows service and IPC boundary"
~~~

## Task 5: Integrate WireGuardNT and Windows IP Helper routing

**Files:**

- Create: internal/wgnt/api.go
- Create: internal/wgnt/adapter_windows.go
- Create: internal/wgnt/adapter_stub.go
- Create: internal/wgnt/adapter_test.go
- Create: internal/netwin/iphelper_windows.go
- Create: internal/netwin/iphelper_stub.go
- Create: internal/netwin/route_reconciler.go
- Create: internal/netwin/route_reconciler_test.go
- Create: third_party/wireguardnt/README.md
- Create: packaging/windows/wireguardnt-manifest.json

- [x] **Step 1: Define the adapter abstraction**

The service depends on an interface rather than the DLL directly:

~~~go
type Adapter interface {
    Ensure(name string) (Handle, error)
    Configure(context.Context, Handle, PeerConfig) error
    SetUp(context.Context, Handle) error
    SetDown(context.Context, Handle) error
    Delete(context.Context, Handle) error
    Status(context.Context, Handle) (Status, error)
}
~~~

- [x] **Step 2: Bind the official WireGuardNT DLL**

Load wireguard.dll through the documented API, create a WireGuard adapter, configure interface and Peer keys, set endpoints and Allowed IPs, and query handshake and byte counters. Do not reimplement cryptography. Record the exact DLL source, license, architecture, and packaging hash in third_party/wireguardnt/README.md.

- [x] **Step 3: Implement IP Helper operations**

Use the adapter LUID to add/remove the virtual IPv4 address and overlay route. Keep an owned-route registry so cleanup cannot delete unrelated user routes. Never add a default route.

- [x] **Step 4: Test adapter and route reconciliation**

Cover idempotent adapter creation, Peer replacement by public key, /32 direct routes, Relay route replacement, deleted Peer cleanup, partial-apply rollback, and no-default-route behavior.

Run common tests on every platform and Windows integration tests on a Windows host:

~~~text
go test ./internal/wgnt ./internal/netwin -v
~~~

- [x] **Step 5: Commit the Windows data plane**

~~~text
git add internal/wgnt internal/netwin third_party/wireguardnt packaging/windows
git commit -m "feat: integrate WireGuardNT and Windows overlay routes"
~~~

## Task 6: Implement endpoint discovery, snapshots, and client reconciliation

**Files:**

- Create: internal/endpoint/candidates.go
- Create: internal/endpoint/candidates_windows.go
- Create: internal/endpoint/rendezvous.go
- Create: internal/endpoint/candidates_test.go
- Create: internal/control/client.go
- Create: internal/control/events.go
- Create: internal/reconcile/snapshot.go
- Create: internal/reconcile/snapshot_test.go
- Modify: internal/service/service_windows.go

- [ ] **Step 1: Filter Windows IPv6 candidates**

Enumerate unicast addresses and retain preferred, usable, non-link-local, non-loopback, non-VPN addresses that are not marked SkipAsSource. Preserve interface LUID and address lifetimes. Do not select the first IPv6 address returned by the operating system.

- [ ] **Step 2: Implement rendezvous reporting**

The client reports node ID, public key, candidate IPv6, WireGuard listen port, interface LUID, and client version. The server records the observed source address and timestamp. Stale candidates are excluded from snapshots.

- [ ] **Step 3: Implement the Control Server client**

Use HTTPS for request/response and WebSocket for configuration events. Reconnect with bounded exponential backoff. Preserve the last valid snapshot during temporary control-plane loss.

- [ ] **Step 4: Implement idempotent snapshot reconciliation**

Apply snapshots in this order:

~~~text
validate generation
→ ensure adapter
→ ensure virtual IPv4
→ ensure overlay route
→ configure direct Peers
→ configure Relay Peer
→ remove deleted Peers
→ publish status
~~~

If an apply step fails, keep the last known good configuration and return a typed error.

- [ ] **Step 5: Add reconciliation tests**

Test out-of-order snapshots, duplicate events, stale generations, endpoint replacement, IPv6 address changes, control reconnect, partial adapter failure, member deletion, and restart with a cached snapshot.

Run:

~~~text
go test ./internal/endpoint ./internal/control ./internal/reconcile -race -v
~~~

- [ ] **Step 6: Commit endpoint and reconciliation**

~~~text
git add internal/endpoint internal/control internal/reconcile internal/service
git commit -m "feat: add endpoint discovery and snapshot reconciliation"
~~~

## Task 7: Implement the trusted Relay and Direct/Relay selector

**Files:**

- Create: cmd/relay-agent/main_linux.go
- Create: internal/relay/config.go
- Create: internal/relay/config_test.go
- Create: internal/relay/wireguard_linux.go
- Create: internal/relay/routes_linux.go
- Create: internal/path/selector.go
- Create: internal/path/selector_test.go
- Create: deploy/relay/wg-mesh.conf.example
- Create: deploy/relay/nftables.conf
- Create: deploy/relay/sysctl.conf

- [ ] **Step 1: Define Relay membership configuration**

The Relay receives only network ID, node public key, node virtual IPv4, and allowed endpoint. It rejects unknown networks and nodes. Its management channel is authenticated separately from the client data channel.

- [ ] **Step 2: Configure the Linux WireGuard Hub**

Create one WireGuard interface per Relay instance and one Peer per registered node with a single virtual IPv4 /32. Enable forwarding only for the overlay interface. Add nftables rules that allow the WireGuard UDP port, established traffic, and forwarding between registered overlay /32 addresses while rejecting overlay-to-public forwarding.

- [ ] **Step 3: Implement path hysteresis**

Define the states Direct, Suspect, Relay, and Disconnected. A missed handshake enters Suspect; consecutive failures move to Relay; consecutive successful probes move back to Direct. The thresholds must be configurable and covered by a fake-clock test.

- [ ] **Step 4: Test the selector**

Verify no flap after one missed probe, Relay after repeated failures, recovery only after the success threshold, Disconnected when all paths fail, and rejection of deleted members.

Run:

~~~text
go test ./internal/path ./internal/relay -race -v
~~~

- [ ] **Step 5: Add Relay integration tests**

Use Linux network namespaces or containers for Node A, Node B, and Relay. Verify overlay ping and TCP through Relay, Direct-to-Relay recovery, and rejection of an unregistered overlay address.

- [ ] **Step 6: Commit Relay and failover**

~~~text
git add cmd/relay-agent internal/relay internal/path deploy/relay
git commit -m "feat: add trusted relay and path failover"
~~~

## Task 8: Add CLI, operator documentation, and Windows UI

**Files:**

- Create: cmd/vpnctl/commands.go
- Create: cmd/vpnctl/commands_test.go
- Create: cmd/vpnctl/output.go
- Create: docs/operator.md
- Create: ui/VpnUi/VpnUi.csproj
- Create: ui/VpnUi/App.xaml
- Create: ui/VpnUi/MainWindow.xaml
- Create: ui/VpnUi/MainWindow.xaml.cs
- Create: ui/VpnUi/ServiceClient.cs
- Create: ui/VpnUi/ServiceClientTests.cs
- Create: ui/README.md

- [ ] **Step 1: Implement CLI commands**

Support:

~~~text
vpnctl network create --name <name> --pool <cidr>
vpnctl invite create --network <id> --expires <duration>
vpnctl join --invite <token> --name <device>
vpnctl status
vpnctl connect --network <id>
vpnctl disconnect --network <id>
vpnctl leave --network <id>
~~~

The CLI talks to the Windows Service through the Named Pipe. Administrative network and invite creation use the Control Server API.

- [ ] **Step 2: Test command behavior**

Use fake IPC and Control Server clients. Verify argument validation, status output, non-zero service errors, and redaction of private data.

Run:

~~~text
go test ./cmd/vpnctl -v
~~~

- [ ] **Step 3: Add operator documentation**

Document server startup, network creation, invitation, Windows service installation, node join, Direct/Relay status, firewall expectations, Relay trust, no-default-route behavior, and cleanup.

- [ ] **Step 4: Implement the GUI over the same IPC**

Show network name, virtual IPv4, members, Direct/Relay/Disconnected state, last handshake age, last error, and Connect/Disconnect/Join/Leave actions. The GUI must not read the private key.

- [ ] **Step 5: Build and test the GUI**

~~~text
dotnet build ui/VpnUi/VpnUi.csproj
dotnet test ui/VpnUi/VpnUi.csproj
~~~

- [ ] **Step 6: Commit operator surfaces**

~~~text
git add cmd/vpnctl docs ui
git commit -m "feat: add VPN operator CLI and Windows UI"
~~~

## Task 9: Harden, package, and run acceptance tests

**Files:**

- Create: internal/security/redaction.go
- Create: internal/security/redaction_test.go
- Create: internal/diagnostics/status.go
- Create: internal/diagnostics/status_test.go
- Create: packaging/windows/installer.wxs
- Create: packaging/windows/README.md
- Create: test/integration/direct_ipv6.ps1
- Create: test/integration/relay_fallback.ps1
- Create: test/integration/revocation.ps1
- Create: test/integration/no_default_route.ps1
- Create: test/integration/endpoint_change.ps1
- Create: test/integration/README.md

- [ ] **Step 1: Add redaction and permission checks**

Logs may contain node IDs, virtual IPs, path states, timestamps, handshake age, byte counters, and typed error codes. Logs must not contain private keys, bearer tokens, invitation secrets, or VPN payloads. Test DPAPI storage, private-key file ACLs, Named Pipe ACLs, Relay forwarding restrictions, and no-default-route behavior.

- [ ] **Step 2: Add diagnostics**

Expose network ID, virtual IPv4, path state, last handshake, bytes sent/received, config version, and last error code through both CLI and GUI.

- [ ] **Step 3: Build the installable Windows package**

Include the service, CLI, UI, official WireGuardNT runtime dependencies, architecture metadata, hashes, and license notices. Do not commit opaque vendor binaries without the recorded source and license information.

- [ ] **Step 4: Run static checks**

~~~text
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
dotnet test ui/VpnUi/VpnUi.csproj
git diff --check
~~~

Expected: no formatting output, all tests pass, no vet diagnostics, and no whitespace errors.

- [ ] **Step 5: Run Direct IPv6 acceptance**

Use two Windows devices on different IPv6 networks. Enroll both, verify unique virtual IPv4 addresses, verify Direct status, run ping/TCP/UDP tests, and verify normal internet routing is unchanged.

- [ ] **Step 6: Run Relay fallback acceptance**

Block the direct UDP endpoint without stopping the Control Server. Verify Direct → Suspect → Relay, restored virtual IPv4 traffic, and rejection of an unregistered overlay address.

- [ ] **Step 7: Run endpoint-change and revocation acceptance**

Change the active IPv6 interface or address, sleep/wake the machine, remove one member, and verify endpoint rediscovery, Peer update, route cleanup, and refusal of new membership configuration.

- [ ] **Step 8: Record exact verification scope**

Separate unit tests, Windows integration tests, Linux Relay tests, two-node smoke tests, multi-node tests, and known limitations. Do not report a command as completed unless it was actually run.

- [ ] **Step 9: Commit test and packaging evidence**

~~~text
git add internal/security internal/diagnostics packaging/windows test/integration
git commit -m "test: add IPv6 mesh VPN acceptance and packaging"
~~~

## First implementation milestone

Tasks 1–3 form the first independently testable milestone. It must create a Control Server that can create a network, issue an invite, enroll two nodes, allocate two virtual IPv4 addresses, and return a versioned snapshot without Windows data-plane code.

After this milestone, Tasks 4–6 form the Windows Direct-path milestone. Task 7 adds Relay fallback. Task 8 adds operator surfaces. Task 9 is the release and acceptance gate.

## Plan self-review checklist

- [ ] Every v0.1 requirement in the approved design maps to at least one task.
- [ ] The plan contains no unfinished placeholder markers or undefined step.
- [ ] Model names, snapshot fields, path states, and test commands are consistent across tasks.
- [ ] No task adds subnet routing, L2 broadcast, mobile clients, exit nodes, or opaque Relay to v0.1.
- [ ] Each task ends with a focused validation command and an intentional commit.
- [ ] Implementation records are added under records/IPv6MeshVPN/ after each tested milestone.
