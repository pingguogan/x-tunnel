<div align="center">

# 🚀 x-tunnel

**高性能 WebSocket 隧道代理 | High-Performance WebSocket Tunnel Proxy**

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue?style=flat-square)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey?style=flat-square)]()
[![Release](https://img.shields.io/github/v/release/pingguogan/x-tunnel?style=flat-square&logo=github)](https://github.com/pingguogan/x-tunnel/releases)

*基于 WebSocket 的安全隧道解决方案，支持 TCP/UDP 转发、ECH 加密、Cloudflare Argo Tunnel 一键部署*

[English](#features) • [快速开始](#-快速开始) • [文档](#-文档) • [特性](#-特性)

</div>

---

## ✨ 特性

| 特性 | 说明 |
|:---|:---|
| 🔌 **WebSocket 多路复用** | 基于 smux 协议，单连接承载多路数据流 |
| 🛡️ **ECH 加密** | 支持 Encrypted Client Hello，流量伪装抵御审查 |
| ☁️ **Cloudflare 集成** | Argo Tunnel 一键部署，Named / Quick 双模式 |
| 🌐 **多协议代理** | SOCKS5 / HTTP CONNECT / TCP 转发 |
| ⚡ **智能选路** | RTT 探测、优选 IP、IPv4/IPv6 策略 |
| 🖥️ **GUI 客户端** | Web UI + 系统托盘，多配置快速切换 |
| 📦 **一键部署** | `bash <(curl -fsSL ...)` 服务端自动安装 |
| 🔄 **自动重连** | 断线自动恢复，连接池动态管理 |

## 📐 架构

```
┌─────────────┐    WebSocket (wss://)    ┌─────────────┐     TCP/UDP      ┌──────────┐
│   客户端     │ ◄──────────────────────► │   服务端     │ ◄──────────────► │  目标服务  │
│  SOCKS5/HTTP │    smux 多路复用         │   Argo/直连  │                  │          │
└─────────────┘    ECH 加密              └─────────────┘                  └──────────┘
      │                                          │
      ▼                                          ▼
 ┌─────────┐                              ┌─────────────┐
 │ GUI 客户端│                              │ Cloudflare  │
 │ Web UI   │                              │   CDN/ECH   │
 └─────────┘                              └─────────────┘
```

## 🚀 快速开始

### 一键部署服务端

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/pingguogan/x-tunnel/master/install.sh)
```

脚本自动完成：
- ✅ 检测网络环境（NAT / 公网 IP）
- ✅ 安装 cloudflared（可选）
- ✅ 配置 Argo Tunnel（Named / Quick 模式）
- ✅ 下载并安装 x-tunnel 服务端
- ✅ 生成 systemd 服务并启动

### 客户端连接

**GUI 客户端（推荐）：**

下载 `x-tunnel-gui.exe`，双击运行 → 自动最小化到系统托盘 → 浏览器打开管理界面

**命令行客户端：**

```bash
./x-tunnel-client -f wss://your-server.com/tunnel -token your-token -l socks5://0.0.0.0:1080
```

## 🛠️ 构建

```bash
# 环境要求：Go 1.23+
make all              # 构建所有平台
make server           # 服务端
make client           # CLI 客户端
make gui              # GUI 客户端
make windows          # Windows (GUI 无 cmd 窗口)
make linux-arm64      # Linux ARM64
```

## ⚙️ 配置示例

<details>
<summary>📄 服务端配置 (server.json)</summary>

```json
{
  "listen": "wss://0.0.0.0:8443/tunnel",
  "token": "your-secret-token",
  "cidr": "0.0.0.0/0,::/0",
  "cert": "/path/to/cert.pem",
  "key": "/path/to/key.pem",
  "global": {
    "dial_timeout_sec": 3,
    "ws_handshake_timeout_sec": 5,
    "reconnect_delay_sec": 1,
    "rtt_probe_timeout_sec": 2,
    "read_buf": 65536
  }
}
```
</details>

<details>
<summary>📄 客户端配置 (client.json)</summary>

```json
{
  "server": "wss://your-server.com/tunnel",
  "token": "your-secret-token",
  "listen": [
    "socks5://0.0.0.0:1080",
    "http://0.0.0.0:8080"
  ],
  "connections": 3,
  "ech_domain": "cloudflare-ech.com",
  "ech_config_url": "https://api.example.com/ech?domain=cloudflare-ech.com",
  "dns_server": "https://doh.pub/dns-query",
  "udp_block_ports": "443"
}
```
</details>

## 📚 文档

| 文档 | 说明 |
|:---|:---|
| [DEPLOY.md](DEPLOY.md) | 服务端部署指南、systemd 配置、Cloudflare CDN 配置 |
| [USAGE.md](USAGE.md) | 完整参数说明、使用示例、ECH 配置、常见问题 |

## 🌟 GUI 客户端特性

- 📋 **多配置管理** — 保存多个服务器配置，一键切换
- 🔗 **快速导入** — 粘贴部署脚本输出，自动填充配置
- ⚡ **快速连接** — 配置列表直接点击连接
- 🛡️ **ECH 支持** — 自动获取并缓存 ECH 配置
- 📊 **实时日志** — 连接状态和日志实时显示
- 🔔 **系统托盘** — Windows 下最小化到托盘，无 cmd 窗口

## 📦 下载

前往 [Releases](https://github.com/pingguogan/x-tunnel/releases) 页面下载最新版本。

| 平台 | 文件 |
|:---|:---|
| Windows x64 | `x-tunnel-gui.exe` / `x-tunnel-server.exe` / `x-tunnel-client.exe` |
| Linux x64 | `x-tunnel-server` / `x-tunnel-client` |
| Linux ARM64 | `x-tunnel-server-linux-arm64` |

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'feat: add amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 创建 Pull Request

## 📄 License

本项目基于 MIT License 开源。详见 [LICENSE](LICENSE) 文件。

---

<div align="center">

**如果觉得有用，请给个 ⭐ Star 支持一下！**

</div>
