package client

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/xtaci/smux"

	"github.com/x-tunnel/internal/protocol"
)

// ECHPool 多通道连接池
type ECHPool struct {
	wsServerAddr  string
	connectionNum int
	targetIPs     []string
	clientID      string
	token         string
	insecure      bool
	fallback      bool
	echDomain     string
	dnsServer     string
	echConfig     string // 直接指定的 ECH 配置 (base64)
	echConfigSet  bool   // 是否直接指定了 ECH 配置
	echConfigURL  string // 从 URL 获取 ECH 配置
	readBuf       int

	wsConnsMu     sync.RWMutex
	smuxConns     []*smux.Session
	channelRTT    []int64
	selectCounter uint64

	echListMu     sync.RWMutex
	echList       []byte
	refreshMu     sync.Mutex
	lastRefresh   time.Time              // 上次刷新时间，防止刷新风暴
	onRefreshed   func(echBase64 string) // ECH 刷新成功回调，用于同步配置到 GUI

	// 停止信号
	stopCh chan struct{}
	stopped bool

	// 缓存：系统证书池（避免每次 TLS 连接重复加载）
	rootCAs   *x509.CertPool
	rootCAsOnce sync.Once
}

// NewECHPool 创建连接池
func NewECHPool(addr string, n int, ips []string, clientID string, token string, insecure bool, fallback bool, echDomain, dnsServer, echConfig, echConfigURL string, readBuf int, onRefreshed func(echBase64 string)) *ECHPool {
	total := n
	if len(ips) > 0 {
		total = len(ips) * n
	}
	return &ECHPool{
		wsServerAddr:  addr,
		connectionNum: n,
		targetIPs:     ips,
		clientID:      clientID,
		token:         token,
		insecure:      insecure,
		fallback:      fallback,
		echDomain:     echDomain,
		dnsServer:     dnsServer,
		echConfig:     echConfig,
		echConfigSet:  echConfig != "",
		echConfigURL:  echConfigURL,
		readBuf:       readBuf,
		smuxConns:     make([]*smux.Session, total),
		channelRTT:    make([]int64, total),
		onRefreshed:   onRefreshed,
		stopCh:        make(chan struct{}),
	}
}

// Start 启动连接池
func (p *ECHPool) Start() error {
	log.Printf("[客户端] 连接池启动: fallback=%v, server=%s", p.fallback, p.wsServerAddr)
	if !p.fallback && strings.HasPrefix(p.wsServerAddr, "wss") {
		// 优先级: ech_config > ech_config_url > DNS 查询
		if p.echConfig != "" {
			raw, err := base64.StdEncoding.DecodeString(p.echConfig)
			if err != nil {
				return fmt.Errorf("ECH 配置解码失败: %v", err)
			}
			p.echListMu.Lock()
			p.echList = raw
			p.echListMu.Unlock()
			log.Printf("[客户端] 使用指定的 ECH 配置 (%d 字节)", len(raw))
		} else if p.echConfigURL != "" {
			if err := p.fetchECHFromURL(); err != nil {
				return fmt.Errorf("从 URL 获取 ECH 配置失败: %v", err)
			}
		} else {
			if err := p.prepareECH(); err != nil {
				return fmt.Errorf("获取 ECH 公钥失败: %v", err)
			}
		}
	}
	// 定期刷新 ECH 配置（每 6 小时）
	if !p.fallback && (p.echDomain != "" || p.echConfigURL != "") {
		go p.echRefreshLoop()
	}
	for i := 0; i < len(p.smuxConns); i++ {
		ip := ""
		if len(p.targetIPs) > 0 {
			if idx := i / p.connectionNum; idx < len(p.targetIPs) {
				ip = p.targetIPs[idx]
			}
		}
		go p.dialAndServe(i, ip)
	}
	return nil
}

// Stop 停止连接池
func (p *ECHPool) Stop() {
	if p.stopped {
		return
	}
	p.stopped = true
	close(p.stopCh)

	// 关闭所有 smux 会话
	p.wsConnsMu.Lock()
	for i, sess := range p.smuxConns {
		if sess != nil && !sess.IsClosed() {
			sess.Close()
		}
		p.smuxConns[i] = nil
	}
	p.wsConnsMu.Unlock()

	log.Printf("[客户端] 连接池已停止")
}

func (p *ECHPool) dialAndServe(idx int, ip string) {
	chID := idx + 1
	ipLabel := ip
	if strings.TrimSpace(ipLabel) == "" {
		ipLabel = "自动解析"
	}
	for {
		// 检查是否已停止
		select {
		case <-p.stopCh:
			return
		default:
		}

		wsConn, err := p.dialWebSocket(p.wsServerAddr, 3, ip, p.clientID, chID)
		if err != nil {
			log.Printf("[客户端] 通道 %d (IP:%s) 连接失败: %v", chID, ipLabel, err)
			// 等待或停止
			select {
			case <-p.stopCh:
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}
		wsNet := protocol.NewWSNetConn(wsConn)
		smuxCfg := smux.DefaultConfig()
		smuxCfg.KeepAliveInterval = 5 * time.Second
		smuxCfg.KeepAliveTimeout = 15 * time.Second
		smuxCfg.MaxFrameSize = 32768
		smuxCfg.MaxReceiveBuffer = 4 * 1024 * 1024 // 4MB
		sess, err := smux.Client(wsNet, smuxCfg)
		if err != nil {
			_ = wsConn.Close()
			log.Printf("[客户端] 通道 %d (IP:%s) smux 初始化失败: %v", chID, ipLabel, err)
			time.Sleep(1 * time.Second)
			continue
		}
		p.wsConnsMu.Lock()
		p.smuxConns[idx] = sess
		p.channelRTT[idx] = 0
		p.wsConnsMu.Unlock()
		log.Printf("[客户端] 通道 %d (IP:%s) 就绪 (smux)", chID, ipLabel)
		if rtt, err := p.probeChannelRTTOnce(sess, 2*time.Second); err == nil {
			atomic.StoreInt64(&p.channelRTT[idx], rtt)
		}

		done := make(chan error, 1)
		go p.probeChannelRTT(sess, idx, done)
		var probeErr error
		select {
		case probeErr = <-done:
		case <-wsNet.Dead():
			_ = sess.Close()
			<-done
			probeErr = wsNet.DeadErr()
			if probeErr == nil {
				probeErr = io.EOF
			}
		}

		_ = sess.Close()
		_ = wsConn.Close()

		p.wsConnsMu.Lock()
		p.smuxConns[idx] = nil
		p.channelRTT[idx] = 0
		p.wsConnsMu.Unlock()
		if probeErr != nil {
			log.Printf("[客户端] 通道 %d 断开原因: %v", chID, probeErr)
		}
		log.Printf("[客户端] 通道 %d 断开，重连中...", chID)
		time.Sleep(1 * time.Second)
	}
}

func (p *ECHPool) probeChannelRTT(sess *smux.Session, idx int, done chan error) {
	var exitErr error
	defer func() {
		done <- exitErr
		close(done)
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		rtt, err := p.probeChannelRTTOnce(sess, 2*time.Second)
		if err != nil {
			atomic.StoreInt64(&p.channelRTT[idx], int64(2*time.Second))
			if sess.IsClosed() {
				exitErr = err
				return
			}
			<-ticker.C
			continue
		}
		atomic.StoreInt64(&p.channelRTT[idx], rtt)
		<-ticker.C
	}
}

func (p *ECHPool) probeChannelRTTOnce(sess *smux.Session, timeout time.Duration) (int64, error) {
	start := time.Now()
	s, err := sess.OpenStream()
	if err != nil {
		return 0, err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(timeout))
	if err := protocol.WriteSmuxOpenHeader(s, protocol.StreamKindPing, 0, ""); err != nil {
		return 0, err
	}
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, uint64(start.UnixNano()))
	if _, err := s.Write(payload); err != nil {
		return 0, err
	}
	ack := make([]byte, 8)
	if _, err := io.ReadFull(s, ack); err != nil {
		return 0, err
	}
	if !bytes.Equal(ack, payload) {
		return 0, fmt.Errorf("ping ack mismatch")
	}
	return time.Since(start).Nanoseconds(), nil
}

// OpenBestStream 选择最佳通道并打开流
func (p *ECHPool) OpenBestStream() (*smux.Stream, int, int, error) {
	p.wsConnsMu.RLock()
	type candidate struct {
		idx int
		rtt int64
	}
	cands := make([]candidate, 0, len(p.smuxConns))
	for i, sess := range p.smuxConns {
		if sess == nil || sess.IsClosed() {
			continue
		}
		rtt := atomic.LoadInt64(&p.channelRTT[i])
		if rtt <= 0 {
			rtt = int64(2 * time.Second)
		}
		cands = append(cands, candidate{idx: i, rtt: rtt})
	}
	p.wsConnsMu.RUnlock()
	if len(cands) == 0 {
		return nil, 0, 0, fmt.Errorf("无可用 smux 通道")
	}
	minRTT := cands[0].rtt
	for _, c := range cands[1:] {
		if c.rtt < minRTT {
			minRTT = c.rtt
		}
	}
	tieWindow := int64((10 * time.Millisecond).Nanoseconds())
	near := make([]candidate, 0, len(cands))
	for _, c := range cands {
		if c.rtt <= minRTT+tieWindow {
			near = append(near, c)
		}
	}
	pick := int(atomic.AddUint64(&p.selectCounter, 1)-1) % len(near)
	best := near[pick]
	p.wsConnsMu.RLock()
	sess := p.smuxConns[best.idx]
	p.wsConnsMu.RUnlock()
	if sess == nil || sess.IsClosed() {
		return nil, 0, 0, fmt.Errorf("通道不可用")
	}
	decision := best.idx + 1
	s, err := sess.OpenStream()
	if err != nil {
		return nil, 0, 0, err
	}
	return s, best.idx + 1, decision, nil
}

// OpenTCPStream 打开 TCP 流
func (p *ECHPool) OpenTCPStream(target string, ipStrategy byte) (*smux.Stream, int, int, error) {
	s, chID, decision, err := p.OpenBestStream()
	if err != nil {
		return nil, 0, 0, err
	}
	if err := protocol.WriteSmuxOpenHeader(s, protocol.StreamKindTCP, ipStrategy, target); err != nil {
		_ = s.Close()
		return nil, 0, 0, err
	}
	return s, chID, decision, nil
}

// OpenUDPStream 打开 UDP 流
func (p *ECHPool) OpenUDPStream(target string, ipStrategy byte) (*smux.Stream, int, int, error) {
	s, chID, decision, err := p.OpenBestStream()
	if err != nil {
		return nil, 0, 0, err
	}
	if err := protocol.WriteSmuxOpenHeader(s, protocol.StreamKindUDP, ipStrategy, target); err != nil {
		_ = s.Close()
		return nil, 0, 0, err
	}
	return s, chID, decision, nil
}

// ECH 相关方法
func (p *ECHPool) prepareECH() error {
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		log.Printf("[客户端] DNS查询 ECH: %s -> %s", p.dnsServer, p.echDomain)
		echBase64, err := p.queryHTTPSRecord(p.echDomain, p.dnsServer)
		if err != nil {
			log.Printf("[客户端] DNS 查询失败: %v，重试...", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if echBase64 == "" {
			log.Printf("[客户端] 未找到 ECH 参数，重试...")
			time.Sleep(2 * time.Second)
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(echBase64)
		if err != nil {
			log.Printf("[客户端] ECH Base64 解码失败: %v，重试...", err)
			time.Sleep(2 * time.Second)
			continue
		}
		p.echListMu.Lock()
		p.echList = raw
		p.echListMu.Unlock()
		log.Printf("[客户端] ECHConfigList 长度: %d 字节", len(raw))
		return nil
	}
	return fmt.Errorf("ECH 查询失败，已重试 %d 次，请检查域名是否支持 ECH 或使用 -fallback 禁用 ECH", maxRetries)
}

func (p *ECHPool) refreshECH() error {
	if p.fallback {
		return nil
	}
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()

	// 30 秒内已刷新过，跳过（防止刷新风暴）
	if time.Since(p.lastRefresh) < 30*time.Second {
		return nil
	}

	log.Printf("[客户端] 刷新 ECH 配置...")
	var err error
	if p.echConfigURL != "" {
		err = p.fetchECHFromURL()
	} else {
		err = p.prepareECH()
	}
	if err == nil {
		p.lastRefresh = time.Now()
	}
	return err
}

// fetchECHFromURL 从指定 URL 获取 ECH 配置
func (p *ECHPool) fetchECHFromURL() error {
	log.Printf("[客户端] 从 URL 获取 ECH 配置: %s", p.echConfigURL)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(p.echConfigURL)
	if err != nil {
		return fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %v", err)
	}

	echBase64, err := p.parseECHURLResponse(body)
	if err != nil {
		return err
	}
	if echBase64 == "" {
		return fmt.Errorf("响应中未找到 ECH 配置")
	}

	raw, err := base64.StdEncoding.DecodeString(echBase64)
	if err != nil {
		return fmt.Errorf("ECH Base64 解码失败: %v", err)
	}
	p.echListMu.Lock()
	p.echList = raw
	p.echListMu.Unlock()
	log.Printf("[客户端] 从 URL 获取 ECH 配置成功 (%d 字节)", len(raw))

	// 通知回调：ECH 配置已更新
	if p.onRefreshed != nil {
		p.onRefreshed(echBase64)
	}
	return nil
}

// parseECHURLResponse 解析 URL 响应，支持多种格式
func (p *ECHPool) parseECHURLResponse(body []byte) (string, error) {
	// 1. 尝试 JSON 解析
	var jsonResp struct {
		ECH      string `json:"ech"`
		HasECH   *bool  `json:"has_ech"`
		Records  []struct {
			Data string `json:"data"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &jsonResp); err == nil {
		// 格式 A: {"ech": "base64..."}
		if jsonResp.ECH != "" {
			return jsonResp.ECH, nil
		}
		// 格式 B: {"has_ech": true, "records": [{"data": "1 . ech=BASE64..."}]}
		if jsonResp.HasECH != nil && *jsonResp.HasECH {
			for _, r := range jsonResp.Records {
				if ech := p.extractECHFromHTTPSData(r.Data); ech != "" {
					return ech, nil
				}
			}
		}
		return "", fmt.Errorf("JSON 响应中未找到 ECH 配置")
	}

	// 2. 尝试作为纯 base64 文本
	s := strings.TrimSpace(string(body))
	if _, err := base64.StdEncoding.DecodeString(s); err == nil {
		return s, nil
	}
	if _, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return s, nil
	}

	return "", fmt.Errorf("无法解析响应格式（非 JSON 也非 base64）")
}

// echRefreshLoop 定期刷新 ECH 配置
func (p *ECHPool) echRefreshLoop() {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		if p.fallback {
			continue
		}
		log.Printf("[客户端] 定期刷新 ECH 配置...")
		if err := p.refreshECH(); err != nil {
			log.Printf("[客户端] ECH 配置刷新失败: %v", err)
		} else {
			log.Printf("[客户端] ECH 配置刷新成功")
		}
	}
}

func (p *ECHPool) getECHList() ([]byte, error) {
	if p.fallback {
		return nil, nil
	}
	p.echListMu.RLock()
	defer p.echListMu.RUnlock()
	if len(p.echList) == 0 {
		return nil, errors.New("ECH 配置尚未加载")
	}
	return p.echList, nil
}

func (p *ECHPool) getRootCAs() *x509.CertPool {
	p.rootCAsOnce.Do(func() {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		p.rootCAs = roots
	})
	return p.rootCAs
}

func (p *ECHPool) buildTLSConfig(serverName string) (*tls.Config, error) {
	roots := p.getRootCAs()
	if p.fallback {
		log.Printf("[客户端] TLS 配置: fallback=true, ServerName=%s", serverName)
		return &tls.Config{
			MinVersion:         tls.VersionTLS13,
			ServerName:         serverName,
			RootCAs:            roots,
			InsecureSkipVerify: p.insecure,
		}, nil
	}
	ech, e := p.getECHList()
	if e != nil {
		return nil, e
	}
	log.Printf("[客户端] TLS 配置: ECH 启用, ServerName=%s, ECH 配置长度=%d 字节", serverName, len(ech))
	return &tls.Config{
		MinVersion:                     tls.VersionTLS13,
		ServerName:                     serverName,
		EncryptedClientHelloConfigList: ech,
		EncryptedClientHelloRejectionVerify: func(cs tls.ConnectionState) error {
			return errors.New("服务器拒绝 ECH")
		},
		RootCAs:            roots,
		InsecureSkipVerify: p.insecure,
	}, nil
}

func (p *ECHPool) dialWebSocket(addr string, retries int, ip string, clientID string, channelID int) (*websocket.Conn, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(u.Scheme)

	dialURL := *u
	q := dialURL.Query()
	if clientID != "" {
		q.Set("client_id", clientID)
	}
	if channelID > 0 {
		q.Set("channel_id", fmt.Sprintf("%d", channelID))
	}
	dialURL.RawQuery = q.Encode()
	dialAddr := dialURL.String()

	newDialer := func() websocket.Dialer {
		dialer := websocket.Dialer{
			HandshakeTimeout: 5 * time.Second,
			ReadBufferSize:   p.readBuf,
			WriteBufferSize:  p.readBuf,
		}
		if p.token != "" {
			dialer.Subprotocols = []string{p.token}
		}
		if ip != "" {
			dialer.NetDial = func(network, address string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(address)
				if host, portVal, err := net.SplitHostPort(ip); err == nil {
					return net.DialTimeout(network, net.JoinHostPort(host, portVal), 3*time.Second)
				}
				return net.DialTimeout(network, net.JoinHostPort(ip, port), 3*time.Second)
			}
		}
		return dialer
	}

	if scheme == "ws" {
		dialer := newDialer()
		conn, resp, err := dialer.Dial(dialAddr, nil)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("认证失败：Token 不匹配或未提供")
			}
			return nil, err
		}
		return conn, nil
	}

	serverName := u.Hostname()
	for i := 1; i <= retries; i++ {
		tlsCfg, e := p.buildTLSConfig(serverName)
		if e != nil {
			if i < retries {
				_ = p.refreshECH()
				time.Sleep(1 * time.Second)
				continue
			}
			return nil, e
		}
		dialer := newDialer()
		dialer.TLSClientConfig = tlsCfg
		conn, resp, err := dialer.Dial(dialAddr, nil)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("认证失败：Token 不匹配或未提供")
			}
			// ECH 相关错误，尝试刷新配置
			if !p.fallback && (strings.Contains(err.Error(), "ECH") || strings.Contains(err.Error(), "ech") || strings.Contains(err.Error(), "client hello")) && i < retries {
				log.Printf("[客户端] ECH 连接失败，尝试刷新配置: %v", err)
				_ = p.refreshECH()
				time.Sleep(1 * time.Second)
				continue
			}
			return nil, err
		}
		return conn, nil
	}
	return nil, fmt.Errorf("连接失败")
}

// DNS 查询方法
func (p *ECHPool) queryHTTPSRecord(domain, dnsServer string) (string, error) {
	if strings.HasPrefix(dnsServer, "http://") || strings.HasPrefix(dnsServer, "https://") {
		// 优先尝试 JSON 格式 DoH（更简单可靠，支持 Cloudflare 等）
		ech, err := p.queryDoHJSON(domain, dnsServer)
		if err == nil {
			return ech, nil
		}
		log.Printf("[客户端] JSON DoH 查询失败，回退到二进制 DNS: %v", err)
		return p.queryDoH(domain, dnsServer)
	}
	return p.queryDNSUDP(domain, dnsServer)
}

func (p *ECHPool) queryDNSUDP(domain, dnsServer string) (string, error) {
	if !strings.Contains(dnsServer, ":") {
		dnsServer = dnsServer + ":53"
	}
	query := p.buildDNSQuery(domain, 65)
	conn, err := net.Dial("udp", dnsServer)
	if err != nil {
		return "", fmt.Errorf("连接 DNS 服务器失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = conn.Write(query); err != nil {
		return "", fmt.Errorf("发送查询失败: %v", err)
	}
	response := make([]byte, 4096)
	n, err := conn.Read(response)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return "", fmt.Errorf("DNS 查询超时")
		}
		return "", fmt.Errorf("读取 DNS 响应失败: %v", err)
	}
	return p.parseDNSResponse(response[:n])
}

// queryDoHJSON 使用 JSON 格式 DoH API 查询（Cloudflare/Google 等支持）
func (p *ECHPool) queryDoHJSON(domain, dohURL string) (string, error) {
	apiURL := fmt.Sprintf("%s?name=%s&type=65", dohURL, url.QueryEscape(domain))
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/dns-json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DoH JSON 状态码: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return p.parseDoHJSONResponse(body)
}

// parseDoHJSONResponse 解析 JSON 格式的 DoH 响应
func (p *ECHPool) parseDoHJSONResponse(data []byte) (string, error) {
	var result struct {
		Answer []struct {
			Name string `json:"name"`
			Type int    `json:"type"`
			TTL  int    `json:"TTL"`
			Data string `json:"data"`
		} `json:"Answer"`
		Status int `json:"Status"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("JSON 解析失败: %v", err)
	}
	if result.Status != 0 {
		return "", fmt.Errorf("DNS 响应状态码: %d", result.Status)
	}
	for _, ans := range result.Answer {
		if ans.Type == 65 {
			if ech := p.extractECHFromHTTPSData(ans.Data); ech != "" {
				return ech, nil
			}
		}
	}
	return "", nil
}

// extractECHFromHTTPSData 从 HTTPS 记录的 data 字段提取 ECH 配置
func (p *ECHPool) extractECHFromHTTPSData(data string) string {
	// data 格式示例: "1 . alpn=\"h3,h2\" ipv4hint=... ech=AEX+DQBB..."
	for _, part := range strings.Fields(data) {
		if strings.HasPrefix(part, "ech=") {
			return strings.TrimPrefix(part, "ech=")
		}
	}
	return ""
}

func (p *ECHPool) queryDoH(domain, dohURL string) (string, error) {
	u, err := url.Parse(dohURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	dnsQuery := p.buildDNSQuery(domain, 65)
	dnsBase64 := base64.RawURLEncoding.EncodeToString(dnsQuery)
	q.Set("dns", dnsBase64)
	u.RawQuery = q.Encode()
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("Content-Type", "application/dns-message")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DoH 状态码: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return p.parseDNSResponse(body)
}

func (p *ECHPool) buildDNSQuery(domain string, qtype uint16) []byte {
	query := make([]byte, 0, 512)
	query = append(query, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	for _, label := range strings.Split(domain, ".") {
		query = append(query, byte(len(label)))
		query = append(query, []byte(label)...)
	}
	query = append(query, 0x00)
	query = append(query, byte(qtype>>8), byte(qtype), 0x00, 0x01)
	return query
}

func (p *ECHPool) parseDNSResponse(response []byte) (string, error) {
	if len(response) < 12 {
		return "", fmt.Errorf("响应过短")
	}
	ancount := binary.BigEndian.Uint16(response[6:8])
	if ancount == 0 {
		return "", fmt.Errorf("无答案记录")
	}
	offset := 12
	for offset < len(response) && response[offset] != 0 {
		offset += int(response[offset]) + 1
	}
	offset += 5
	for i := 0; i < int(ancount); i++ {
		if offset >= len(response) {
			break
		}
		if response[offset]&0xC0 == 0xC0 {
			offset += 2
		} else {
			for offset < len(response) && response[offset] != 0 {
				offset += int(response[offset]) + 1
			}
			offset++
		}
		if offset+10 > len(response) {
			break
		}
		rrType := binary.BigEndian.Uint16(response[offset : offset+2])
		offset += 8
		dataLen := binary.BigEndian.Uint16(response[offset : offset+2])
		offset += 2
		if offset+int(dataLen) > len(response) {
			break
		}
		data := response[offset : offset+int(dataLen)]
		offset += int(dataLen)
		if rrType == 65 {
			if ech := p.parseHTTPSRecord(data); ech != "" {
				return ech, nil
			}
		}
	}
	return "", nil
}

func (p *ECHPool) parseHTTPSRecord(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	offset := 2
	if offset < len(data) && data[offset] == 0 {
		offset++
	} else {
		for offset < len(data) && data[offset] != 0 {
			offset += int(data[offset]) + 1
		}
		offset++
	}
	for offset+4 <= len(data) {
		key := binary.BigEndian.Uint16(data[offset : offset+2])
		length := binary.BigEndian.Uint16(data[offset+2 : offset+4])
		offset += 4
		if offset+int(length) > len(data) {
			break
		}
		value := data[offset : offset+int(length)]
		offset += int(length)
		if key == 0x00 {
			if len(value) > 0 {
				return string(value)
			}
		}
		_ = key
	}
	return ""
}

// UUID 生成
func genUUID() string {
	return uuid.NewString()
}
