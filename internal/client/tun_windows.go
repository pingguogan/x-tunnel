//go:build windows

package client

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"

	"github.com/x-tunnel/internal/config"
	"github.com/x-tunnel/internal/protocol"
	"golang.zx2c4.com/wireguard/tun"
)

// TUNDevice TUN 虚拟网卡设备
type TUNDevice struct {
	pool   *ECHPool
	cfg    config.TUNConfig
	dev    tun.Device
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewTUNDevice 创建 TUN 设备
func NewTUNDevice(pool *ECHPool, cfg config.TUNConfig) (*TUNDevice, error) {
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = 1420
	}

	dev, err := tun.CreateTUN(cfg.Name, mtu)
	if err != nil {
		return nil, fmt.Errorf("创建 TUN 设备失败: %w", err)
	}

	name, _ := dev.Name()
	log.Printf("[TUN] TUN 设备已创建: %s", name)

	return &TUNDevice{
		pool:   pool,
		cfg:    cfg,
		dev:    dev,
		stopCh: make(chan struct{}),
	}, nil
}

// Start 启动 TUN 设备
func (t *TUNDevice) Start() error {
	log.Printf("[TUN] 启动 TUN 设备: %s, 子网: %s, MTU: %d", t.cfg.Name, t.cfg.Subnet, t.cfg.MTU)
	log.Printf("[TUN] 模式: %s, DNS: %v", t.cfg.Mode, t.cfg.DNS)

	// 配置 TUN 接口 IP 地址
	if err := t.configureInterface(); err != nil {
		log.Printf("[TUN] 配置接口失败: %v", err)
	}

	// 启动数据包处理
	t.wg.Add(1)
	go t.processPackets()

	log.Printf("[TUN] TUN 设备已启动")
	return nil
}

// Stop 停止 TUN 设备
func (t *TUNDevice) Stop() {
	log.Printf("[TUN] 停止 TUN 设备...")
	close(t.stopCh)
	t.dev.Close()
	t.wg.Wait()
	log.Printf("[TUN] TUN 设备已停止")
}

// configureInterface 配置 TUN 接口
func (t *TUNDevice) configureInterface() error {
	ip, ipNet, err := net.ParseCIDR(t.cfg.Subnet)
	if err != nil {
		return fmt.Errorf("解析子网失败: %w", err)
	}

	mask := net.IP(ipNet.Mask).String()

	// 等待接口就绪
	name, _ := t.dev.Name()

	// 配置 IP 地址
	cmd := exec.Command("netsh", "interface", "ip", "set", "address", name, "static", ip.String(), mask)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[TUN] 配置 IP 失败: %v, output: %s", err, string(output))
	} else {
		log.Printf("[TUN] 已配置 IP: %s/%s", ip.String(), mask)
	}

	// 配置 DNS
	for _, dns := range t.cfg.DNS {
		cmd = exec.Command("netsh", "interface", "ip", "add", "dns", name, dns, "index=1")
		output, err = cmd.CombinedOutput()
		if err != nil {
			log.Printf("[TUN] 配置 DNS 失败: %v", err)
		} else {
			log.Printf("[TUN] 已配置 DNS: %s", dns)
		}
	}

	// 启用接口
	cmd = exec.Command("netsh", "interface", "set", "interface", name, "admin=enable")
	cmd.Run()

	return nil
}

// processPackets 处理 TUN 接口的数据包
func (t *TUNDevice) processPackets() {
	defer t.wg.Done()

	buf := make([]byte, 65535)
	offset := 4 // tun 包头偏移

	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		sizes := []int{0}
		bufs := [][]byte{buf}
		_, err := t.dev.Read(bufs, sizes, offset)
		if err != nil {
			select {
			case <-t.stopCh:
				return
			default:
				log.Printf("[TUN] 读取数据包失败: %v", err)
				continue
			}
		}

		n := sizes[0]
		if n < 20 {
			continue
		}

		packet := buf[offset : offset+n]
		go t.handlePacket(packet)
	}
}

// handlePacket 处理单个 IP 数据包
func (t *TUNDevice) handlePacket(packet []byte) {
	if len(packet) < 20 {
		return
	}

	version := packet[0] >> 4
	if version != 4 {
		return
	}

	protocolNum := packet[9]
	srcIP := net.IP(packet[12:16])
	dstIP := net.IP(packet[16:20])

	var srcPort, dstPort uint16
	if protocolNum == 6 || protocolNum == 17 {
		if len(packet) >= 24 {
			srcPort = uint16(packet[20])<<8 | uint16(packet[21])
			dstPort = uint16(packet[22])<<8 | uint16(packet[23])
		}
	}

	_ = srcIP
	_ = srcPort

	if protocolNum == 17 && dstPort == 53 {
		t.handleDNS(packet)
		return
	}

	if protocolNum == 6 {
		t.handleTCP(srcIP, srcPort, dstIP, dstPort, packet)
		return
	}

	if protocolNum == 17 {
		t.handleUDP(srcIP, srcPort, dstIP, dstPort, packet)
		return
	}
}

// handleTCP 处理 TCP 连接
func (t *TUNDevice) handleTCP(srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16, packet []byte) {
	target := fmt.Sprintf("%s:%d", dstIP.String(), dstPort)

	if t.cfg.Mode == "rule" && t.shouldDirect(target) {
		log.Printf("[TUN] TCP 直连: %s", target)
		return
	}

	log.Printf("[TUN] TCP 代理: %s", target)

	ipStrategy := protocol.IPStrategyDefault
	stream, _, _, err := t.pool.OpenTCPStream(target, ipStrategy)
	if err != nil {
		log.Printf("[TUN] 打开 TCP 流失败: %v", err)
		return
	}
	defer stream.Close()

	log.Printf("[TUN] TCP 连接已建立: %s", target)
}

// handleUDP 处理 UDP 数据包
func (t *TUNDevice) handleUDP(srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16, packet []byte) {
	target := fmt.Sprintf("%s:%d", dstIP.String(), dstPort)

	if t.cfg.Mode == "rule" && t.shouldDirect(target) {
		log.Printf("[TUN] UDP 直连: %s", target)
		return
	}

	log.Printf("[TUN] UDP 代理: %s", target)

	if len(packet) < 28 {
		return
	}
	udpPayload := packet[28:]

	ipStrategy := protocol.IPStrategyDefault
	stream, _, _, err := t.pool.OpenUDPStream(target, ipStrategy)
	if err != nil {
		log.Printf("[TUN] 打开 UDP 流失败: %v", err)
		return
	}
	defer stream.Close()

	if err := protocol.WriteChunk(stream, udpPayload); err != nil {
		log.Printf("[TUN] UDP 写入失败: %v", err)
		return
	}

	reply, err := protocol.ReadChunk(stream)
	if err != nil {
		return
	}

	log.Printf("[TUN] UDP 响应: %d 字节", len(reply))
}

// handleDNS 处理 DNS 查询
func (t *TUNDevice) handleDNS(packet []byte) {
	if len(packet) < 28 {
		return
	}
	dnsData := packet[28:]

	var dnsServer string
	if len(t.cfg.DNS) > 0 {
		dnsServer = t.cfg.DNS[0]
	} else {
		dnsServer = "1.1.1.1"
	}

	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP(dnsServer),
		Port: 53,
	})
	if err != nil {
		log.Printf("[TUN] DNS 连接失败: %v", err)
		return
	}
	defer conn.Close()

	_, err = conn.Write(dnsData)
	if err != nil {
		log.Printf("[TUN] DNS 发送失败: %v", err)
		return
	}

	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		log.Printf("[TUN] DNS 读取失败: %v", err)
		return
	}

	log.Printf("[TUN] DNS 响应: %d 字节", n)
}

// shouldDirect 判断是否应该直连
func (t *TUNDevice) shouldDirect(target string) bool {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}

	for _, rule := range t.cfg.Rules {
		switch rule.Type {
		case "cidr":
			_, cidr, err := net.ParseCIDR(rule.Value)
			if err != nil {
				continue
			}
			ip := net.ParseIP(host)
			if ip != nil && cidr.Contains(ip) {
				return rule.Action == "direct"
			}
		case "domain":
			if strings.EqualFold(host, rule.Value) {
				return rule.Action == "direct"
			}
		case "domain_suffix":
			if strings.HasSuffix(strings.ToLower(host), strings.ToLower(rule.Value)) {
				return rule.Action == "direct"
			}
		}
	}

	return false
}
