package server

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/x-tunnel/internal/config"
)

// Server WebSocket 隧道服务端
type Server struct {
	cfg      *config.ServerConfig
	sessions *SessionManager
	stats    *StatsCollector
	socks5   *SOCKS5Config
	upgrader websocket.Upgrader
}

// NewServer 创建服务端
func NewServer(cfg *config.ServerConfig) *Server {
	stats := NewStatsCollector()
	sm := NewSessionManager(stats)

	s := &Server{
		cfg:      cfg,
		sessions: sm,
		stats:    stats,
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(r *http.Request) bool { return true },
			ReadBufferSize:  cfg.Global.ReadBuf,
			WriteBufferSize: cfg.Global.ReadBuf,
		},
	}

	if cfg.Token != "" {
		s.upgrader.Subprotocols = []string{cfg.Token}
	}

	if cfg.SOCKS5Proxy != "" {
		socks5Cfg, err := ParseSOCKS5Addr(cfg.SOCKS5Proxy)
		if err != nil {
			log.Fatalf("[服务端] 解析SOCKS5代理地址失败: %v", err)
		}
		s.socks5 = socks5Cfg
		log.Printf("[服务端] 使用SOCKS5前置代理: %s", socks5Cfg.Host)
		if socks5Cfg.Username != "" {
			log.Printf("[服务端] SOCKS5代理认证已启用")
		}
	} else {
		log.Printf("[服务端] 直连模式（未配置SOCKS5代理）")
	}

	return s
}

// Start 启动服务端
func (s *Server) Start() error {
	u, err := url.Parse(s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("WS 地址无效: %v", err)
	}
	path := u.Path
	if path == "" {
		path = "/"
	}

	var allowedNets []*net.IPNet
	for _, cidr := range strings.Split(s.cfg.CIDR, ",") {
		_, allowedNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return fmt.Errorf("CIDR 解析失败: %v", err)
		}
		allowedNets = append(allowedNets, allowedNet)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		s.handleWebSocket(w, r, allowedNets)
	})

	addr := u.Host
	if u.Scheme == "wss" {
		server := &http.Server{Addr: addr, Handler: mux}
		if s.cfg.Cert != "" && s.cfg.Key != "" {
			server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
			log.Printf("[服务端] WSS 启动 %s%s", addr, path)
			return server.ListenAndServeTLS(s.cfg.Cert, s.cfg.Key)
		}
		cert, err := GenerateSelfSignedCert()
		if err != nil {
			return fmt.Errorf("生成自签名证书失败: %v", err)
		}
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
		log.Printf("[服务端] WSS 启动 %s%s (自签名证书)", addr, path)
		return server.ListenAndServeTLS("", "")
	}

	log.Printf("[服务端] WS 启动 %s%s", addr, path)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request, allowedNets []*net.IPNet) {
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "错误的请求", http.StatusBadRequest)
		return
	}
	ip := net.ParseIP(clientIP)
	allowed := false
	for _, n := range allowedNets {
		if n.Contains(ip) {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}
	if s.cfg.Token != "" {
		if r.Header.Get("Sec-WebSocket-Protocol") != s.cfg.Token {
			log.Printf("[服务端] Token 认证失败，来源 IP: %s", clientIP)
			http.Error(w, "未授权", http.StatusUnauthorized)
			return
		}
	}
	wsConn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	cid := r.URL.Query().Get("client_id")
	if cid == "" {
		cid = uuid.NewString()
	}
	channelID := uint64(0)
	if v := r.URL.Query().Get("channel_id"); v != "" {
		if parsed, parseErr := strconv.ParseUint(v, 10, 64); parseErr == nil {
			channelID = parsed
		}
	}
	session := s.sessions.GetOrCreateSession(cid, clientIP)
	ch := session.AddChannel(wsConn, channelID)
	log.Printf("[服务端] 客户端通道 %d 连接, 客户端ID: %s, IP: %s", ch.id, cid, clientIP)

	s.stats.UpdateClientStats(cid, func(stats *ClientStats) {
		stats.ChannelCount = session.ChannelCount()
		stats.LastActiveAt = time.Now()
	})

	go HandleWebSocketChannel(ch, s.sessions, s.socks5, GlobalCfg{
		DialTimeout: s.cfg.Global.DialTimeout,
		ReadBuf:     s.cfg.Global.ReadBuf,
	})
}
