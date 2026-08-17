# IPv6Mesh macOS 工具包

这个 DMG 是原生 `darwin/arm64`（Apple Silicon）工具包，当前包含：

- `bin/vpnctl`：控制面和房间命令行客户端；
- `bin/control-server`：可在 macOS 上运行的控制面服务；
- `docs/operator.md`：运维说明；
- `version.txt` 和 `manifest.txt`：构建信息。

## 安装

把 DMG 拖到任意位置后，在终端中复制二进制：

```sh
mkdir -p "$HOME/.local/bin"
cp /Volumes/IPv6Mesh*/bin/vpnctl "$HOME/.local/bin/"
cp /Volumes/IPv6Mesh*/bin/control-server "$HOME/.local/bin/"
chmod 755 "$HOME/.local/bin/vpnctl" "$HOME/.local/bin/control-server"
```

如果 `$HOME/.local/bin` 尚未在 `PATH` 中，可临时执行：

```sh
export PATH="$HOME/.local/bin:$PATH"
```

## 能力边界

当前仓库的节点数据面仍然只有 Windows 实现：`vpn-service` 在 macOS 上是空入口，
WireGuard 适配器、节点身份保护、虚拟 IPv4 路由和本机 IPC 服务尚未移植到 macOS。
因此这个 DMG 是可运行的 macOS 控制面/CLI 工具包，不宣称 macOS 节点 VPN 已可用。

macOS 上可直接使用控制面命令，例如：

```sh
export IPV6MESH_CONTROL_URL='http://[2001:db8::1]:8080'
export IPV6MESH_ADMIN_TOKEN='<bootstrap-token>'
vpnctl room endpoint --host-ipv6 2001:db8::1
vpnctl room create --name friends --pool 10.42.0.0/24
```

也可以在本机启动内存模式控制面：

```sh
export CONTROL_BOOTSTRAP_TOKEN='local-development-token'
export CONTROL_SESSION_TTL='24h'
export CONTROL_INVITE_TTL='1h'
CONTROL_ROOM_MODE=true \
CONTROL_REPOSITORY_MODE=memory \
control-server
```

另开一个终端后，可用同一个 bootstrap token 调用本机控制面：

```sh
export IPV6MESH_CONTROL_URL='http://127.0.0.1:8080'
export IPV6MESH_ADMIN_TOKEN='local-development-token'
vpnctl room create --name friends --pool 10.42.0.0/24
```

`vpnctl status`、`join`、`leave`、`connect` 和 `disconnect` 依赖本机节点服务；在
当前 macOS 工具包中会因节点服务未实现而不可用。

DMG 未做 Apple Developer ID 签名或 notarization。正式对外发布前仍需在 macOS
发布机上完成签名、公证和真实网络验收。
