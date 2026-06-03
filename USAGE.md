# x-tunnel 使用文档

## 目录

- [简介](#简介)
- [编译构建](#编译构建)
- [服务端](#服务端)
- [客户端 - 命令行](#客户端---命令行)
- [客户端 - GUI](#客户端---gui)
- [Cloudflare CDN + ECH 优选IP 配置](#cloudflare-cdn--ech-优选ip-配置)
- [协议说明](#协议说明)
- [常见问题](#常见问题)

---

## 简介

x-tunnel 是一个基于 WebSocket 的 TCP/UDP 隧道代理，支持：

- WebSocket 多路复用（smux）
- TCP/UDP 流量转发
- SOCKS5 / HTTP CONNECT 代理
- ECH（加密客户端握手）流量伪装
- ECH 配置自动获取与缓存
- DoH（DNS over HTTPS）ECH 公钥查询
- 优选 IP 连接
- IPv4/IPv6 策略控制
- 多通道连接池（RTT 自动选路）

---

## 编译构建

### 环境要求

- Go 1.26+

### 构建命令

```bash
make all          # 构建所有二进制
make server       # 仅构建服务端
make client       # 仅构建 CLI 客户端
make gui          # 仅构建 GUI 客户端
make windows      # 交叉编译 Windows
make linux-arm64  # 交叉编译 Linux ARM64
make clean        # 清理构建产物
```

---

## 服务端

### 启动参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-config` | 配置文件路径 (JSON) | - |
| `-l` | WebSocket 监听地址 | - |
| `-token` | 身份验证令牌 | 空 |
| `-cidr` | 允许的来源 IP 范围 | `0.0.0.0/0,::/0` |
| `-cert` | TLS 证书文件 | 自动生成 |
| `-key` | TLS 密钥文件 | 自动生成 |
| `-f` | SOCKS5 前置代理 | 空 |

### 使用示例

```bash
# WSS 模式
./x-tunnel-server -l wss://0.0.0.0:8443/tunnel -token mypassword -cert cert.pem -key key.pem

# 使用配置文件
./x-tunnel-server -config server.json

# 带前置代理
./x-tunnel-server -l wss://0.0.0.0:443 -token mypassword -f socks5://127.0.0.1:1080
```

---

## 客户端 - 命令行

### 启动参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-config` | 配置文件路径 (JSON) | - |
| `-f` | WebSocket 服务器地址 | - |
| `-l` | 本地监听地址 | `socks5://0.0.0.0:1080` |
| `-token` | 身份验证令牌 | 空 |
| `-n` | 每个IP的连接数 | 3 |
| `-insecure` | 忽略证书校验 | false |
| `-fallback` | 禁用 ECH | false |
| `-ech` | ECH 域名 | `cloudflare-ech.com` |
| `-dns` | DoH DNS 服务器 | `https://doh.pub/dns-query` |
| `-ech-config` | ECH 配置 (base64) | 空 |
| `-ech-config-url` | ECH 配置 URL | 空 |
| `-ip` | 指定连接 IP | 空 |
| `-block` | UDP 拦截端口 | `443` |
| `-ips` | IP 策略 | 空 |

### 使用示例

```bash
# 基础 SOCKS5
./x-tunnel-client -f wss://your-server.com -token mypassword

# 多协议
./x-tunnel-client -f wss://your-server.com -token mypassword \
  -l "socks5://0.0.0.0:1080,http://0.0.0.0:8080"

# Cloudflare CDN + ECH + 优选IP
./x-tunnel-client \
  -f wss://your-domain.com/tunnel \
  -token mypassword \
  -ech your-domain.com \
  -dns https://1.1.1.1/dns-query \
  -ip 104.16.132.229,104.16.133.229

# 使用 ECH 配置 URL
./x-tunnel-client -f wss://your-server.com -token mypassword \
  -ech-config-url "https://api.example.com/api/ech/raw?domain=cloudflare-ech.com"

# 使用配置文件
./x-tunnel-client -config client.json
```

---

## 客户端 - GUI

### 启动

**Windows:** 双击 `x-tunnel-gui.exe`（无 cmd 窗口，最小化到系统托盘）

**Linux:** `./x-tunnel-gui`

启动后自动打开浏览器，通过 Web 控制面板管理。

### 功能

- **多配置管理** — 添加/编辑/删除多个服务端配置
- **ECH 自动管理** — 从 URL 自动获取并缓存，无需手动填写
- **本地监听** — SOCKS5 / HTTP / TCP 转发
- **优选 IP** — 指定 Cloudflare CDN 节点 IP
- **系统托盘** — 红/黄/绿状态图标

### ECH 配置自动管理

1. 在高级选项中填写 **ECH 配置 URL**
2. 启动时自动从 URL 获取最新 ECH 配置
3. 获取成功后自动缓存到配置文件
4. 后续启动优先使用缓存（有效则跳过网络请求）
5. 连接被服务器拒绝时自动刷新并更新缓存

### 配置文件

`x-tunnel-config.json`（与 exe 同目录）：

```json
{
  "profiles": [
    {
      "name": "我的服务器",
      "server": "wss://your-domain.com/tunnel",
      "token": "your-token",
      "listen": ["socks5://0.0.0.0:1080"],
      "connections": 3,
      "ech_domain": "cloudflare-ech.com",
      "dns_server": "https://doh.pub/dns-query",
      "ech_config_url": "https://api.example.com/api/ech/raw?domain=cloudflare-ech.com",
      "target_ips": ["104.18.39.209", "172.67.71.102"],
      "udp_block_ports": "443"
    }
  ],
  "active_profile": "我的服务器"
}
```

---

## Cloudflare CDN + ECH 优选IP 配置

### 原理

```
客户端 → [ECH加密SNI] → 优选IP(CDN节点) → Cloudflare CDN → 源站服务器
```

### 服务端配置

1. 域名添加 Cloudflare CDN（橙色云朵开启）
2. SSL/TLS 模式设为 **完全(严格)**
3. 开启 WebSocket（网络 → WebSocket）
4. 申请 Cloudflare Origin Certificate
5. 启动服务端：
```bash
./x-tunnel-server -l wss://0.0.0.0:8443/tunnel -token your-token -cert cf.pem -key cf-key.pem
```

### 客户端配置

在 GUI 中配置 ECH 域名、ECH 配置 URL、优选 IP 即可。

---

## 协议说明

### 数据流

```
本地应用 → SOCKS5/HTTP/TCP 监听器 → smux 流 → WebSocket → 服务端 → 目标地址
```

### 流类型

| 类型 | ID | 说明 |
|------|------|------|
| TCP | 1 | TCP 流量转发 |
| UDP | 2 | UDP 流量转发 |
| Ping | 3 | RTT 探测 |

### 认证方式

使用 WebSocket Subprotocol 字段传递 Token。

---

## 常见问题

### Q: 连接被拒绝

检查服务端 CIDR 设置是否允许客户端 IP。

### Q: Token 认证失败

确保客户端和服务端使用相同的 Token。

### Q: ECH 连接失败

1. 确认域名已开启 Cloudflare CDN 且支持 ECH
2. 配置 ECH 配置 URL，让客户端自动获取
3. 尝试 `-fallback` 禁用 ECH
4. 更换 DoH 服务器

### Q: 断线重连

客户端自动重连，可通过配置文件调整重连间隔：
```json
{ "global": { "reconnect_delay_sec": 3 } }
```

### Q: 性能优化

1. 增加连接数：`-n 5`
2. 调整缓冲区：`{ "global": { "read_buf": 131072 } }`
3. 使用优选 IP 降低延迟
