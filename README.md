# Net Conductor

一台公网服务器，连接 N 台 Windows 设备。提供网页管理端和 Windows 桌面客户端。

**v0.1.0 测试版。** 首次实机验证采用已有 WireGuard 私网接入；全新机器安装路径尚未完成独立端到端验收。

## 已实现

- 单次、10 分钟有效的入网码；生成新 WireGuard 配置或接入已有隧道。
- 网页查看节点心跳、虚拟 IP、隧道握手、收发流量和代理健康状态。
- 按来源设备配置多个 HTTP/SOCKS5 出口，按顺序尝试；全部失败时可阻止或直连。
- Windows 本地 HTTP 代理 `127.0.0.1:17891`；桌面端可设置并还原当前用户的系统代理。
- Windows 共享代理由后台服务直接转发，仅接受服务器私网地址；支持与既有 portproxy 并存。
- TCP、UDP、HTTP 端口映射，HTTP 支持域名匹配。
- 网页 SSH 交互终端，使用专用密钥与已核对的主机指纹。
- Windows 桌面窗口、托盘和自启动后台服务；连接诊断、配置备份、隧道重启修复。

界面参考 Agent Conductor 的深蓝侧栏、浅色卡片和状态信息布局，代码独立实现。

## 结构

```text
Windows desktop ─ local authenticated API ─ Windows service
                                               │ HTTPS heartbeat/config
Public server: Web/API + WireGuard + authenticated HTTP proxy + port forwarding + SSH
                                               │ encrypted private network
                                      Windows devices
```

数据通道使用 WireGuard。代理策略和端口转发由 Go 服务实现，测试版不依赖另装 Mihomo/Caddy；现有部署中的这两个服务可以继续使用。

## 安装

预编译测试包见 [GitHub Releases](https://github.com/eliotnash/net-conductor/releases)。下载 bundle ZIP，校验 SHA256 后完整解压，保留 `dist/` 与 `deploy/` 的相对位置。已有 Windows 设备接入流程见 [接入说明](docs/join-windows.md)。

### Ubuntu 服务端

准备 Linux 二进制和 `deploy/`，在 Ubuntu 24.04 执行：

```bash
sudo env NC_PUBLIC_IP=你的公网IPv4 bash deploy/install-server.sh
```

默认私网 `10.77.0.0/24`、服务端 `10.77.0.1`。安全组放行 UDP 51820、TCP 18443，以及实际发布的端口。已有 wg0 不会被安装脚本覆盖，但须自行核对其网段和转发规则。

服务配置在 `/var/lib/net-conductor/config.json`，管理密码为其中的 `adminToken`。首次生成自签名证书；客户端须导入通过可信渠道获取的 `server.crt`，浏览器也须信任该证书或换成正式证书。不要关闭客户端的 TLS 验证。

### Windows 客户端

以管理员 PowerShell 执行：

```powershell
powershell -ExecutionPolicy Bypass -File deploy/Install-Desktop.ps1
```

安装器检查 WireGuard；未安装时下载官方 AMD64 MSI，验证 Authenticode 签名后安装。桌面程序会创建快捷方式。后台服务名称 `NetConductorAgent`，数据目录 `%ProgramData%\NetConductor`。

网页生成入网码，然后在桌面端填写服务器 HTTPS 地址、入网码、设备名和服务器证书。接入现有隧道时，网页生成入网码前填写该设备的虚拟 IP，客户端选择“接入已有隧道”并填写真实隧道名称。服务端会校验既有 IP 与公钥是否对应。

## 使用

1. **共享出口**：在客户端选择“共享本机代理”，填写真实本机代理端口和未占用的私网共享端口。在网页设备设置填写对应协议及共享端口。
2. **使用出口**：网页“代理策略”给来源设备安排出口顺序；客户端启用“系统代理”，或为特定应用手动设置 `http://127.0.0.1:17891`。系统服务及容器需另行配置。
3. **发布应用**：添加公网端口、目标设备和后端端口。后端须监听私网 IP，Windows 防火墙须允许服务器访问。HTTP 域名需正确解析；HTTPS 可通过现有 Caddy 终止 TLS 并转到映射端口，本版不会自动改写既有 Caddy。
4. **网页 SSH**：目标设备启用 OpenSSH；将服务端 `/var/lib/net-conductor/ssh_ed25519.pub` 授权给目标账号；在设备设置填写用户名、端口和从目标机核对的 SSH 主机指纹。优先使用 ED25519 主机密钥。
5. **修复**：先运行诊断；一键修复会保存客户端配置备份并重启隧道，短暂中断私网连接。它不会盲目重置系统防火墙或原有代理。

全流量 TUN 接管、自动局域网选路、多公网容灾、细粒度多用户 ACL、设备撤销界面和自动证书轮换尚未实现。节点间是否能访问目标服务仍受系统防火墙及服务监听设置约束。

## 开发与测试

需要 Go 1.26、Node.js 22.12+。锁文件固定依赖版本。

```powershell
powershell -ExecutionPolicy Bypass -File deploy/build.ps1
```

单独执行 `go test ./...`、`go vet ./...`。测试覆盖认证、跨站请求拒绝、敏感字段隐藏、代理故障切换、TCP/UDP 转发、状态保存和本机 API 防护。

目录：`cmd/` 入口，`internal/server/` 控制服务，`internal/agent/` Windows 服务，`internal/core/` 公共数据与代理，`web/static/` 页面，`desktop/` Electron 窗口，`deploy/` 安装脚本。

## 安全与维护

- 管理页面仅限可信管理员；网页 SSH 具有对应操作系统账号权限。
- 设备私钥保留本机；配置文件包含凭据，不要提交或放进发布包。
- 服务器共享代理只监听私网，按设备凭据认证；Windows 本地管理 API 只监听回环并校验本机令牌。
- 配置写入使用临时文件和替换；操作日志不记录终端内容、密码或私钥。
- 本版本没有经过独立安全审计，先在受控环境验证。
- 卸载后台服务使用 `deploy/Uninstall-Agent.ps1`。卸载前在桌面端还原系统代理；脚本保留隧道和配置备份，便于人工核对。

MIT 许可证适用于本项目原创代码，第三方组件见 `THIRD_PARTY_NOTICES.md`。
