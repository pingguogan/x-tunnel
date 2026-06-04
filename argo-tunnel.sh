#!/bin/bash
set -e

# Cloudflare Argo Tunnel 独立部署脚本
# 支持 Quick Tunnel (临时) 和 Named Tunnel (永久) 两种模式
# 可与任意本地服务配合使用 (x-tunnel, 3x-ui, nginx 等)
# 支持 TCP 端口和 Unix 套接字两种转发方式
#
# 用法:
#   bash argo-tunnel.sh                          # 交互式配置
#   bash argo-tunnel.sh --port 8080              # TCP 端口转发 (Quick 模式)
#   bash argo-tunnel.sh --socket /run/xray/in.sock  # Unix 套接字转发 (Quick 模式)
#   bash argo-tunnel.sh --socket /run/xray/in.sock --domain x.example.com --name my-tunnel  # Named 模式
#   bash argo-tunnel.sh --port 8080 --service 3x-ui  # 指定关联服务名 (systemd 依赖)
#   bash argo-tunnel.sh uninstall                # 卸载

# ─── 配置 ───────────────────────────────────────────────────────────────────────

CF_SERVICE_NAME="cf-argo-tunnel"
CF_CONFIG_DIR="/etc/cloudflared"
ARGO_MODE=""
ARGO_DOMAIN=""
ARGO_TUNNEL_NAME=""
CF_TUNNEL_ID=""
TARGET_PORT=""
TARGET_HOST="127.0.0.1"
TARGET_SOCK=""      # Unix 套接字路径 (如 /run/xray/in.sock)，与 TARGET_PORT 二选一
DEPEND_SERVICE=""   # 可选: 关联的 systemd 服务名，Argo 服务将依赖它

# ─── 颜色 & 工具 ────────────────────────────────────────────────────────────────

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

# 构建 cloudflared 转发目标 URL
# Unix 套接字: unix:/run/xray/in.sock
# TCP 端口:    http://127.0.0.1:8080
build_service_url() {
    if [[ -n "$TARGET_SOCK" ]]; then
        echo "unix:${TARGET_SOCK}"
    else
        echo "http://${TARGET_HOST}:${TARGET_PORT}"
    fi
}

# 获取用于显示的目标描述
build_target_desc() {
    if [[ -n "$TARGET_SOCK" ]]; then
        echo "unix:${TARGET_SOCK}"
    else
        echo "${TARGET_HOST}:${TARGET_PORT}"
    fi
}

# ─── 系统检测 ────────────────────────────────────────────────────────────────────

check_root() {
    if [[ $EUID -ne 0 ]]; then
        error "请使用 root 运行此脚本"
    fi
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *)             error "不支持的架构: $(uname -m)" ;;
    esac
}

# ─── 安装 cloudflared ───────────────────────────────────────────────────────────

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

# ─── Argo 模式配置 ──────────────────────────────────────────────────────────────

setup_argo_quick() {
    ARGO_MODE="quick"
    info "使用 Quick Tunnel 模式（临时 URL，重启后变化）"
}

setup_argo_named() {
    ARGO_MODE="named"

    if [[ -z "$ARGO_TUNNEL_NAME" ]]; then
        echo -e "\n${CYAN}Cloudflare Tunnel 名称${NC} (用于标识，如 my-tunnel):"
        read -rp "> " ARGO_TUNNEL_NAME
        ARGO_TUNNEL_NAME=${ARGO_TUNNEL_NAME:-my-tunnel}
    fi

    if [[ -z "$ARGO_DOMAIN" ]]; then
        echo -e "\n${CYAN}绑定域名${NC} (如 tunnel.example.com，需已托管在 Cloudflare):"
        read -rp "> " ARGO_DOMAIN
    fi

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
    if [[ -z "$tunnel_id" ]]; then
        error "获取隧道 ID 失败，请检查 cloudflared 是否正常"
    fi
    info "隧道 ID: ${tunnel_id}"

    # DNS 路由
    info "配置 DNS 路由: ${ARGO_DOMAIN} -> ${ARGO_TUNNEL_NAME}"
    cloudflared tunnel route dns "$ARGO_TUNNEL_NAME" "$ARGO_DOMAIN" 2>/dev/null || true

    CF_TUNNEL_ID="${tunnel_id}"
}

# ─── 交互式选择 Argo 模式 ───────────────────────────────────────────────────────

configure_argo() {
    step "Argo Tunnel 配置"

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
}

# ─── 生成 cloudflared 配置 ──────────────────────────────────────────────────────

generate_argo_config() {
    local service_url
    service_url=$(build_service_url)

    mkdir -p "$CF_CONFIG_DIR"
    cat > "${CF_CONFIG_DIR}/${ARGO_TUNNEL_NAME}.yml" <<EOF
tunnel: ${CF_TUNNEL_ID}
credentials-file: /root/.cloudflared/${CF_TUNNEL_ID}.json

ingress:
  - hostname: ${ARGO_DOMAIN}
    service: "${service_url}"
  - service: http_status:404
EOF
    info "cloudflared 配置已保存: ${CF_CONFIG_DIR}/${ARGO_TUNNEL_NAME}.yml"
}

# ─── 安装 systemd 服务 ──────────────────────────────────────────────────────────

install_argo_service() {
    step "安装 Argo Tunnel systemd 服务"

    local exec_start=""
    local after_line="After=network.target"
    local requires_line=""
    local service_url
    service_url=$(build_service_url)

    if [[ "$ARGO_MODE" == "quick" ]]; then
        exec_start="/usr/local/bin/cloudflared tunnel --url ${service_url} --no-autoupdate"
    else
        exec_start="/usr/local/bin/cloudflared tunnel --config ${CF_CONFIG_DIR}/${ARGO_TUNNEL_NAME}.yml run"
    fi

    # 如果指定了关联服务，添加依赖
    if [[ -n "$DEPEND_SERVICE" ]]; then
        after_line="After=network.target ${DEPEND_SERVICE}.service"
        requires_line="Requires=${DEPEND_SERVICE}.service"
    fi

    cat > "/etc/systemd/system/${CF_SERVICE_NAME}.service" <<EOF
[Unit]
Description=Cloudflare Argo Tunnel
${after_line}
${requires_line}

[Service]
Type=simple
ExecStart=${exec_start}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "${CF_SERVICE_NAME}" --now

    # 等待隧道建立
    info "等待 Argo 隧道建立..."
    local ok=false
    for i in $(seq 1 15); do
        sleep 2
        if systemctl is-active --quiet "${CF_SERVICE_NAME}"; then
            if [[ "$ARGO_MODE" == "quick" ]]; then
                local url
                url=$(journalctl -u "${CF_SERVICE_NAME}" --no-pager -n 30 2>/dev/null | grep -oP 'https://[a-z0-9-]+\.trycloudflare\.com' | tail -1)
                if [[ -n "$url" ]]; then
                    ok=true
                    break
                fi
            else
                local connected
                connected=$(journalctl -u "${CF_SERVICE_NAME}" --no-pager -n 30 2>/dev/null | grep -c "Registered tunnel connection" || true)
                if [[ "$connected" -gt 0 ]]; then
                    ok=true
                    break
                fi
            fi
        else
            break
        fi
    done

    if $ok; then
        info "Argo Tunnel 已连接"
    else
        warn "Argo Tunnel 可能仍在启动中，检查日志: journalctl -u ${CF_SERVICE_NAME} -f"
    fi
}

# ─── 获取 Quick Tunnel URL ──────────────────────────────────────────────────────

get_argo_url() {
    if [[ "$ARGO_MODE" != "quick" ]]; then
        echo "$ARGO_DOMAIN"
        return
    fi

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

# ─── 打印摘要 ───────────────────────────────────────────────────────────────────

summary() {
    step "部署完成"

    local target_desc
    target_desc=$(build_target_desc)

    echo -e "
${GREEN}Argo Tunnel 信息:${NC}
  模式:        ${ARGO_MODE}
  转发目标:    ${target_desc}
  关联服务:    ${DEPEND_SERVICE:-无}
  服务名:      ${CF_SERVICE_NAME}
  配置目录:    ${CF_CONFIG_DIR}"

    if [[ "$ARGO_MODE" == "quick" ]]; then
        echo -e "${YELLOW}等待 Argo 分配 URL...${NC}"
        local argo_url
        argo_url=$(get_argo_url)
        if [[ -n "$argo_url" ]]; then
            echo -e "
${GREEN}Quick Tunnel:${NC}
  外部地址:    ${argo_url}
  注意:        重启后 URL 会变化"
        else
            echo -e "
${YELLOW}URL 分配中，请稍后查看:${NC}
  journalctl -u ${CF_SERVICE_NAME} -f"
        fi
    else
        echo -e "
${GREEN}Named Tunnel:${NC}
  域名:        ${ARGO_DOMAIN}
  隧道名:      ${ARGO_TUNNEL_NAME}
  隧道 ID:     ${CF_TUNNEL_ID}
  配置文件:    ${CF_CONFIG_DIR}/${ARGO_TUNNEL_NAME}.yml"
    fi

    echo -e "
${GREEN}管理命令:${NC}
  启动:   systemctl start ${CF_SERVICE_NAME}
  停止:   systemctl stop ${CF_SERVICE_NAME}
  重启:   systemctl restart ${CF_SERVICE_NAME}
  状态:   systemctl status ${CF_SERVICE_NAME}
  日志:   journalctl -u ${CF_SERVICE_NAME} -f
"
}

# ─── 快速修改配置 ───────────────────────────────────────────────────────────────

edit_config() {
    step "修改 Argo Tunnel 配置"

    # 检查服务是否存在
    if [[ ! -f "/etc/systemd/system/${CF_SERVICE_NAME}.service" ]]; then
        error "未找到 ${CF_SERVICE_NAME} 服务，请先安装"
    fi

    # 检测当前模式
    local current_exec
    current_exec=$(grep "^ExecStart=" "/etc/systemd/system/${CF_SERVICE_NAME}.service" | cut -d= -f2-)

    if echo "$current_exec" | grep -q "\-\-config"; then
        ARGO_MODE="named"
    else
        ARGO_MODE="quick"
    fi

    # 读取当前配置
    local current_target=""
    local current_domain=""
    local current_tunnel_name=""
    local current_tunnel_id=""

    if [[ "$ARGO_MODE" == "named" ]]; then
        # 从 ExecStart 提取配置文件路径
        local config_file
        config_file=$(echo "$current_exec" | grep -oP '(?<=--config )\S+')

        if [[ -f "$config_file" ]]; then
            current_tunnel_id=$(grep "^tunnel:" "$config_file" | awk '{print $2}')
            current_domain=$(grep "hostname:" "$config_file" | awk '{print $2}')
            current_tunnel_name=$(basename "$config_file" .yml)
            local service_line
            service_line=$(grep "service:" "$config_file" | head -1 | sed 's/.*service: *"\?\([^"]*\)"\?.*/\1/')
            if echo "$service_line" | grep -q "^unix:"; then
                current_target="$service_line"
            else
                current_target="$service_line"
            fi
        fi
    else
        # Quick 模式从 ExecStart 提取
        current_target=$(echo "$current_exec" | grep -oP '(?<=--url )\S+')
    fi

    # 显示当前配置
    echo -e "\n${GREEN}当前配置:${NC}"
    echo -e "  模式:     ${ARGO_MODE}"
    echo -e "  目标:     ${current_target}"
    if [[ "$ARGO_MODE" == "named" ]]; then
        echo -e "  域名:     ${current_domain}"
        echo -e "  隧道名:   ${current_tunnel_name}"
        echo -e "  隧道 ID:  ${current_tunnel_id}"
    fi

    # 选择修改项
    echo -e "\n${CYAN}选择修改项:${NC}"
    echo "  1) 修改转发目标 (端口/套接字)"
    if [[ "$ARGO_MODE" == "named" ]]; then
        echo "  2) 修改绑定域名"
        echo "  3) 同时修改目标和域名"
    fi
    echo "  0) 取消"
    read -rp "选择: " edit_choice

    case $edit_choice in
        0)
            info "已取消"
            return
            ;;
        1)
            # 修改目标
            echo -e "\n${CYAN}选择新转发方式:${NC}"
            echo "  1) TCP 端口"
            echo "  2) Unix 套接字"
            read -rp "选择 [1-2]: " fwd_choice

            case $fwd_choice in
                1)
                    echo -e "${CYAN}新端口${NC}:"
                    read -rp "> " TARGET_PORT
                    if ! [[ "$TARGET_PORT" =~ ^[0-9]+$ ]] || [ "$TARGET_PORT" -lt 1 ] || [ "$TARGET_PORT" -gt 65535 ]; then
                        error "端口无效"
                    fi
                    TARGET_SOCK=""
                    TARGET_HOST="127.0.0.1"
                    ;;
                2)
                    echo -e "${CYAN}新套接字路径${NC}:"
                    read -rp "> " TARGET_SOCK
                    if [[ -z "$TARGET_SOCK" ]]; then
                        error "路径不能为空"
                    fi
                    TARGET_PORT=""
                    ;;
                *)
                    error "无效选择"
                    ;;
            esac

            ARGO_DOMAIN="$current_domain"
            ARGO_TUNNEL_NAME="$current_tunnel_name"
            CF_TUNNEL_ID="$current_tunnel_id"
            ;;
        2)
            if [[ "$ARGO_MODE" != "named" ]]; then
                error "Quick 模式不支持修改域名"
            fi
            echo -e "${CYAN}新域名${NC}:"
            read -rp "> " ARGO_DOMAIN
            if [[ -z "$ARGO_DOMAIN" ]]; then
                error "域名不能为空"
            fi

            # 从当前配置提取目标
            if echo "$current_target" | grep -q "^unix:"; then
                TARGET_SOCK="${current_target#unix:}"
            else
                TARGET_PORT=$(echo "$current_target" | grep -oP ':\K[0-9]+')
                TARGET_HOST=$(echo "$current_target" | grep -oP '//\K[^:]+')
            fi
            ARGO_TUNNEL_NAME="$current_tunnel_name"
            CF_TUNNEL_ID="$current_tunnel_id"

            # 更新 DNS 路由
            info "更新 DNS 路由: ${ARGO_DOMAIN} -> ${ARGO_TUNNEL_NAME}"
            cloudflared tunnel route dns "$ARGO_TUNNEL_NAME" "$ARGO_DOMAIN" 2>/dev/null || true
            ;;
        3)
            if [[ "$ARGO_MODE" != "named" ]]; then
                error "Quick 模式不支持此选项"
            fi
            echo -e "${CYAN}新域名${NC}:"
            read -rp "> " ARGO_DOMAIN
            if [[ -z "$ARGO_DOMAIN" ]]; then
                error "域名不能为空"
            fi

            echo -e "\n${CYAN}选择新转发方式:${NC}"
            echo "  1) TCP 端口"
            echo "  2) Unix 套接字"
            read -rp "选择 [1-2]: " fwd_choice

            case $fwd_choice in
                1)
                    echo -e "${CYAN}新端口${NC}:"
                    read -rp "> " TARGET_PORT
                    TARGET_SOCK=""
                    TARGET_HOST="127.0.0.1"
                    ;;
                2)
                    echo -e "${CYAN}新套接字路径${NC}:"
                    read -rp "> " TARGET_SOCK
                    TARGET_PORT=""
                    ;;
            esac

            ARGO_TUNNEL_NAME="$current_tunnel_name"
            CF_TUNNEL_ID="$current_tunnel_id"

            info "更新 DNS 路由: ${ARGO_DOMAIN} -> ${ARGO_TUNNEL_NAME}"
            cloudflared tunnel route dns "$ARGO_TUNNEL_NAME" "$ARGO_DOMAIN" 2>/dev/null || true
            ;;
        *)
            error "无效选择"
            ;;
    esac

    # 应用修改
    local new_service_url
    new_service_url=$(build_service_url)

    if [[ "$ARGO_MODE" == "named" ]]; then
        generate_argo_config
    fi

    # 更新 systemd 服务
    local new_exec_start
    if [[ "$ARGO_MODE" == "quick" ]]; then
        new_exec_start="/usr/local/bin/cloudflared tunnel --url ${new_service_url} --no-autoupdate"
    else
        new_exec_start="/usr/local/bin/cloudflared tunnel --config ${CF_CONFIG_DIR}/${ARGO_TUNNEL_NAME}.yml run"
    fi

    # 更新 ExecStart
    sed -i "s|^ExecStart=.*|ExecStart=${new_exec_start}|" "/etc/systemd/system/${CF_SERVICE_NAME}.service"

    systemctl daemon-reload
    systemctl restart "${CF_SERVICE_NAME}"

    info "配置已更新，服务已重启"
    echo -e "\n${GREEN}新配置:${NC}"
    echo -e "  转发目标: $(build_target_desc)"
    if [[ "$ARGO_MODE" == "named" ]]; then
        echo -e "  域名:     ${ARGO_DOMAIN}"
    fi

    # 等待连接
    sleep 3
    if systemctl is-active --quiet "${CF_SERVICE_NAME}"; then
        info "Argo Tunnel 运行中"
    else
        warn "服务启动异常，检查日志: journalctl -u ${CF_SERVICE_NAME} -n 20"
    fi
}

# ─── 卸载 ───────────────────────────────────────────────────────────────────────

uninstall() {
    step "卸载 Argo Tunnel"

    systemctl stop "${CF_SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${CF_SERVICE_NAME}" 2>/dev/null || true
    rm -f "/etc/systemd/system/${CF_SERVICE_NAME}.service"
    systemctl daemon-reload

    info "已移除 ${CF_SERVICE_NAME} 服务"

    # 清理 Named Tunnel 配置
    if [[ -d "$CF_CONFIG_DIR" ]]; then
        echo -e "\n是否删除 cloudflared 配置目录 ${CF_CONFIG_DIR}? [y/N]"
        read -rp "> " remove_config
        if [[ "$remove_config" =~ ^[Yy]$ ]]; then
            rm -rf "$CF_CONFIG_DIR"
            info "已删除 ${CF_CONFIG_DIR}"
        fi
    fi

    # 清理 cloudflared 二进制
    echo -e "\n是否卸载 cloudflared? [y/N]"
    read -rp "> " remove_cf
    if [[ "$remove_cf" =~ ^[Yy]$ ]]; then
        rm -f /usr/local/bin/cloudflared
        info "已卸载 cloudflared"
    fi

    # 清理证书
    echo -e "\n是否删除 Cloudflare 证书 (/root/.cloudflared)? [y/N]"
    read -rp "> " remove_cert
    if [[ "$remove_cert" =~ ^[Yy]$ ]]; then
        rm -rf /root/.cloudflared
        info "已删除 /root/.cloudflared"
    fi

    exit 0
}

# ─── 参数解析 ───────────────────────────────────────────────────────────────────

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --port)
                TARGET_PORT="$2"
                shift 2
                ;;
            --host)
                TARGET_HOST="$2"
                shift 2
                ;;
            --socket)
                TARGET_SOCK="$2"
                shift 2
                ;;
            --domain)
                ARGO_DOMAIN="$2"
                shift 2
                ;;
            --name)
                ARGO_TUNNEL_NAME="$2"
                shift 2
                ;;
            --service)
                DEPEND_SERVICE="$2"
                shift 2
                ;;
            --service-name)
                CF_SERVICE_NAME="$2"
                shift 2
                ;;
            --mode)
                ARGO_MODE="$2"
                shift 2
                ;;
            uninstall|remove)
                uninstall
                ;;
            edit|config)
                edit_config
                exit 0
                ;;
            -h|--help)
                echo "用法: bash argo-tunnel.sh [命令|选项]"
                echo ""
                echo "命令:"
                echo "  edit                 快速修改已有配置 (目标/域名)"
                echo "  uninstall            卸载 Argo Tunnel"
                echo ""
                echo "目标 (二选一):"
                echo "  --port <端口>        TCP 端口转发 (如 8080)"
                echo "  --socket <路径>      Unix 套接字转发 (如 /run/xray/in.sock)"
                echo ""
                echo "选项:"
                echo "  --host <地址>        TCP 模式的本地地址 (默认 127.0.0.1)"
                echo "  --domain <域名>      Named Tunnel 绑定域名"
                echo "  --name <名称>        Named Tunnel 隧道名"
                echo "  --service <服务名>   关联的 systemd 服务名 (添加依赖关系)"
                echo "  --service-name <名>  Argo 服务的 systemd 服务名 (默认 cf-argo-tunnel)"
                echo "  --mode <quick|named> Argo 模式 (省略则交互选择)"
                echo "  -h, --help           显示帮助"
                echo ""
                echo "示例:"
                echo "  # 为 3x-ui 配置 Quick Tunnel (TCP 端口)"
                echo "  bash argo-tunnel.sh --port 54321 --service x-ui"
                echo ""
                echo "  # 为 3x-ui 配置 Quick Tunnel (Unix 套接字)"
                echo "  bash argo-tunnel.sh --socket /run/xray/in.sock --service x-ui"
                echo ""
                echo "  # 为 x-tunnel 配置 Named Tunnel"
                echo "  bash argo-tunnel.sh --port 8080 --domain t.example.com --name my-tunnel --service x-tunnel"
                echo ""
                echo "  # Named Tunnel + Unix 套接字"
                echo "  bash argo-tunnel.sh --socket /run/xray/in.sock --domain x.example.com --name my-xray"
                echo ""
                echo "  # 快速修改已有配置"
                echo "  bash argo-tunnel.sh edit"
                exit 0
                ;;
            *)
                error "未知参数: $1 (使用 -h 查看帮助)"
                ;;
        esac
    done
}

# ─── 主流程 ─────────────────────────────────────────────────────────────────────

main() {
    echo -e "${CYAN}"
    echo "  ╔═══════════════════════════════════╗"
    echo "  ║  Cloudflare Argo Tunnel 部署脚本   ║"
    echo "  ╚═══════════════════════════════════╝"
    echo -e "${NC}"

    check_root

    if ! command -v curl &>/dev/null && ! command -v wget &>/dev/null; then
        error "需要 curl 或 wget，请先安装"
    fi

    parse_args "$@"

    # 交互式获取目标 (如果命令行未指定)
    if [[ -z "$TARGET_PORT" && -z "$TARGET_SOCK" ]]; then
        echo -e "${CYAN}选择转发方式:${NC}"
        echo "  1) TCP 端口 (如 127.0.0.1:8080)"
        echo "  2) Unix 套接字 (如 /run/xray/in.sock)"
        read -rp "选择 [1-2，默认 1]: " fwd_choice
        fwd_choice=${fwd_choice:-1}

        case $fwd_choice in
            1)
                echo -e "${CYAN}本地服务端口${NC} (如 3x-ui 的 54321，x-tunnel 的 8080):"
                read -rp "> " TARGET_PORT
                if ! [[ "$TARGET_PORT" =~ ^[0-9]+$ ]] || [ "$TARGET_PORT" -lt 1 ] || [ "$TARGET_PORT" -gt 65535 ]; then
                    error "端口无效，请输入 1-65535 之间的数字"
                fi
                ;;
            2)
                echo -e "${CYAN}Unix 套接字路径${NC} (如 /run/xray/in.sock):"
                read -rp "> " TARGET_SOCK
                if [[ -z "$TARGET_SOCK" ]]; then
                    error "套接字路径不能为空"
                fi
                ;;
            *)
                error "无效选择"
                ;;
        esac
    fi

    # 参数互斥检查
    if [[ -n "$TARGET_PORT" && -n "$TARGET_SOCK" ]]; then
        error "--port 和 --socket 不能同时使用，请选择其中一个"
    fi

    info "目标: $(build_target_desc)"

    # 安装 cloudflared
    install_cloudflared

    # 交互式选择模式 (如果未通过 --mode 指定)
    if [[ -z "$ARGO_MODE" ]]; then
        configure_argo
    fi

    # Named 模式需要额外配置
    if [[ "$ARGO_MODE" == "named" ]]; then
        setup_argo_named
        generate_argo_config
    fi

    # 安装服务
    install_argo_service

    # 打印摘要
    summary
}

main "$@"
