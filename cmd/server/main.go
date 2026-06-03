package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/x-tunnel/internal/config"
	"github.com/x-tunnel/internal/server"
)

func main() {
	configFile := flag.String("config", "", "配置文件路径 (JSON)")
	listen := flag.String("l", "", "WebSocket 监听地址 (ws:// or wss://)")
	token := flag.String("token", "", "身份验证令牌")
	cidr := flag.String("cidr", "0.0.0.0/0,::/0", "允许的来源 IP 范围")
	cert := flag.String("cert", "", "TLS 证书文件路径")
	key := flag.String("key", "", "TLS 密钥文件路径")
	socks5 := flag.String("f", "", "SOCKS5 前置代理")
	flag.Parse()

	var cfg *config.ServerConfig

	if *configFile != "" {
		var err error
		cfg, err = config.LoadServerConfig(*configFile)
		if err != nil {
			log.Fatalf("加载配置文件失败: %v", err)
		}
	} else {
		defaultCfg := config.DefaultServerConfig()
		cfg = &defaultCfg
	}

	// CLI 参数覆盖配置文件
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *cidr != "0.0.0.0/0,::/0" {
		cfg.CIDR = *cidr
	}
	if *cert != "" {
		cfg.Cert = *cert
	}
	if *key != "" {
		cfg.Key = *key
	}
	if *socks5 != "" {
		cfg.SOCKS5Proxy = *socks5
	}

	// 验证配置
	if err := server.ValidateConfig(cfg); err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	// 如果没有指定监听地址，显示用法
	if *listen == "" && *configFile == "" {
		fmt.Println("x-tunnel 服务端")
		fmt.Println()
		fmt.Println("用法:")
		fmt.Println("  server -l ws://0.0.0.0:80")
		fmt.Println("  server -l wss://0.0.0.0:443 -cert cert.pem -key key.pem")
		fmt.Println("  server -config server.json")
		fmt.Println()
		fmt.Println("参数:")
		flag.PrintDefaults()
		os.Exit(0)
	}

	log.Printf("[服务端] x-tunnel 服务端启动中...")

	s := server.NewServer(cfg)
	if err := s.Start(); err != nil {
		log.Fatalf("[服务端] 启动失败: %v", err)
	}
}
