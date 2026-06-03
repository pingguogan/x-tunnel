# x-tunnel 部署文档

## 项目概述

x-tunnel 是一个基于 WebSocket + ECH (Encrypted Client Hello) 的加密隧道代理工具。
包含服务端、CLI 客户端、GUI 客户端三个组件。

## 系统要求

- Go 1.26+ (编译)
- Linux / Windows (运行)

## 编译

```bash
# 安装 Go 1.26+
cd /tmp && wget -q https://go.dev/dl/go1.26.4.linux-amd64.tar.gz && rm -rf /usr/local/go && tar -C /usr/local -xzf go1.26.4.linux-amd64.tar.gz && rm go1.26.4.linux-amd64.tar.gz
export PATH=/usr/local/go/bin:$PATH

cd /root/x-tunnel
make all       # 编译全部
make windows   # 交叉编译 Windows
```

## 服务端部署

### 配置文件 server.json

```json
{
  "listen": "wss://0.0.0.0:8443/tunnel",
  "token": "your-secret-token",
  "cidr": "0.0.0.0/0,::/0",
  "cert": "/etc/ssl/cf-origin.pem",
  "key": "/etc/ssl/cf-origin-key.pem",
  "socks5_proxy": "",
  "global": {
    "dial_timeout_sec": 3,
    "ws_handshake_timeout_sec": 5,
    "reconnect_delay_sec": 1,
    "rtt_probe_timeout_sec": 2,
    "read_buf": 65536
  }
}
```

### systemd 服务

```ini
# /etc/systemd/system/x-tunnel.service
[Unit]
Description=x-tunnel Server
After=network.target

[Service]
Type=simple
ExecStart=/root/x-tunnel/x-tunnel-server -l wss://0.0.0.0:8443/tunnel -token your-token -cert /etc/ssl/cf-origin.pem -key /etc/ssl/cf-origin-key.pem
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-target.target
```

```bash
systemctl daemon-reload
systemctl enable --now x-tunnel
```

## 客户端使用

### GUI 客户端 (推荐)

双击 `x-tunnel-gui.exe` 启动，自动最小化到系统托盘并打开 Web 控制面板。

- 配置文件：`x-tunnel-config.json`（与 exe 同目录）
- 无 cmd 窗口，仅系统托盘图标

#### ECH 配置自动管理

GUI 客户端支持 ECH 配置自动获取和缓存：

1. 配置 **ECH 配置 URL**（如 `https://api.example.com/api/ech/raw?domain=cloudflare-ech.com`）
2. 启动时自动从 URL 获取最新 ECH 配置并缓存到配置文件
3. 后续启动优先使用缓存的配置
4. 连接失败时自动刷新并更新缓存

无需手动填写 ECH base64 配置。

### CLI 客户端

```bash
# 使用配置文件
./x-tunnel-client -config client.json

# 命令行参数
./x-tunnel-client -f wss://your-server.com -l socks5://0.0.0.0:1080 -token your-token
```

### 配置文件 client.json

```json
{
  "server": "wss://your-server.com:8443/tunnel",
  "token": "your-secret-token",
  "listen": ["socks5://0.0.0.0:1080"],
  "connections": 3,
  "ech_domain": "cloudflare-ech.com",
  "dns_server": "https://doh.pub/dns-query",
  "ech_config_url": "https://api.example.com/api/ech/raw?domain=cloudflare-ech.com",
  "target_ips": [],
  "udp_block_ports": "443"
}
```

### ECH 配置来源优先级

1. **`ech_config`** — 缓存值（自动管理，无需手动填写）
2. **`ech_config_url`** — 从 URL 获取（启动时自动获取并缓存）
3. **DNS 查询** — 通过 `ech_domain` + `dns_server` 查询

## Cloudflare CDN + ECH + 优选IP

### 服务端

1. 域名开启 Cloudflare CDN（橙色云朵）
2. SSL/TLS 模式设为 "完全(严格)"
3. 申请 Cloudflare Origin Certificate
4. 启动服务端：
```bash
./x-tunnel-server -l wss://0.0.0.0:8443/tunnel -token your-token -cert cf.pem -key cf-key.pem
```

### 客户端

GUI 中配置：
- 服务器地址：`wss://your-domain.com/tunnel`
- ECH 域名：`your-domain.com`
- ECH 配置 URL：`https://api.example.com/api/ech/raw?domain=cloudflare-ech.com`
- 指定连接 IP：优选 IP 列表

## 防火墙

```bash
ufw allow 8443/tcp
```
