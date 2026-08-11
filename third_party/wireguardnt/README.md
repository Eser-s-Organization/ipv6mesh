# WireGuardNT runtime provenance

IPv6Mesh uses the official WireGuardNT embeddable DLL ABI. The Go adapter in
`internal/wgnt` dynamically resolves the documented functions from
`wireguard.dll`; it does not copy, modify, or reimplement WireGuard
cryptography.

- Upstream source: <https://git.zx2c4.com/wireguard-nt/>
- ABI header: <https://git.zx2c4.com/wireguard-nt/plain/api/wireguard.h>
- Official runtime download directory: <https://download.wireguard.com/wireguard-nt/>
- Target release family: WireGuardNT 1.1, with the published `wireguard.h` ABI.
- Supported packaging architectures: Windows AMD64 and ARM64; the current CI
  gate validates AMD64.
- Header/source license: `GPL-2.0 OR MIT` as declared by the upstream header.
- Prebuilt DLL licensing: use the license notice shipped with the exact
  upstream runtime archive. It is separate from the kernel-driver source
  license and must be included in the final installer notices.

No vendor DLL is committed to this repository. The release process must fetch
the official architecture-specific archive, verify its signature/hash, record
the exact archive and SHA-256 in
`packaging/windows/wireguardnt-manifest.json`, and place `wireguard.dll`
side-by-side with the service executable. Until that step is completed, the
Windows adapter remains buildable but cannot create a live WireGuardNT adapter.
