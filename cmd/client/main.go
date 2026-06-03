package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/x-tunnel/internal/client"
	"github.com/x-tunnel/internal/config"
	"github.com/x-tunnel/internal/protocol"
)

func main() {
	configFile := flag.String("config", "", "配置文件路径 (JSON)")
	serverAddr := flag.String("f", "", "WebSocket 服务器地址 (ws:// or wss://)")
	listenAddrs := flag.String("l", "socks5://0.0.0.0:1080", "本地监听地址 (支持多个，逗号分隔)")
	token := flag.String("token", "", "身份验证令牌")
	connections := flag.Int("n", 3, "每个IP建立的WebSocket连接数量")
	insecure := flag.Bool("insecure", false, "忽略证书校验")
	fallback := flag.Bool("fallback", false, "禁用 ECH")
	echDomain := flag.String("ech", "cloudflare-ech.com", "ECH 域名")
	dnsServer := flag.String("dns", "https://doh.pub/dns-query", "DNS 服务器")
	echConfig := flag.String("ech-config", "", "直接指定 ECH 配置 (base64)，跳过 DNS 查询")
	echConfigURL := flag.String("ech-config-url", "", "从 URL 获取 ECH 配置")
	ipStrategy := flag.String("ips", "", "IP 策略 (4/6/4,6/6,4)")
	targetIPs := flag.String("ip", "", "指定目标IP")
	blockPorts := flag.String("block", "443", "UDP 拦截端口")
	tunEnabled := flag.Bool("tun", false, "启用 TUN 模式 (需要 root 权限)")
	tunName := flag.String("tun-name", "xtun", "TUN 设备名称")
	tunSubnet := flag.String("tun-subnet", "10.0.0.1/24", "TUN 子网地址")
	tunDNS := flag.String("tun-dns", "1.1.1.1,8.8.8.8", "TUN DNS 服务器 (逗号分隔)")
	tunMode := flag.String("tun-mode", "global", "TUN 模式: global (全局) 或 rule (分流)")
	flag.Parse()

	var cfg *config.ClientConfig

	if *configFile != "" {
		var err error
		cfg, err = config.LoadClientConfig(*configFile)
		if err != nil {
			log.Fatalf("加载配置文件失败: %v", err)
		}
	} else {
		defaultCfg := config.DefaultClientConfig()
		cfg = &defaultCfg
	}

	// CLI 参数覆盖
	if *serverAddr != "" {
		cfg.Server = *serverAddr
	}
	if *listenAddrs != "socks5://0.0.0.0:1080" {
		cfg.Listen = strings.Split(*listenAddrs, ",")
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *connections != 3 {
		cfg.Connections = *connections
	}
	if *insecure {
		cfg.Insecure = true
	}
	if *fallback {
		cfg.Fallback = true
	}
	if *echDomain != "cloudflare-ech.com" {
		cfg.ECHDomain = *echDomain
	}
	if *dnsServer != "https://doh.pub/dns-query" {
		cfg.DNSServer = *dnsServer
	}
	if *echConfig != "" {
		cfg.ECHConfig = *echConfig
	}
	if *echConfigURL != "" {
		cfg.ECHConfigURL = *echConfigURL
	}
	if *ipStrategy != "" {
		cfg.IPStrategy = *ipStrategy
	}
	if *targetIPs != "" {
		cfg.TargetIPs = strings.Split(*targetIPs, ",")
	}
	if *blockPorts != "443" {
		cfg.UDPBlockPorts = *blockPorts
	}

	// TUN 参数
	if *tunEnabled {
		cfg.TUN.Enabled = true
	}
	if *tunName != "xtun" {
		cfg.TUN.Name = *tunName
	}
	if *tunSubnet != "10.0.0.1/24" {
		cfg.TUN.Subnet = *tunSubnet
	}
	if *tunDNS != "1.1.1.1,8.8.8.8" {
		cfg.TUN.DNS = strings.Split(*tunDNS, ",")
	}
	if *tunMode != "global" {
		cfg.TUN.Mode = *tunMode
	}

	// 显示用法
	if cfg.Server == "" {
		fmt.Println("x-tunnel 客户端")
		fmt.Println()
		fmt.Println("用法:")
		fmt.Println("  client -f wss://example.com -l socks5://0.0.0.0:1080")
		fmt.Println("  client -f ws://example.com -l socks5://0.0.0.0:1080,http://0.0.0.0:8080")
		fmt.Println("  client -config client.json")
		fmt.Println()
		fmt.Println("参数:")
		flag.PrintDefaults()
		os.Exit(0)
	}

	// 验证服务器地址
	forwardURL, err := url.Parse(cfg.Server)
	if err != nil {
		log.Fatalf("[客户端] 无效的服务地址: %v", err)
	}
	scheme := strings.ToLower(forwardURL.Scheme)
	if scheme != "wss" && scheme != "ws" {
		log.Fatalf("[客户端] 仅支持 ws:// 或 wss:// 协议 (当前: %s)", forwardURL.Scheme)
	}

	// 处理 insecure 自动禁用 ECH
	if cfg.Insecure && !cfg.Fallback {
		cfg.Fallback = true
		log.Printf("[客户端] wss 模式且启用不校验证书（insecure）：已自动禁用 ECH（fallback）")
	}

	// 解析 UDP 拦截端口
	udpBlockPorts := make(map[int]struct{})
	if cfg.UDPBlockPorts != "" {
		for _, p := range strings.Split(cfg.UDPBlockPorts, ",") {
			pp := strings.TrimSpace(p)
			if pp == "" {
				continue
			}
			var port int
			fmt.Sscanf(pp, "%d", &port)
			if port > 0 && port < 65536 {
				udpBlockPorts[port] = struct{}{}
			}
		}
	}

	// 生成客户端 ID
	clientID := uuid.NewString()
	log.Printf("[客户端] 客户端ID: %s", clientID)

	// 解析 IP 策略
	strategy := protocol.ParseIPStrategy(cfg.IPStrategy)
	if cfg.IPStrategy != "" {
		log.Printf("[客户端] IP 访问策略: %s (code: %d)", cfg.IPStrategy, strategy)
	}

	// 创建连接池
	pool := client.NewECHPool(
		cfg.Server,
		cfg.Connections,
		cfg.TargetIPs,
		clientID,
		cfg.Token,
		cfg.Insecure,
		cfg.Fallback,
		cfg.ECHDomain,
		cfg.DNSServer,
		cfg.ECHConfig,
		cfg.ECHConfigURL,
		cfg.Global.ReadBuf,
		nil,
	)
	if err := pool.Start(); err != nil {
		log.Fatalf("[客户端] 连接池启动失败: %v", err)
	}

	// 创建代理
	proxy := client.NewProxy(pool, strategy, udpBlockPorts)

	// 启动 TUN 模式
	if cfg.TUN.Enabled {
		log.Printf("[客户端] TUN 模式已启用")
		tunDevice, err := client.NewTUNDevice(pool, cfg.TUN)
		if err != nil {
			log.Fatalf("[客户端] 创建 TUN 设备失败: %v", err)
		}
		if err := tunDevice.Start(); err != nil {
			log.Fatalf("[客户端] 启动 TUN 设备失败: %v", err)
		}
		defer tunDevice.Stop()
	}

	// 启动监听器
	var wg sync.WaitGroup
	for _, rule := range cfg.Listen {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		if strings.HasPrefix(rule, "tcp://") {
			wg.Add(1)
			go func(r string) {
				defer wg.Done()
				proxy.RunTCPListener(r)
			}(rule)
		} else if strings.HasPrefix(rule, "socks5://") {
			wg.Add(1)
			go func(r string) {
				defer wg.Done()
				proxy.RunSOCKS5Listener(r)
			}(rule)
		} else if strings.HasPrefix(rule, "http://") {
			wg.Add(1)
			go func(r string) {
				defer wg.Done()
				proxy.RunHTTPListener(r)
			}(rule)
		} else {
			log.Printf("[客户端] 忽略未知协议的监听地址: %s", rule)
		}
	}
	wg.Wait()
}
