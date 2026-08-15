# IPv6Mesh 操作说明（v0.1）

IPv6Mesh 是 IPv6-first 的远程组网 VPN：控制面只负责成员、虚拟 IPv4 地址、endpoint 和版本化快照；节点间数据面使用 WireGuard，默认只安装虚拟 IPv4 `/32` 路由。

## 控制面环境变量

管理员 CLI 使用：

```powershell
$env:IPV6MESH_CONTROL_URL = 'https://control.example.invalid'
$env:IPV6MESH_ADMIN_TOKEN = '<bootstrap-or-admin-token>'
```

创建网络和一次性邀请：

```text
vpnctl network create --name friends --pool 10.42.0.0/24
vpnctl invite create --network <network-id> --expires 1h
```

邀请 token 只在明确的 `invite create` 成功输出中返回。不要把它写入日志、Issue 或普通聊天记录。

## Windows 节点

```text
vpnctl join --invite <one-time-token> --name <device-name>
vpnctl status
vpnctl connect --network <network-id>
vpnctl disconnect --network <network-id>
vpnctl leave --network <network-id>
```

节点私钥由 Windows 服务生成并用 DPAPI 保护，CLI 和 Named Pipe 响应不会返回私钥。WireGuardNT 官方 DLL 需要在发布包中按 `third_party/wireguardnt/README.md` 的来源、版本、架构和许可证要求单独携带；当前源码仓库不包含 DLL。

IPv6 endpoint 会优先用于直连；IPv4 endpoint 仅作为后备。link-local、loopback、SkipAsSource、虚拟 VPN 接口和过期地址不会被报告。连接只为虚拟 IPv4 节点地址创建 `/32` host route，不会把默认路由改到 VPN。

## Relay

Relay 配置必须明确列出本 Relay 的虚拟 IPv4 和每个已注册节点的 IPv4 `/32`。参考文件在 `deploy/relay/`；Linux agent 使用：

```text
relay-agent -config /etc/ipv6mesh/relay.json
```

配置中的 `private_key` 只允许是本机权限受控的配置来源；agent 调用 `wg` 时通过临时 `0600` 文件传递私钥，并以参数数组调用 `ip`/`wg`，不经过 shell。Relay 的 nftables 规则只允许登记的 overlay IPv4 互通，禁止 overlay 到公网转发。

Direct/Relay 状态采用滞回：一次丢失不会立即切 Relay，直连恢复需要连续成功观察；成员被撤销或两条路径均不可用时进入 Disconnected。

## 当前验收边界

源码单元测试、Windows 条件编译、Linux Relay 构建和纯逻辑路径测试已覆盖。带官方 DLL 的管理员权限 Windows 双机隧道、Linux root/network namespace Relay 测试、真实 PostgreSQL 和完整安装包验收仍需在目标环境执行。
