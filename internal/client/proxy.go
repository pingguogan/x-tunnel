package client

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xtaci/smux"

	"github.com/x-tunnel/internal/protocol"
)

// ProxyConfig 代理配置
type ProxyConfig struct {
	Username, Password, Host string
}

// Proxy 代理服务器
type Proxy struct {
	pool        *ECHPool
	ipStrategy  byte
	blockPorts  map[int]struct{}
	stopCh      chan struct{}
	listeners   []net.Listener
	mu          sync.Mutex
}

// NewProxy 创建代理
func NewProxy(pool *ECHPool, ipStrategy byte, blockPorts map[int]struct{}) *Proxy {
	return &Proxy{
		pool:       pool,
		ipStrategy: ipStrategy,
		blockPorts: blockPorts,
		stopCh:     make(chan struct{}),
	}
}

// Stop 停止代理服务器
func (p *Proxy) Stop() {
	close(p.stopCh)

	p.mu.Lock()
	defer p.mu.Unlock()

	// 关闭所有监听器
	for _, l := range p.listeners {
		if l != nil {
			l.Close()
		}
	}
	p.listeners = nil

	log.Printf("[客户端] 代理已停止")
}

// RunTCPListener 运行 TCP 转发器
func (p *Proxy) RunTCPListener(rule string) {
	rule = strings.TrimPrefix(rule, "tcp://")
	parts := strings.Split(rule, "/")
	if len(parts) != 2 {
		return
	}
	lAddr, tAddr := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	l, err := net.Listen("tcp", lAddr)
	if err != nil {
		log.Printf("[客户端] TCP监听失败: %v", err)
		return
	}

	// 注册监听器
	p.mu.Lock()
	p.listeners = append(p.listeners, l)
	p.mu.Unlock()

	log.Printf("[客户端] TCP转发: %s -> %s", lAddr, tAddr)
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		c, err := l.Accept()
		if err != nil {
			select {
			case <-p.stopCh:
				return
			default:
				continue
			}
		}
		go p.handleLocalTCP(c, tAddr)
	}
}

func (p *Proxy) handleLocalTCP(c net.Conn, target string) {
	stream, _, decision, err := p.pool.OpenTCPStream(target, p.ipStrategy)
	if err != nil {
		_ = c.Close()
		return
	}
	logClientConnEvent(c, "TCP转发", target, decision, true)
	defer logClientConnEvent(c, "TCP转发", target, decision, false)
	protocol.ProxyConnStream(c, stream)
}

// RunSOCKS5Listener 运行 SOCKS5 代理
func (p *Proxy) RunSOCKS5Listener(addr string) {
	h, u, pass, err := parseAuthAndAddr(strings.TrimPrefix(addr, "socks5://"))
	if err != nil {
		log.Printf("[客户端] SOCKS5地址解析失败: %v", err)
		return
	}
	l, err := net.Listen("tcp", h)
	if err != nil {
		log.Printf("[客户端] SOCKS5监听失败: %v", err)
		return
	}

	// 注册监听器
	p.mu.Lock()
	p.listeners = append(p.listeners, l)
	p.mu.Unlock()

	log.Printf("[客户端] SOCKS5 代理: %s", h)
	cfgp := &ProxyConfig{u, pass, h}
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		c, err := l.Accept()
		if err != nil {
			select {
			case <-p.stopCh:
				return
			default:
				continue
			}
		}
		go p.handleSOCKS5(c, cfgp)
	}
}

func (p *Proxy) handleSOCKS5(c net.Conn, cfgp *ProxyConfig) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil || buf[0] != 0x05 {
		return
	}
	methods := make([]byte, buf[1])
	_, _ = io.ReadFull(c, methods)
	if cfgp.Username != "" {
		_, _ = c.Write([]byte{0x05, 0x02})
		if err := handleSOCKS5UserPassAuth(c, cfgp); err != nil {
			return
		}
	} else {
		_, _ = c.Write([]byte{0x05, 0x00})
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	var target string
	switch head[3] {
	case 0x01:
		b := make([]byte, 4)
		_, _ = io.ReadFull(c, b)
		target = net.IP(b).String()
	case 0x03:
		b := make([]byte, 1)
		_, _ = io.ReadFull(c, b)
		addr := make([]byte, b[0])
		_, _ = io.ReadFull(c, addr)
		target = string(addr)
	case 0x04:
		b := make([]byte, 16)
		_, _ = io.ReadFull(c, b)
		target = net.IP(b).String()
	}
	pb := make([]byte, 2)
	_, _ = io.ReadFull(c, pb)
	port := int(pb[0])<<8 | int(pb[1])
	target = net.JoinHostPort(target, fmt.Sprintf("%d", port))

	host, _, _ := net.SplitHostPort(target)
	ip := net.ParseIP(host)

	if head[1] == 0x01 {
		if p.ipStrategy == protocol.IPStrategyIPv4Only {
			if head[3] == 0x04 || (ip != nil && ip.To4() == nil) {
				_, _ = c.Write([]byte{0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				return
			}
		}
		if p.ipStrategy == protocol.IPStrategyIPv6Only {
			if head[3] == 0x01 || (ip != nil && ip.To4() != nil) {
				_, _ = c.Write([]byte{0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				return
			}
		}
	}

	_ = c.SetDeadline(time.Time{})

	switch head[1] {
	case 0x01:
		p.handleSOCKS5Connect(c, target)
	case 0x03:
		p.handleSOCKS5UDP(c, cfgp)
	}
}

func handleSOCKS5UserPassAuth(c net.Conn, cfgp *ProxyConfig) error {
	b := make([]byte, 2)
	_, _ = io.ReadFull(c, b)
	u := make([]byte, b[1])
	_, _ = io.ReadFull(c, u)
	_, _ = io.ReadFull(c, b[:1])
	pass := make([]byte, b[0])
	_, _ = io.ReadFull(c, pass)
	if string(u) == cfgp.Username && string(pass) == cfgp.Password {
		_, _ = c.Write([]byte{0x01, 0x00})
		return nil
	}
	_, _ = c.Write([]byte{0x01, 0x01})
	return errors.New("认证失败")
}

func (p *Proxy) handleSOCKS5Connect(c net.Conn, target string) {
	_, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	if err != nil {
		_ = c.Close()
		return
	}
	stream, _, decision, err := p.pool.OpenTCPStream(target, p.ipStrategy)
	if err != nil {
		_ = c.Close()
		return
	}
	logClientConnEvent(c, "SOCKS5", target, decision, true)
	defer logClientConnEvent(c, "SOCKS5", target, decision, false)
	protocol.ProxyConnStream(c, stream)
}

func (p *Proxy) handleSOCKS5UDP(c net.Conn, cfgp *ProxyConfig) {
	host, _, _ := net.SplitHostPort(cfgp.Host)
	uAddr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(host, "0"))
	ul, err := net.ListenUDP("udp", uAddr)
	if err != nil {
		_ = c.Close()
		return
	}
	defer ul.Close()

	actual, ok := ul.LocalAddr().(*net.UDPAddr)
	if !ok || actual == nil {
		_ = c.Close()
		return
	}
	resp := []byte{0x05, 0x00, 0x00}
	if ip4 := actual.IP.To4(); ip4 != nil {
		resp = append(resp, 0x01)
		resp = append(resp, ip4...)
	} else {
		resp = append(resp, 0x04)
		resp = append(resp, actual.IP...)
	}
	resp = append(resp, byte(actual.Port>>8), byte(actual.Port))
	if _, err := c.Write(resp); err != nil {
		_ = c.Close()
		return
	}

	assoc := &UDPAssociation{
		tcpConn:     c,
		udpListener: ul,
		pool:        p.pool,
		ipStrategy:  p.ipStrategy,
		blockPorts:  p.blockPorts,
		channelID:   -1,
	}

	go assoc.loop()
	b := make([]byte, 1)
	for {
		if _, err := c.Read(b); err != nil {
			assoc.Close()
			return
		}
	}
}

// UDPAssociation UDP 关联
type UDPAssociation struct {
	tcpConn       net.Conn
	udpListener   *net.UDPConn
	clientUDPAddr *net.UDPAddr
	pool          *ECHPool
	ipStrategy    byte
	blockPorts    map[int]struct{}

	mu        sync.Mutex
	closed    bool
	receiving bool
	channelID int
	target    string
	stream    *smux.Stream
}

func (a *UDPAssociation) loop() {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := a.udpListener.ReadFromUDP(buf)
		if err != nil {
			return
		}
		a.mu.Lock()
		if a.clientUDPAddr == nil {
			a.clientUDPAddr = addr
		} else if a.clientUDPAddr.String() != addr.String() {
			a.mu.Unlock()
			continue
		}
		a.mu.Unlock()

		tgt, data, err := parseSOCKS5UDPPacket(buf[:n])
		if err == nil {
			h, ps, _ := net.SplitHostPort(tgt)
			if ip := net.ParseIP(h); ip != nil {
				if a.ipStrategy == protocol.IPStrategyIPv4Only && ip.To4() == nil {
					continue
				}
				if a.ipStrategy == protocol.IPStrategyIPv6Only && ip.To4() != nil {
					continue
				}
			}
			var prt int
			_, _ = fmt.Sscanf(ps, "%d", &prt)
			if _, ok := a.blockPorts[prt]; ok {
				continue
			}
			a.send(tgt, data)
		}
	}
}

func (a *UDPAssociation) send(target string, data []byte) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	needStart := !a.receiving
	if needStart {
		a.receiving = true
		a.target = target
	}
	stream := a.stream
	a.mu.Unlock()

	if needStart {
		s, id, decision, err := a.pool.OpenUDPStream(target, a.ipStrategy)
		if err != nil {
			a.Close()
			return
		}
		a.mu.Lock()
		a.stream = s
		a.channelID = id
		stream = s
		a.mu.Unlock()
		logClientConnEvent(a.tcpConn, "SOCKS5-UDP", target, decision, true)
		go func() {
			for {
				addrStr, payload, e := protocol.ReadUDPReply(s)
				if e != nil {
					a.Close()
					return
				}
				a.handleUDPResponse(addrStr, payload)
			}
		}()
	} else {
		if target != "" && target != a.target {
			a.mu.Lock()
			a.target = target
			a.mu.Unlock()
		}
	}
	if stream == nil {
		a.Close()
		return
	}
	if err := protocol.WriteChunk(stream, data); err != nil {
		a.Close()
	}
}

func (a *UDPAssociation) handleUDPResponse(addrStr string, data []byte) {
	host, portStr, _ := net.SplitHostPort(addrStr)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)
	pkt, _ := buildSOCKS5UDPPacket(host, port, data)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clientUDPAddr != nil {
		_, _ = a.udpListener.WriteToUDP(pkt, a.clientUDPAddr)
	}
}

func (a *UDPAssociation) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	stream := a.stream
	a.closed = true
	a.stream = nil
	a.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	_ = a.udpListener.Close()
	if a.tcpConn != nil {
		_ = a.tcpConn.Close()
	}
}

// RunHTTPListener 运行 HTTP 代理
func (p *Proxy) RunHTTPListener(addr string) {
	h, u, pass, _ := parseAuthAndAddr(strings.TrimPrefix(addr, "http://"))
	l, err := net.Listen("tcp", h)
	if err != nil {
		log.Printf("[客户端] HTTP监听失败: %v", err)
		return
	}

	// 注册监听器
	p.mu.Lock()
	p.listeners = append(p.listeners, l)
	p.mu.Unlock()

	log.Printf("[客户端] HTTP 代理: %s", h)
	cfgp := &ProxyConfig{u, pass, h}
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		c, err := l.Accept()
		if err != nil {
			select {
			case <-p.stopCh:
				return
			default:
				continue
			}
		}
		go p.handleHTTP(c, cfgp)
	}
}

func (p *Proxy) handleHTTP(c net.Conn, cfgp *ProxyConfig) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	if cfgp.Username != "" {
		auth := req.Header.Get("Proxy-Authorization")
		ok := false
		if strings.HasPrefix(auth, "Basic ") {
			decoded, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
			pair := strings.SplitN(string(decoded), ":", 2)
			if len(pair) == 2 && pair[0] == cfgp.Username && pair[1] == cfgp.Password {
				ok = true
			}
		}
		if !ok {
			_, _ = c.Write([]byte("HTTP/1.1 407 需要认证\r\nProxy-Authenticate: Basic realm=\"代理\"\r\n\r\n"))
			return
		}
	}

	target := req.Host
	if !strings.Contains(target, ":") {
		if req.Method == "CONNECT" {
			target += ":443"
		} else {
			target += ":80"
		}
	}

	var first []byte
	if req.Method == "CONNECT" {
		_, _ = c.Write([]byte("HTTP/1.1 200 连接已建立\r\n\r\n"))
	} else {
		req.RequestURI = ""
		req.URL.Scheme = ""
		req.URL.Host = ""
		var buf bytes.Buffer
		_ = req.Write(&buf)
		first = buf.Bytes()
	}

	stream, _, decision, err := p.pool.OpenTCPStream(target, p.ipStrategy)
	if err != nil {
		return
	}
	if len(first) > 0 {
		if _, err := stream.Write(first); err != nil {
			_ = stream.Close()
			return
		}
	}
	logClientConnEvent(c, "HTTP", target, decision, true)
	defer logClientConnEvent(c, "HTTP", target, decision, false)
	protocol.ProxyConnStream(c, stream)
}

// 辅助函数
func parseAuthAndAddr(full string) (string, string, string, error) {
	u, p, h := "", "", full
	if strings.Contains(full, "@") {
		parts := strings.SplitN(full, "@", 2)
		if len(parts) != 2 {
			return "", "", "", fmt.Errorf("格式错误")
		}
		auth := parts[0]
		if strings.Contains(auth, ":") {
			ap := strings.SplitN(auth, ":", 2)
			u, p = ap[0], ap[1]
		}
		h = parts[1]
	}
	return h, u, p, nil
}

func parseSOCKS5UDPPacket(b []byte) (string, []byte, error) {
	if len(b) < 10 || b[2] != 0 {
		return "", nil, errors.New("数据不合法")
	}
	off := 4
	var h string
	switch b[3] {
	case 0x01:
		if off+4 > len(b) {
			return "", nil, errors.New("IPv4地址长度过短")
		}
		h = net.IP(b[off : off+4]).String()
		off += 4
	case 0x03:
		if off+1 > len(b) {
			return "", nil, errors.New("域名长度不足")
		}
		l := int(b[off])
		off++
		if off+l > len(b) {
			return "", nil, errors.New("域名长度不足")
		}
		h = string(b[off : off+l])
		off += l
	case 0x04:
		if off+16 > len(b) {
			return "", nil, errors.New("IPv6地址长度过短")
		}
		h = net.IP(b[off : off+16]).String()
		off += 16
	default:
		return "", nil, errors.New("地址类型无效")
	}
	if off+2 > len(b) {
		return "", nil, errors.New("端口字段过短")
	}
	port := int(b[off])<<8 | int(b[off+1])
	off += 2
	t := fmt.Sprintf("%s:%d", h, port)
	if b[3] == 0x04 {
		t = fmt.Sprintf("[%s]:%d", h, port)
	}
	return t, b[off:], nil
}

func buildSOCKS5UDPPacket(h string, p int, d []byte) ([]byte, error) {
	buf := []byte{0, 0, 0}
	ip := net.ParseIP(h)
	if ip4 := ip.To4(); ip4 != nil {
		buf = append(buf, 0x01)
		buf = append(buf, ip4...)
	} else if ip != nil {
		buf = append(buf, 0x04)
		buf = append(buf, ip...)
	} else {
		buf = append(buf, 0x03, byte(len(h)))
		buf = append(buf, h...)
	}
	buf = append(buf, byte(p>>8), byte(p))
	buf = append(buf, d...)
	return buf, nil
}

func logClientConnEvent(c net.Conn, reqType, target string, chID int, opened bool) {
	arrow := "关闭"
	if opened {
		arrow = "打开"
	}
	remoteAddr := "-"
	if ra := c.RemoteAddr(); ra != nil {
		remoteAddr = ra.String()
	}
	log.Printf("[客户端] %s %s %s %s 通道 %d", remoteAddr, reqType, arrow, target, chID)
}
