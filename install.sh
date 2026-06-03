#!/bin/bash
set -e

# x-tunnel 服务端一键部署脚本
# 用法: bash <(curl -fsSL https://raw.githubusercontent.com/user/x-tunnel/main/install.sh)

REPO="your-org/x-tunnel"  # GitHub 仓库，按需修改
BINARY_NAME="x-tunnel-server"
INSTALL_DIR="/opt/x-tunnel"
SERVICE_NAME="x-tunnel"
CF_SERVICE_NAME="x-tunnel-argo"
GITHUB="https://github.com"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${GREEN}[✓]${NC} $1"; }
warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[✗]${NC} $1"; exit 1; }
step()  { echo -e "\n${BLUE}━━━ $1 ━━━${NC}"; }

# 全局变量
USE_ARGO=false
ARGO_MODE=""
ARGO_DOMAIN=""
ARGO_TUNNEL_NAME=""
LISTEN_ADDR=""
TOKEN=""
PORT=""
CERT_PATH=""
KEY_PATH=""

# 检查 root
check_root() {
    [[ $EUID -ne 0 ]] && error "请使用 root 运行此脚本"
}

# 检测系统架构
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *)             error "不支持的架构: $(uname -m)" ;;
    esac
}

# 检测公网 IP
detect_public_ip() {
    local ip=""
    for url in "https://api.ipify.org" "https://ifconfig.me" "https://icanhazip.com"; do
        ip=$(curl -s4 --connect-timeout 3 "$url" 2>/dev/null | tr -d '\n')
        if [[ -n "$ip" && "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            echo "$ip"
            return 0
        fi
    done
    return 1
}

# 检测是否为 NAT
detect_nat() {
    step "检测网络环境"
    local public_ip
    public_ip=$(detect_public_ip) || true

    if [[ -z "$public_ip" ]]; then
        warn "无法获取公网 IP，可能为 NAT 环境"
        return 0
    fi

    # 检查是否有公网 IP 绑定在网卡上
    local local_ips
    local_ips=$(hostname -I 2>/dev/null || ip -4 addr show | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | grep -v '^127')

    if echo "$local_ips" | grep -qw "$public_ip"; then
        info "检测到公网 IP: ${public_ip}"
        return 1  # 有公网 IP，非 NAT
    else
        info "检测到公网 IP: ${public_ip} (未绑定本机，可能为 NAT)"
        return 0  # NAT
    fi
}

# 安装 cloudflared
install_cloudflared() {
    if command -v cloudflared &>/dev/null; then
        info "cloudflared 已安装: $(cloudflared --version 2>&1 | head -1)"
        return 0
    fi

    step "安装 cloudflared"
    local arch
    arch=$(detect_arch)
    local url

    case "$arch" in
        amd64) url="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64" ;;
        arm64) url="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm64" ;;
    esac

    info "下载 cloudflared..."
    curl -fSL -o /usr/local/bin/cloudflared "$url"
    chmod +x /usr/local/bin/cloudflared
    info "cloudflared 已安装: $(cloudflared --version 2>&1 | head -1)"
}

# 配置 Argo Quick Tunnel (临时，无需登录)
setup_argo_quick() {
    ARGO_MODE="quick"
    info "使用 Quick Tunnel 模式（临时 URL，重启后变化）"
    # Quick tunnel 不需要额外配置，cloudflared 会自动生成 trycloudflare.com 域名
}

# 配置 Argo Named Tunnel (永久，需要 Cloudflare 账号)
setup_argo_named() {
    ARGO_MODE="named"

    echo -e "\n${CYAN}Cloudflare Tunnel 名称${NC} (用于标识，如 x-tunnel):"
    read -rp "> " ARGO_TUNNEL_NAME
    ARGO_TUNNEL_NAME=${ARGO_TUNNEL_NAME:-x-tunnel}

    echo -e "\n${CYAN}绑定域名${NC} (如 tunnel.example.com，需已托管在 Cloudflare):"
    read -rp "> " ARGO_DOMAIN

    if [[ -z "$ARGO_DOMAIN" ]]; then
        error "Named Tunnel 模式必须指定域名"
    fi

    # 检查 cloudflared 是否已登录
    if [[ ! -f /root/.cloudflared/cert.pem ]]; then
        echo -e "\n${YELLOW}需要登录 Cloudflare 授权:${NC}"
        echo -e "请在浏览器中完成授权，完成后继续...\n"
        cloudflared tunnel login
    fi

    # 创建隧道
    if ! cloudflared tunnel list | grep -q "$ARGO_TUNNEL_NAME"; then
        info "创建隧道: ${ARGO_TUNNEL_NAME}"
        cloudflared tunnel create "$ARGO_TUNNEL_NAME"
    else
        info "隧道已存在: ${ARGO_TUNNEL_NAME}"
    fi

    # 获取隧道 ID
    local tunnel_id
    tunnel_id=$(cloudflared tunnel list | grep "$ARGO_TUNNEL_NAME" | awk '{print $1}')
    info "隧道 ID: ${tunnel_id}"

    # DNS 路由
    info "配置 DNS 路由: ${ARGO_DOMAIN} -> ${ARGO_TUNNEL_NAME}"
    cloudflared tunnel route dns "$ARGO_TUNNEL_NAME" "$ARGO_DOMAIN" 2>/dev/null || true

    # 生成 cloudflared 配置
    mkdir -p /etc/cloudflared
    cat > /etc/cloudflared/${ARGO_TUNNEL_NAME}.yml <<EOF
tunnel: ${tunnel_id}
credentials-file: /root/.cloudflared/${tunnel_id}.json

ingress:
  - hostname: ${ARGO_DOMAIN}
    service: http://127.0.0.1:${PORT}
    originRequest:
      noTLSVerify: true
  - service: http_status:404
EOF
    info "cloudflared 配置已保存"
}

# Argo 交互选择
configure_argo() {
    step "Argo Tunnel 配置 (NAT 穿透)"

    echo -e "${CYAN}选择 Argo 模式:${NC}"
    echo "  1) Quick Tunnel — 临时 URL (xxx.trycloudflare.com)，无需账号，重启后变化"
    echo "  2) Named Tunnel — 绑定自有域名，需 Cloudflare 账号，永久有效"
    read -rp "选择 [1-2，默认 1]: " argo_choice
    argo_choice=${argo_choice:-1}

    case $argo_choice in
        1) setup_argo_quick ;;
        2) setup_argo_named ;;
        *) error "无效选择" ;;
    esac

    USE_ARGO=true
}

# 下载二进制
download_binary() {
    step "下载 ${BINARY_NAME}"
    local arch
    arch=$(detect_arch)
    local url="${GITHUB}/${REPO}/releases/latest/download/${BINARY_NAME}-linux-${arch}"

    info "架构: linux/${arch}"
    info "下载: ${url}"

    if command -v curl &>/dev/null; then
        curl -fSL -o "${INSTALL_DIR}/${BINARY_NAME}" "$url"
    elif command -v wget &>/dev/null; then
        wget -qO "${INSTALL_DIR}/${BINARY_NAME}" "$url"
    else
        error "需要 curl 或 wget"
    fi

    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    info "已安装到 ${INSTALL_DIR}/${BINARY_NAME}"
}

# 交互式配置
configure() {
    step "配置服务端"

    # 监听端口
    echo -e "${CYAN}监听端口${NC} (默认 8443):"
    read -rp "> " PORT
    PORT=${PORT:-8443}

    # WebSocket 路径
    echo -e "\n${CYAN}WebSocket 路径${NC} (默认 /tunnel，留空则无路径):"
    read -rp "> " WS_PATH
    WS_PATH=${WS_PATH:-/tunnel}

    # Token
    echo -e "\n${CYAN}Token 认证密钥${NC} (留空自动生成):"
    read -rp "> " TOKEN
    if [[ -z "$TOKEN" ]]; then
        TOKEN=$(head -c 24 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 16)
        info "已生成 Token: ${TOKEN}"
    fi

    # Argo 模式下固定使用 ws:// 自签名或无 TLS
    if $USE_ARGO; then
        # Argo 隧道到本地用 ws 即可，cloudflared 自己处理外部 TLS
        LISTEN_ADDR="ws://127.0.0.1:${PORT}${WS_PATH}"
        CERT_PATH=""
        KEY_PATH=""
        info "Argo 模式: 本地监听 ws://127.0.0.1:${PORT}${WS_PATH}"
        return
    fi

    # TLS 模式 (非 Argo)
    echo -e "\n${CYAN}TLS 模式:${NC}"
    echo "  1) 自签名证书 (适合直连测试)"
    echo "  2) Cloudflare Origin 证书 (适合 CDN)"
    echo "  3) 自定义证书路径"
    echo "  4) 不使用 TLS (ws://)"
    read -rp "选择 [1-4，默认 2]: " TLS_MODE
    TLS_MODE=${TLS_MODE:-2}

    CERT_PATH=""
    KEY_PATH=""
    SCHEME="wss"

    case $TLS_MODE in
        1)
            step "生成自签名证书"
            CERT_PATH="${INSTALL_DIR}/cert.pem"
            KEY_PATH="${INSTALL_DIR}/key.pem"
            if command -v openssl &>/dev/null; then
                openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
                    -nodes -days 3650 \
                    -keyout "$KEY_PATH" -out "$CERT_PATH" \
                    -subj "/CN=x-tunnel" 2>/dev/null
                info "证书已生成: ${CERT_PATH}"
            else
                info "将使用内置自签名证书"
                CERT_PATH=""
                KEY_PATH=""
            fi
            ;;
        2)
            step "Cloudflare Origin 证书"
            echo -e "请在 Cloudflare Dashboard 申请 Origin Certificate 并粘贴内容。"
            echo -e "路径: SSL/TLS → 源站服务器 → 创建证书\n"

            CERT_PATH="${INSTALL_DIR}/cf-origin.pem"
            KEY_PATH="${INSTALL_DIR}/cf-origin-key.pem"

            echo -e "${CYAN}粘贴证书内容 (PEM 格式，输入 END 结束):${NC}"
            cert_content=""
            while IFS= read -r line; do
                [[ "$line" == "END" ]] && break
                cert_content+="${line}"$'\n'
            done
            echo "$cert_content" > "$CERT_PATH"
            info "证书已保存"

            echo -e "\n${CYAN}粘贴私钥内容 (PEM 格式，输入 END 结束):${NC}"
            key_content=""
            while IFS= read -r line; do
                [[ "$line" == "END" ]] && break
                key_content+="${line}"$'\n'
            done
            echo "$key_content" > "$KEY_PATH"
            chmod 600 "$KEY_PATH"
            info "私钥已保存"
            ;;
        3)
            echo -e "${CYAN}证书文件路径:${NC}"
            read -rp "> " CERT_PATH
            [[ ! -f "$CERT_PATH" ]] && error "证书文件不存在: $CERT_PATH"

            echo -e "${CYAN}私钥文件路径:${NC}"
            read -rp "> " KEY_PATH
            [[ ! -f "$KEY_PATH" ]] && error "私钥文件不存在: $KEY_PATH"
            ;;
        4)
            SCHEME="ws"
            warn "已禁用 TLS，使用 ws:// 明文模式"
            ;;
    esac

    # CIDR 限制
    echo -e "\n${CYAN}IP 白名单 CIDR${NC} (默认允许所有，留空跳过):"
    read -rp "> " CIDR
    CIDR=${CIDR:-"0.0.0.0/0,::/0"}

    # 前置代理
    echo -e "\n${CYAN}SOCKS5 前置代理${NC} (如 socks5://127.0.0.1:1080，留空跳过):"
    read -rp "> " SOCKS5_PROXY

    LISTEN_ADDR="${SCHEME}://0.0.0.0:${PORT}${WS_PATH}"
}

# 生成服务端配置
generate_config() {
    step "生成配置"

    local cidr="${CIDR:-0.0.0.0/0,::/0}"
    local socks5="${SOCKS5_PROXY:-}"

    # Argo 模式下只监听本地
    if $USE_ARGO; then
        cidr="0.0.0.0/0,::/0"  # Argo 隧道过来的都是本地连接
    fi

    cat > "${INSTALL_DIR}/server.json" <<EOF
{
  "listen": "${LISTEN_ADDR}",
  "token": "${TOKEN}",
  "cidr": "${cidr}",
  "cert": "${CERT_PATH}",
  "key": "${KEY_PATH}",
  "socks5_proxy": "${socks5}",
  "global": {
    "dial_timeout_sec": 3,
    "ws_handshake_timeout_sec": 5,
    "reconnect_delay_sec": 1,
    "rtt_probe_timeout_sec": 2,
    "read_buf": 65536
  }
}
EOF
    info "配置已保存到 ${INSTALL_DIR}/server.json"
}

# 安装 x-tunnel 服务
install_service() {
    step "安装 systemd 服务"

    local cmd_args="-config ${INSTALL_DIR}/server.json"

    cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=x-tunnel Server
After=network.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/${BINARY_NAME} ${cmd_args}
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}" --now
    sleep 1

    if systemctl is-active --quiet "${SERVICE_NAME}"; then
        info "x-tunnel 服务已启动"
    else
        error "服务启动失败: journalctl -u ${SERVICE_NAME} -n 20"
    fi
}

# 安装 Argo 服务
install_argo_service() {
    step "安装 Argo Tunnel 服务"

    if [[ "$ARGO_MODE" == "quick" ]]; then
        cat > "/etc/systemd/system/${CF_SERVICE_NAME}.service" <<EOF
[Unit]
Description=Cloudflare Argo Tunnel (x-tunnel)
After=network.target ${SERVICE_NAME}.service
Requires=${SERVICE_NAME}.service

[Service]
Type=simple
ExecStart=/usr/local/bin/cloudflared tunnel --url http://127.0.0.1:${PORT} --no-autoupdate
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    else
        cat > "/etc/systemd/system/${CF_SERVICE_NAME}.service" <<EOF
[Unit]
Description=Cloudflare Argo Tunnel (x-tunnel)
After=network.target ${SERVICE_NAME}.service
Requires=${SERVICE_NAME}.service

[Service]
Type=simple
ExecStart=/usr/local/bin/cloudflared tunnel --config /etc/cloudflared/${ARGO_TUNNEL_NAME}.yml run
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    fi

    systemctl daemon-reload
    systemctl enable "${CF_SERVICE_NAME}" --now
    sleep 2

    if systemctl is-active --quiet "${CF_SERVICE_NAME}"; then
        info "Argo Tunnel 服务已启动"
    else
        warn "Argo Tunnel 启动可能需要几秒，检查日志: journalctl -u ${CF_SERVICE_NAME} -n 20"
    fi
}

# 获取 Argo 分配的 URL (Quick Tunnel)
get_argo_url() {
    if [[ "$ARGO_MODE" != "quick" ]]; then
        echo "$ARGO_DOMAIN"
        return
    fi

    # 从日志中提取 trycloudflare.com URL
    local url=""
    for i in $(seq 1 15); do
        url=$(journalctl -u "${CF_SERVICE_NAME}" --no-pager -n 50 2>/dev/null | grep -oP 'https://[a-z0-9-]+\.trycloudflare\.com' | tail -1)
        if [[ -n "$url" ]]; then
            echo "$url"
            return 0
        fi
        sleep 1
    done
    echo ""
}

# 配置防火墙
setup_firewall() {
    # Argo 模式不需要开放端口
    if $USE_ARGO; then
        return
    fi

    if command -v ufw &>/dev/null; then
        step "配置防火墙"
        ufw allow "$PORT/tcp" >/dev/null 2>&1 && info "已放行 ${PORT}/tcp (ufw)"
    elif command -v firewall-cmd &>/dev/null; then
        step "配置防火墙"
        firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null 2>/dev/null
        firewall-cmd --reload >/dev/null 2>&1 && info "已放行 ${PORT}/tcp (firewalld)"
    fi
}

# 打印摘要
summary() {
    step "部署完成"

    local connect_addr="$LISTEN_ADDR"
    local extra_info=""

    if $USE_ARGO; then
        if [[ "$ARGO_MODE" == "quick" ]]; then
            echo -e "${YELLOW}等待 Argo 分配 URL...${NC}"
            local argo_url
            argo_url=$(get_argo_url)
            if [[ -n "$argo_url" ]]; then
                connect_addr="${argo_url}/tunnel"
                extra_info="
${GREEN}Argo Tunnel:${NC}
  模式:      Quick Tunnel (临时)
  外部地址:  ${connect_addr}
  注意:      重启后 URL 会变化
  管理面板:  http://127.0.0.1:${PORT}"
            else
                connect_addr="等待分配中... (检查 journalctl -u ${CF_SERVICE_NAME})"
                extra_info="
${YELLOW}Argo Tunnel:${NC}
  URL 分配中，请稍后查看日志:
  journalctl -u ${CF_SERVICE_NAME} -f"
            fi
        else
            connect_addr="wss://${ARGO_DOMAIN}/tunnel"
            extra_info="
${GREEN}Argo Tunnel:${NC}
  模式:      Named Tunnel (永久)
  域名:      ${ARGO_DOMAIN}
  配置:      /etc/cloudflared/${ARGO_TUNNEL_NAME}.yml"
        fi
    fi

    echo -e "
${GREEN}服务端信息:${NC}
  本地监听:  ${LISTEN_ADDR}
  Token:     ${TOKEN}
  配置文件:  ${INSTALL_DIR}/server.json
  证书:      ${CERT_PATH:-无 (Argo/自签名)}
${extra_info}

${GREEN}管理命令:${NC}
  x-tunnel:  systemctl {start|stop|restart|status} ${SERVICE_NAME}
  日志:      journalctl -u ${SERVICE_NAME} -f"
    if $USE_ARGO; then
        echo -e "  Argo:      systemctl {start|stop|restart|status} ${CF_SERVICE_NAME}"
        echo -e "  Argo 日志: journalctl -u ${CF_SERVICE_NAME} -f"
    fi
    echo -e "
${GREEN}客户端连接:${NC}
  地址:  ${connect_addr}
  Token: ${TOKEN}
"
}

# 卸载
uninstall() {
    step "卸载 x-tunnel"

    systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"

    systemctl stop "${CF_SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${CF_SERVICE_NAME}" 2>/dev/null || true
    rm -f "/etc/systemd/system/${CF_SERVICE_NAME}.service"

    systemctl daemon-reload
    rm -rf "${INSTALL_DIR}"
    info "已卸载 x-tunnel"

    echo -e "\n是否同时卸载 cloudflared? [y/N]"
    read -rp "> " remove_cf
    if [[ "$remove_cf" =~ ^[Yy]$ ]]; then
        rm -f /usr/local/bin/cloudflared
        rm -rf /root/.cloudflared /etc/cloudflared
        info "已卸载 cloudflared"
    fi

    exit 0
}

# 主流程
main() {
    echo -e "${CYAN}"
    echo "  ╔═══════════════════════════════╗"
    echo "  ║    x-tunnel 服务端部署脚本     ║"
    echo "  ╚═══════════════════════════════╝"
    echo -e "${NC}"

    # 参数处理
    case "${1:-}" in
        uninstall|remove)
            uninstall
            ;;
    esac

    check_root

    mkdir -p "${INSTALL_DIR}"

    # 检测网络环境
    if detect_nat; then
        echo -e "\n${CYAN}检测到 NAT 环境，是否使用 Argo Tunnel 穿透?${NC}"
        echo "  1) 是，配置 Argo Tunnel (推荐)"
        echo "  2) 否，直接监听端口 (需要有公网 IP 或端口映射)"
        read -rp "选择 [1-2，默认 1]: " nat_choice
        nat_choice=${nat_choice:-1}

        if [[ "$nat_choice" == "1" ]]; then
            install_cloudflared
            configure_argo
        fi
    else
        echo -e "\n${CYAN}是否使用 Argo Tunnel?${NC}"
        echo "  1) 否，直接监听公网端口 (推荐)"
        echo "  2) 是，使用 Argo Tunnel"
        read -rp "选择 [1-2，默认 1]: " argo_choice
        argo_choice=${argo_choice:-1}

        if [[ "$argo_choice" == "2" ]]; then
            install_cloudflared
            configure_argo
        fi
    fi

    download_binary
    configure
    generate_config
    install_service
    setup_firewall

    if $USE_ARGO; then
        install_argo_service
    fi

    summary
}

main "$@"
