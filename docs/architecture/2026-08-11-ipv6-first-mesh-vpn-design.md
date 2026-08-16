# IPv6-first Mesh VPN Architecture

This source repository implements the design approved in the [Memory repository design record](https://github.com/Eser-Tired/Memory/blob/agent/ipv6-first-mesh-vpn-design/docs/superpowers/specs/2026-08-11-ipv6-first-mesh-vpn-design.md).

## v0.1

- Windows-first client service.
- WireGuardNT data plane.
- Randomly selected, then stable virtual IPv4 per enrolled node.
- IPv6 Direct path preferred.
- Trusted WireGuard Relay fallback.
- Node-to-node access only.
- No default-route takeover.

## Explicit non-goals

Subnet routing, exit nodes, LAN broadcast or multicast, TAP/L2 bridging, mobile clients, opaque Relay encryption, and custom cryptography are not part of v0.1.

See the [implementation plan](../superpowers/plans/2026-08-11-ipv6-first-mesh-vpn-implementation.md) for task order, file boundaries, test commands, and acceptance criteria.
