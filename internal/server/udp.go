package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/x-tunnel/internal/protocol"
)

// UDPRelayer UDP 中继接口
type UDPRelayer interface {
	Read(buffer []byte) (int, *net.UDPAddr, error)
	Write(data []byte) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// DirectUDPRelayer 直连 UDP 中继
type DirectUDPRelayer struct {
	conn   *net.UDPConn
	target *net.UDPAddr
}

func (d *DirectUDPRelayer) Read(buffer []byte) (int, *net.UDPAddr, error) {
	return d.conn.ReadFromUDP(buffer)
}
func (d *DirectUDPRelayer) Write(data []byte) (int, error)    { return d.conn.WriteToUDP(data, d.target) }
func (d *DirectUDPRelayer) SetReadDeadline(t time.Time) error { return d.conn.SetReadDeadline(t) }
func (d *DirectUDPRelayer) Close() error                      { return d.conn.Close() }

// SOCKS5UDPRelay SOCKS5 UDP 中继
type SOCKS5UDPRelay struct {
	tcpConn    net.Conn
	udpConn    *net.UDPConn
	relayAddr  *net.UDPAddr
	targetAddr *net.UDPAddr
	mu         sync.Mutex
	closed     bool
}

// NewSOCKS5UDPRelay 创建 SOCKS5 UDP 中继
func NewSOCKS5UDPRelay(targetAddr string, socks5Cfg *SOCKS5Config, timeout time.Duration) (*SOCKS5UDPRelay, error) {
	if socks5Cfg == nil {
		return nil, errors.New("SOCKS5配置为空")
	}
	tcpConn, err := net.DialTimeout("tcp", socks5Cfg.Host, timeout)
	if err != nil {
		return nil, err
	}
	if err := socks5Handshake(tcpConn, socks5Cfg); err != nil {
		tcpConn.Close()
		return nil, err
	}
	req := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if _, err := tcpConn.Write(req); err != nil {
		tcpConn.Close()
		return nil, err
	}
	resp := make([]byte, 4)
	if _, err := io.ReadFull(tcpConn, resp); err != nil {
		tcpConn.Close()
		return nil, err
	}
	if resp[1] != 0x00 {
		tcpConn.Close()
		return nil, fmt.Errorf("UDP ASSOCIATE拒绝: %d", resp[1])
	}
	var relayHost string
	switch resp[3] {
	case 0x01:
		ipBuf := make([]byte, 4)
		if _, err := io.ReadFull(tcpConn, ipBuf); err != nil {
			tcpConn.Close()
			return nil, err
		}
		relayHost = net.IP(ipBuf).String()
	case 0x03:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(tcpConn, lenBuf); err != nil {
			tcpConn.Close()
			return nil, err
		}
		domainBuf := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(tcpConn, domainBuf); err != nil {
			tcpConn.Close()
			return nil, err
		}
		relayHost = string(domainBuf)
	case 0x04:
		ipBuf := make([]byte, 16)
		if _, err := io.ReadFull(tcpConn, ipBuf); err != nil {
			tcpConn.Close()
			return nil, err
		}
		relayHost = net.IP(ipBuf).String()
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(tcpConn, portBuf); err != nil {
		tcpConn.Close()
		return nil, err
	}
	relayPort := int(portBuf[0])<<8 | int(portBuf[1])

	if relayHost == "0.0.0.0" || relayHost == "::" {
		h, _, _ := net.SplitHostPort(socks5Cfg.Host)
		relayHost = h
	}
	rAddr, errResolve := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", relayHost, relayPort))
	if errResolve != nil {
		tcpConn.Close()
		return nil, errResolve
	}

	tAddr, errResolve := net.ResolveUDPAddr("udp", targetAddr)
	if errResolve != nil {
		tcpConn.Close()
		return nil, errResolve
	}

	localUDP, errListen := net.ListenUDP("udp", nil)
	if errListen != nil {
		tcpConn.Close()
		return nil, errListen
	}

	log.Printf("[服务端UDP] SOCKS5 UDP中继: %s -> %s", rAddr, targetAddr)
	return &SOCKS5UDPRelay{
		tcpConn:    tcpConn,
		udpConn:    localUDP,
		relayAddr:  rAddr,
		targetAddr: tAddr,
	}, nil
}

func (r *SOCKS5UDPRelay) Write(data []byte) (int, error) {
	if r == nil || r.udpConn == nil || r.relayAddr == nil || r.targetAddr == nil {
		return 0, errors.New("SOCKS5 UDP relay 未初始化")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errors.New("closed")
	}
	pkt := buildSOCKS5UDPPacketData(r.targetAddr, data)
	return r.udpConn.WriteToUDP(pkt, r.relayAddr)
}

func (r *SOCKS5UDPRelay) Read(buffer []byte) (int, *net.UDPAddr, error) {
	if r == nil || r.udpConn == nil {
		return 0, nil, errors.New("SOCKS5 UDP relay 未初始化")
	}
	if r.closed {
		return 0, nil, errors.New("closed")
	}

	tmp := make([]byte, 64*1024)
	n, _, err := r.udpConn.ReadFromUDP(tmp)
	if err != nil {
		return 0, nil, err
	}
	srcAddr, payload, err := parseSOCKS5UDPResp(tmp[:n])
	if err != nil {
		return 0, nil, err
	}
	copy(buffer, payload)
	return len(payload), srcAddr, nil
}

func (r *SOCKS5UDPRelay) SetReadDeadline(t time.Time) error {
	if r == nil || r.udpConn == nil {
		return errors.New("SOCKS5 UDP relay 未初始化")
	}
	return r.udpConn.SetReadDeadline(t)
}

func (r *SOCKS5UDPRelay) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	_ = r.udpConn.Close()
	_ = r.tcpConn.Close()
	return nil
}

func buildSOCKS5UDPPacketData(target *net.UDPAddr, data []byte) []byte {
	packet := []byte{0x00, 0x00, 0x00}
	if ip4 := target.IP.To4(); ip4 != nil {
		packet = append(packet, 0x01)
		packet = append(packet, ip4...)
	} else {
		packet = append(packet, 0x04)
		packet = append(packet, target.IP...)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(target.Port))
	packet = append(packet, portBytes...)
	packet = append(packet, data...)
	return packet
}

func parseSOCKS5UDPResp(packet []byte) (*net.UDPAddr, []byte, error) {
	if len(packet) < 10 {
		return nil, nil, fmt.Errorf("数据包过短")
	}
	atyp := packet[3]
	offset := 4
	var host string
	switch atyp {
	case 0x01:
		if offset+4 > len(packet) {
			return nil, nil, fmt.Errorf("IPv4地址长度过短")
		}
		host = net.IP(packet[offset : offset+4]).String()
		offset += 4
	case 0x03:
		if offset+1 > len(packet) {
			return nil, nil, fmt.Errorf("域名长度字段过短")
		}
		l := int(packet[offset])
		offset++
		if offset+l > len(packet) {
			return nil, nil, fmt.Errorf("域名长度不足")
		}
		host = string(packet[offset : offset+l])
		offset += l
	case 0x04:
		if offset+16 > len(packet) {
			return nil, nil, fmt.Errorf("IPv6地址长度过短")
		}
		host = net.IP(packet[offset : offset+16]).String()
		offset += 16
	default:
		return nil, nil, fmt.Errorf("地址类型无效: %d", atyp)
	}
	if offset+2 > len(packet) {
		return nil, nil, fmt.Errorf("端口字段过短")
	}
	port := int(packet[offset])<<8 | int(packet[offset+1])
	offset += 2
	addr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
	if addr == nil {
		return nil, nil, fmt.Errorf("解析地址失败")
	}
	return addr, packet[offset:], nil
}

// HandleUDPRelay 处理 UDP 中继
func HandleUDPRelay(stream io.ReadWriteCloser, session *ClientSession, ch *WSChannel, target string, strategy byte, socks5Cfg *SOCKS5Config, dialTimeout time.Duration) {
	var relay UDPRelayer
	var err error
	if socks5Cfg != nil {
		var socksRelay *SOCKS5UDPRelay
		socksRelay, err = NewSOCKS5UDPRelay(target, socks5Cfg, dialTimeout)
		if err != nil {
			log.Printf("[服务端] 客户ID:%s SOCKS5 UDP中继创建失败: %v, 通道:%d", shortID(session.clientID), err, ch.id)
			return
		}
		relay = socksRelay
	} else {
		addr, errResolve := ResolveUDPWithStrategy(target, strategy, dialTimeout)
		if errResolve != nil {
			return
		}
		udpConn, errListen := net.ListenUDP("udp", nil)
		if errListen != nil {
			return
		}
		relay = &DirectUDPRelayer{conn: udpConn, target: addr}
	}
	if relay == nil {
		return
	}
	defer relay.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			packet, e := protocol.ReadChunk(stream)
			if e != nil {
				return
			}
			if len(packet) == 0 {
				continue
			}
			if _, e = relay.Write(packet); e != nil {
				return
			}
		}
	}()
	buf := make([]byte, 64*1024)
	for {
		_ = relay.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, e := relay.Read(buf)
		if e != nil {
			if netErr, ok := e.(net.Error); ok && netErr.Timeout() {
				select {
				case <-done:
					return
				default:
					continue
				}
			}
			return
		}
		if err := protocol.WriteUDPReply(stream, addr.String(), buf[:n]); err != nil {
			return
		}
	}
}
