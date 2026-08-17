#!/bin/bash

set -euo pipefail

SCRIPT_DIRECTORY="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
REPOSITORY_ROOT="$(CDPATH= cd -- "$SCRIPT_DIRECTORY/../.." && pwd)"

native_arch="arm64"
case "$(uname -m)" in
    arm64)
        native_arch="arm64"
        ;;
    x86_64)
        native_arch="amd64"
        ;;
esac

version="${IPV6MESH_VERSION:-0.1.0-dev}"
architecture="${IPV6MESH_ARCH:-$native_arch}"
output_directory="${IPV6MESH_OUTPUT_DIRECTORY:-$SCRIPT_DIRECTORY/dist}"
go_command="${IPV6MESH_GO_COMMAND:-go}"

usage() {
    cat <<'EOF'
Usage: packaging/macos/build-dmg.sh [options]

Build a native macOS IPv6Mesh developer-tools DMG.

Options:
  --version VERSION  Version embedded in the package name and manifest.
  --arch ARCH        darwin target architecture: arm64 or amd64.
  --output DIRECTORY Directory for the DMG and SHA-256 file.
  --go COMMAND       Go executable or absolute path.
  -h, --help         Show this help.

Environment equivalents:
  IPV6MESH_VERSION, IPV6MESH_ARCH, IPV6MESH_OUTPUT_DIRECTORY,
  IPV6MESH_GO_COMMAND
EOF
}

while (($# > 0)); do
    case "$1" in
        --version)
            [[ $# -ge 2 ]] || { echo "--version requires a value" >&2; exit 2; }
            version="$2"
            shift 2
            ;;
        --arch)
            [[ $# -ge 2 ]] || { echo "--arch requires a value" >&2; exit 2; }
            architecture="$2"
            shift 2
            ;;
        --output)
            [[ $# -ge 2 ]] || { echo "--output requires a value" >&2; exit 2; }
            output_directory="$2"
            shift 2
            ;;
        --go)
            [[ $# -ge 2 ]] || { echo "--go requires a value" >&2; exit 2; }
            go_command="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "unknown option: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "the macOS DMG builder must run on macOS" >&2
    exit 1
fi

if [[ -z "$version" || "$version" == */* ]]; then
    echo "version must be non-empty and must not contain '/'" >&2
    exit 2
fi

case "$architecture" in
    arm64|amd64)
        ;;
    *)
        echo "unsupported macOS architecture: $architecture (use arm64 or amd64)" >&2
        exit 2
        ;;
esac

if ! command -v "$go_command" >/dev/null 2>&1; then
    echo "Go command not found: $go_command" >&2
    exit 1
fi
if ! command -v hdiutil >/dev/null 2>&1; then
    echo "hdiutil is required to create a DMG" >&2
    exit 1
fi
if ! command -v shasum >/dev/null 2>&1; then
    echo "shasum is required to write the DMG checksum" >&2
    exit 1
fi

mkdir -p "$output_directory"
output_directory="$(CDPATH= cd -- "$output_directory" && pwd)"
staging_directory="$(mktemp -d "$output_directory/.ipv6mesh-macos.XXXXXX")"

cleanup() {
    rm -rf "$staging_directory"
}
trap cleanup EXIT

mkdir -p "$staging_directory/bin" "$staging_directory/docs"

build_binary() {
    local name="$1"
    local package_path="$2"
    env CGO_ENABLED=0 GOOS=darwin GOARCH="$architecture" "$go_command" build \
        -trimpath \
        -ldflags "-s -w" \
        -o "$staging_directory/bin/$name" \
        "$REPOSITORY_ROOT/$package_path"
    chmod 755 "$staging_directory/bin/$name"
}

build_binary "vpnctl" "cmd/vpnctl"
build_binary "control-server" "cmd/control-server"

cp "$SCRIPT_DIRECTORY/README.md" "$staging_directory/README.md"
cp "$REPOSITORY_ROOT/docs/operator.md" "$staging_directory/docs/operator.md"
printf '%s\n' "$version" > "$staging_directory/version.txt"
cat > "$staging_directory/manifest.txt" <<EOF
IPv6Mesh macOS developer tools
Version: $version
Target: darwin/$architecture

Included binaries:
- bin/vpnctl
- bin/control-server
EOF

dmg_path="$output_directory/ipv6mesh-${version}-macos-${architecture}.dmg"
checksum_path="$dmg_path.sha256"
rm -f "$dmg_path" "$checksum_path"

hdiutil create \
    -volname "IPv6Mesh ${version} macOS ${architecture}" \
    -srcfolder "$staging_directory" \
    -ov \
    -format UDZO \
    "$dmg_path" >/dev/null
hdiutil imageinfo "$dmg_path" >/dev/null

hash="$(shasum -a 256 "$dmg_path" | awk '{print $1}')"
printf '%s  %s\n' "$hash" "$(basename "$dmg_path")" > "$checksum_path"

echo "DMG written to $dmg_path"
echo "SHA-256 written to $checksum_path"
