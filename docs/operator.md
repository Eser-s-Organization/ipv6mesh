# IPv6Mesh 操作说明（v0.1）

IPv6Mesh 的控制面管理节点、房间、虚拟 IPv4、endpoint 和版本化快照；WireGuard
负责节点之间的数据面。Windows 节点服务只为虚拟 IPv4 安装 host route，不改变默认
路由。

## 房间模式

房主控制面必须显式启用：

~~~text
CONTROL_ROOM_MODE=true
CONTROL_REPOSITORY_MODE=memory
~~~

图形安装器会在房主流程中生成内部 bootstrap credential，并通过进程环境变量传给
控制面和 room create 调用；它不会把该值放入 UI、日志、导出文件或响应。手动运维
时只使用本地安全存储的占位 credential，不要把真实值写入文档。

### 房主流程

房主需要一个可从成员访问的首选、非 SkipAsSource 的 2000::/3 全局 IPv6。房主和
成员都必须能访问：

- 控制面 TCP 8080；
- WireGuard UDP 51820；
- 对端的可达 IPv6。

CLI 入口：

~~~text
vpnctl room endpoint --host-ipv6 <ipv6>
vpnctl room create --name <name> --pool 10.42.0.0/24
vpnctl room join --host-ipv6 <ipv6> --name <device>
vpnctl connect --network <network-id>
~~~

正常顺序是先启动 room-mode control-server，再执行 endpoint 和 room create；房主
随后执行 room join 和 connect。成员只需要用同一个房主 IPv6 执行 endpoint、room
join 和 connect。room join 不要求输入 room ID、管理员 credential 或 invitation。

room endpoint 会把裸 IPv6 规范化为固定的
http://[<ipv6>]:8080。IPv4、ULA、link-local、loopback、multicast、未指定地址、
带 zone 的地址和显式端口输入都会被拒绝。

## 房间边界

房间模式是面向一次协作会话的轻量流程，而不是持久化多租户控制面：

- 每个控制面进程最多维护一个活动房间；
- 未创建房间或 room mode 未开启时，公开 join 返回稳定的 room_not_ready 或
  room_mode_disabled 错误；
- 公开 join 按来源 IPv4 和全局窗口限流，并限制请求体大小；
- 每次 join 内部创建短期 invitation，复用既有 enrollment 事务；invitation 不
  通过 room API 返回；
- enrollment 失败时内部 invitation 会撤销，避免留下可重放凭证；
- memory repository 只存在于当前 control-server 进程；停止或重启进程会丢失房间、
  成员、session 和虚拟地址分配；
- 关闭房主 UI 会停止本窗口启动的控制面和节点服务；再次打开会创建新房间；
- 知道房主 IPv6 即获得房间开放期间的加入能力，房主应把该地址当作访问边界。

不要在日志、Issue、截图或聊天中记录 bootstrap credential、session、私钥或任何
invitation 正文。

## 旧版兼容路径

需要持久化网络、显式成员审批或独立运维控制面时，继续使用 PostgreSQL repository
和 legacy invite workflow：

~~~powershell
$env:IPV6MESH_CONTROL_URL = 'https://control.example.invalid'
$env:IPV6MESH_ADMIN_TOKEN = '<bootstrap-token>'
~~~

~~~text
vpnctl network create --name friends --pool 10.42.0.0/24
vpnctl invite create --network <network-id> --expires 1h
vpnctl join --invite <one-time-invite> --name <device>
vpnctl status
vpnctl connect --network <network-id>
vpnctl disconnect --network <network-id>
vpnctl leave --network <network-id>
~~~

room mode 关闭时，这些 legacy endpoint 和 invitation 行为保持不变。两条路径共享
严格的控制面响应解码、enrollment、snapshot 和节点服务边界。

## Windows 节点

节点私钥由 Windows 服务生成并使用受保护的本地存储保存；CLI 和 Named Pipe 不会
返回私钥。WireGuardNT 官方 DLL 不在源码仓库中，发布包必须从官方 SDK 单独提供
正确架构的 DLL、许可证和 provenance。

IPv6 endpoint 优先用于 Direct；IPv4 endpoint 和受信任 Relay 作为后备。节点连接
只创建虚拟 IPv4 的 /32 host route，不会把默认路由导入 VPN。

## 验收边界

源码测试、room lifecycle integration test、Windows 条件编译、CLI/IPC 协议和脚本
解析已覆盖。真实双机公网 IPv6、Windows 管理员权限、TCP/UDP 防火墙、WireGuard
隧道、官方 DLL 和完整安装器仍需在目标环境执行；自动化结果不替代实机验收。
