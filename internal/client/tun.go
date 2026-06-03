package client

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/songgao/water"
	"github.com/x-tunnel/internal/config"
	"github.com/x-tunnel/internal/protocol"
)

// TUNDevice TUN 虚拟网卡设备
type TUNDevice struct {
	pool   *ECHPool
	cfg    config.TUNConfig
	iface  *water.Interface
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewTUNDevice 创建 TUN 设备
func NewTUNDevice(pool *ECHPool, cfg config.TUNConfig) (*TUNDevice, error) {
	// 创建 TUN 设备
	config := water.Config{
		DeviceType: water.TUN,
	}
	config.Name = cfg.Name

	iface, err := water.New(config)
	if err != nil {
		return nil, fmt.Errorf("创建 TUN 设备失败: %w", err)
	}

	log.Printf("[TUN] TUN 设备已创建: %s", iface.Name())

	return &TUNDevice{
		pool:   pool,
		cfg:    cfg,
		iface:  iface,
		stopCh: make(chan struct{}),
	}, nil
}

// Start 启动 TUN 设备
func (t *TUNDevice) Start() error {
	log.Printf("[TUN] 启动 TUN 设备: %s, 子网: %s, MTU: %d", t.cfg.Name, t.cfg.Subnet, t.cfg.MTU)
	log.Printf("[TUN] 模式: %s, DNS: %v", t.cfg.Mode, t.cfg.DNS)

	// 配置 TUN 接口 IP 地址
	if err := t.configureInterface(); err != nil {
		return fmt.Errorf("配置 TUN 接口失败: %w", err)
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
	t.iface.Close()
	t.wg.Wait()
	log.Printf("[TUN] TUN 设备已停止")
}

// configureInterface 配置 TUN 接口
func (t *TUNDevice) configureInterface() error {
	// 解析子网
	ip, ipNet, err := net.ParseCIDR(t.cfg.Subnet)
	if err != nil {
		return fmt.Errorf("解析子网失败: %w", err)
	}

	// 使用系统命令配置接口 (Linux)
	// 这里需要根据平台使用不同的方法
	log.Printf("[TUN] 请手动配置 TUN 接口:")
	log.Printf("[TUN]   sudo ip addr add %s dev %s", ip.String()+"/"+ipNet.Mask.String(), t.iface.Name())
	log.Printf("[TUN]   sudo ip link set dev %s up", t.iface.Name())
	log.Printf("[TUN]   sudo ip route add default dev %s table 100", t.iface.Name())

	return nil
}

// processPackets 处理 TUN 接口的数据包
func (t *TUNDevice) processPackets() {
	defer t.wg.Done()

	buf := make([]byte, 65535)
	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		// 读取数据包
		n, err := t.iface.Read(buf)
		if err != nil {
			select {
			case <-t.stopCh:
				return
			default:
				log.Printf("[TUN] 读取数据包失败: %v", err)
				continue
			}
		}

		if n < 20 {
			continue
		}

		// 解析 IP 数据包
		packet := buf[:n]
		go t.handlePacket(packet)
	}
}

// handlePacket 处理单个 IP 数据包
func (t *TUNDevice) handlePacket(packet []byte) {
	// 解析 IP 头
	if len(packet) < 20 {
		return
	}

	version := packet[0] >> 4
	if version != 4 {
		// 只处理 IPv4
		return
	}

	// 提取协议、源 IP、目标 IP
	protocol := packet[9]
	srcIP := net.IP(packet[12:16])
	dstIP := net.IP(packet[16:20])

	// 提取端口（TCP/UDP）
	var srcPort, dstPort uint16
	if protocol == 6 || protocol == 17 { // TCP 或 UDP
		if len(packet) >= 24 {
			srcPort = binary.BigEndian.Uint16(packet[20:22])
			dstPort = binary.BigEndian.Uint16(packet[22:24])
		}
	}

	// 分流检查
	target := fmt.Sprintf("%s:%d", dstIP.String(), dstPort)
	if t.cfg.Mode == "rule" && t.shouldDirect(target) {
		log.Printf("[TUN] 直连: %s -> %s (协议: %d)", srcIP, dstIP, protocol)
		// 直连模式需要更复杂的实现（需要 NAT 或原始套接字）
		// 这里简化处理，通过隧道转发
	}

	// 处理 DNS 查询 (UDP port 53)
	if protocol == 17 && dstPort == 53 {
		t.handleDNS(packet)
		return
	}

	// 处理 TCP 连接
	if protocol == 6 {
		t.handleTCP(srcIP, srcPort, dstIP, dstPort, packet)
		return
	}

	// 处理 UDP
	if protocol == 17 {
		t.handleUDP(srcIP, srcPort, dstIP, dstPort, packet)
		return
	}
}

// handleTCP 处理 TCP 连接
func (t *TUNDevice) handleTCP(srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16, packet []byte) {
	target := fmt.Sprintf("%s:%d", dstIP.String(), dstPort)

	// 分流检查
	if t.cfg.Mode == "rule" && t.shouldDirect(target) {
		log.Printf("[TUN] TCP 直连: %s", target)
		// 直连需要原始套接字支持，这里简化处理
		return
	}

	log.Printf("[TUN] TCP 代理: %s", target)

	// 通过隧道建立连接
	ipStrategy := protocol.IPStrategyDefault
	stream, _, _, err := t.pool.OpenTCPStream(target, ipStrategy)
	if err != nil {
		log.Printf("[TUN] 打开 TCP 流失败: %v", err)
		return
	}
	defer stream.Close()

	// 注意：由于 TUN 工作在 IP 层，需要更复杂的实现来正确处理 TCP 连接
	// 这里只是一个简化的示例
	log.Printf("[TUN] TCP 连接已建立: %s", target)
}

// handleUDP 处理 UDP 数据包
func (t *TUNDevice) handleUDP(srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16, packet []byte) {
	target := fmt.Sprintf("%s:%d", dstIP.String(), dstPort)

	// 分流检查
	if t.cfg.Mode == "rule" && t.shouldDirect(target) {
		log.Printf("[TUN] UDP 直连: %s", target)
		return
	}

	log.Printf("[TUN] UDP 代理: %s", target)

	// 提取 UDP 负载
	if len(packet) < 28 {
		return
	}
	udpPayload := packet[28:]

	// 通过隧道发送
	ipStrategy := protocol.IPStrategyDefault
	stream, _, _, err := t.pool.OpenUDPStream(target, ipStrategy)
	if err != nil {
		log.Printf("[TUN] 打开 UDP 流失败: %v", err)
		return
	}
	defer stream.Close()

	// 发送数据
	if err := protocol.WriteChunk(stream, udpPayload); err != nil {
		log.Printf("[TUN] UDP 写入失败: %v", err)
		return
	}

	// 读取响应
	reply, err := protocol.ReadChunk(stream)
	if err != nil {
		return
	}

	log.Printf("[TUN] UDP 响应: %d 字节", len(reply))
}

// handleDNS 处理 DNS 查询
func (t *TUNDevice) handleDNS(packet []byte) {
	// 提取 DNS 查询数据
	if len(packet) < 28 {
		return
	}
	dnsData := packet[28:]

	// 使用配置的 DNS 服务器
	var dnsServer string
	if len(t.cfg.DNS) > 0 {
		dnsServer = t.cfg.DNS[0]
	} else {
		dnsServer = "1.1.1.1"
	}

	// 连接到 DNS 服务器
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP(dnsServer),
		Port: 53,
	})
	if err != nil {
		log.Printf("[TUN] DNS 连接失败: %v", err)
		return
	}
	defer conn.Close()

	// 发送查询
	_, err = conn.Write(dnsData)
	if err != nil {
		log.Printf("[TUN] DNS 发送失败: %v", err)
		return
	}

	// 读取响应
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		log.Printf("[TUN] DNS 读取失败: %v", err)
		return
	}

	log.Printf("[TUN] DNS 响应: %d 字节", n)

	// 注意：这里需要将 DNS 响应写回 TUN 接口
	// 但由于 TUN 工作在 IP 层，需要构造完整的 IP/UDP 包
	// 这需要更复杂的实现
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

	// 默认走代理
	return false
}
