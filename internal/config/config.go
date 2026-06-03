package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// GlobalConfig 全局配置
type GlobalConfig struct {
	DialTimeout        time.Duration `json:"-"`
	WSHandshakeTimeout time.Duration `json:"-"`
	ReconnectDelay     time.Duration `json:"-"`
	RTTProbeTimeout    time.Duration `json:"-"`
	ReadBuf            int           `json:"read_buf"`

	// JSON 友好的超时字段（秒）
	DialTimeoutSec        float64 `json:"dial_timeout_sec"`
	WSHandshakeTimeoutSec float64 `json:"ws_handshake_timeout_sec"`
	ReconnectDelaySec     float64 `json:"reconnect_delay_sec"`
	RTTProbeTimeoutSec    float64 `json:"rtt_probe_timeout_sec"`
}

// DefaultGlobalConfig 返回默认全局配置
func DefaultGlobalConfig() GlobalConfig {
	return GlobalConfig{
		DialTimeout:           3 * time.Second,
		WSHandshakeTimeout:    5 * time.Second,
		ReconnectDelay:        1 * time.Second,
		RTTProbeTimeout:       2 * time.Second,
		ReadBuf:               64 * 1024,
		DialTimeoutSec:        3,
		WSHandshakeTimeoutSec: 5,
		ReconnectDelaySec:     1,
		RTTProbeTimeoutSec:    2,
	}
}

// Normalize 将秒字段转换为 Duration
func (c *GlobalConfig) Normalize() {
	if c.DialTimeoutSec > 0 {
		c.DialTimeout = time.Duration(c.DialTimeoutSec * float64(time.Second))
	}
	if c.WSHandshakeTimeoutSec > 0 {
		c.WSHandshakeTimeout = time.Duration(c.WSHandshakeTimeoutSec * float64(time.Second))
	}
	if c.ReconnectDelaySec > 0 {
		c.ReconnectDelay = time.Duration(c.ReconnectDelaySec * float64(time.Second))
	}
	if c.RTTProbeTimeoutSec > 0 {
		c.RTTProbeTimeout = time.Duration(c.RTTProbeTimeoutSec * float64(time.Second))
	}
	if c.ReadBuf <= 0 {
		c.ReadBuf = 64 * 1024
	}
}

// ServerConfig 服务端配置
type ServerConfig struct {
	Listen      string       `json:"listen"`
	Token       string       `json:"token"`
	CIDR        string       `json:"cidr"`
	Cert        string       `json:"cert"`
	Key         string       `json:"key"`
	SOCKS5Proxy string       `json:"socks5_proxy"`
	Global      GlobalConfig `json:"global"`
}

// DefaultServerConfig 返回默认服务端配置
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Listen: "ws://0.0.0.0:80",
		CIDR:   "0.0.0.0/0,::/0",
		Global: DefaultGlobalConfig(),
	}
}

// ClientConfig 客户端配置
type ClientConfig struct {
	Server        string       `json:"server"`
	Token         string       `json:"token"`
	Listen        []string     `json:"listen"`
	Connections   int          `json:"connections"`
	Insecure      bool         `json:"insecure"`
	Fallback      bool         `json:"fallback"`
	ECHDomain     string       `json:"ech_domain"`
	DNSServer     string       `json:"dns_server"`
	ECHConfig     string       `json:"ech_config"`      // 直接指定 ECH 配置 (base64)
	ECHConfigURL  string       `json:"ech_config_url"`  // 从 URL 获取 ECH 配置
	IPStrategy    string       `json:"ip_strategy"`
	TargetIPs     []string     `json:"target_ips"`
	UDPBlockPorts string       `json:"udp_block_ports"`
	Global        GlobalConfig `json:"global"`
}

// DefaultClientConfig 返回默认客户端配置
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Listen:        []string{"socks5://0.0.0.0:1080"},
		Connections:   3,
		ECHDomain:     "cloudflare-ech.com",
		DNSServer:     "https://doh.pub/dns-query",
		UDPBlockPorts: "443",
		Global:        DefaultGlobalConfig(),
	}
}

// LoadServerConfig 从文件加载服务端配置
func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	cfg := DefaultServerConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	cfg.Global.Normalize()
	return &cfg, nil
}

// SaveServerConfig 保存服务端配置到文件
func SaveServerConfig(path string, cfg *ServerConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadClientConfig 从文件加载客户端配置
func LoadClientConfig(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	cfg := DefaultClientConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	cfg.Global.Normalize()
	return &cfg, nil
}

// SaveClientConfig 保存客户端配置到文件
func SaveClientConfig(path string, cfg *ClientConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
