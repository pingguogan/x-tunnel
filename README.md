# x-tunnel

WebSocket 隧道代理，支持 TCP/UDP 转发、ECH 加密、优选 IP。

## 项目结构

```
x-tunnel/
├── cmd/
│   ├── server/main.go      # 服务端入口
│   ├── client/main.go      # 客户端入口 (命令行)
│   └── gui/main.go         # 客户端入口 (GUI)
├── internal/
│   ├── config/             # 配置管理
│   ├── protocol/           # 协议层 (smux, chunk, conn)
│   ├── server/             # 服务端核心
│   ├── client/             # 客户端核心 (连接池、代理)
│   └── gui/                # GUI 客户端 (Web UI + 系统托盘)
├── server.json             # 服务端配置示例
├── client.json             # 客户端配置示例
├── Makefile                # 构建脚本
├── DEPLOY.md               # 部署文档
└── USAGE.md                # 使用文档
```

## 快速开始

### 环境要求

- Go 1.26+

### 构建

```bash
make all          # 构建所有二进制
make server       # 仅构建服务端
make client       # 仅构建客户端
make gui          # 仅构建 GUI 客户端
make windows      # 交叉编译 Windows (GUI 无 cmd 窗口)
```

### 服务端

```bash
# WSS 模式 (推荐)
./x-tunnel-server -l wss://0.0.0.0:8443/tunnel -token your-token -cert cert.pem -key key.pem

# 使用配置文件
./x-tunnel-server -config server.json
```

### 客户端 (GUI)

双击 `x-tunnel-gui.exe`，自动最小化到系统托盘并打开浏览器。

功能：
- 多服务端配置管理
- SOCKS5 / HTTP / TCP 代理
- ECH 配置自动从 URL 获取并缓存
- 优选 IP、DoH DNS
- 系统托盘（无 cmd 窗口）

### 客户端 (命令行)

```bash
./x-tunnel-client -config client.json
./x-tunnel-client -f wss://your-server.com -l socks5://0.0.0.0:1080
```

## 文档

- [部署文档](DEPLOY.md) — 服务端部署、systemd 配置、Cloudflare CDN 配置
- [使用文档](USAGE.md) — 完整参数说明、使用示例、常见问题
