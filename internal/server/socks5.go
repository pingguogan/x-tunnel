package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/x-tunnel/internal/config"
)

// SOCKS5Config SOCKS5 代理配置
type SOCKS5Config struct {
	Host     string
	Username string
	Password string
}

// ParseSOCKS5Addr 解析 SOCKS5 地址
func ParseSOCKS5Addr(addr string) (*SOCKS5Config, error) {
	addr = strings.TrimPrefix(addr, "socks5://")
	cfg := &SOCKS5Config{}

	if strings.Contains(addr, "@") {
		parts := strings.SplitN(addr, "@", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("无效的SOCKS5地址格式")
		}
		auth := parts[0]
		if strings.Contains(auth, ":") {
			authParts := strings.SplitN(auth, ":", 2)
			cfg.Username = authParts[0]
			cfg.Password = authParts[1]
		}
		cfg.Host = parts[1]
	} else {
		cfg.Host = addr
	}
	return cfg, nil
}

// DialViaSocks5 通过 SOCKS5 代理拨号
func DialViaSocks5(network, addr string, socks5Cfg *SOCKS5Config, timeout time.Duration) (net.Conn, error) {
	if socks5Cfg == nil {
		return net.DialTimeout(network, addr, timeout)
	}
	proxyConn, err := net.DialTimeout("tcp", socks5Cfg.Host, timeout)
	if err != nil {
		return nil, fmt.Errorf("连接SOCKS5代理失败: %v", err)
	}
	if err := socks5Handshake(proxyConn, socks5Cfg); err != nil {
		proxyConn.Close()
		return nil, fmt.Errorf("SOCKS5握手失败: %v", err)
	}
	if err := socks5Connect(proxyConn, addr); err != nil {
		proxyConn.Close()
		return nil, fmt.Errorf("SOCKS5 CONNECT失败: %v", err)
	}
	return proxyConn, nil
}

func socks5Handshake(conn net.Conn, cfg *SOCKS5Config) error {
	var methods []byte
	if cfg.Username != "" && cfg.Password != "" {
		methods = []byte{0x00, 0x02}
	} else {
		methods = []byte{0x00}
	}
	greeting := make([]byte, 2+len(methods))
	greeting[0], greeting[1] = 0x05, byte(len(methods))
	copy(greeting[2:], methods)

	if _, err := conn.Write(greeting); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[0] != 0x05 {
		return fmt.Errorf("SOCKS 版本错误: %d", response[0])
	}
	switch response[1] {
	case 0x00:
		return nil
	case 0x02:
		return socks5UserPassAuthSrv(conn, cfg.Username, cfg.Password)
	case 0xFF:
		return errors.New("服务器不接受认证")
	default:
		return fmt.Errorf("认证方法错误: %d", response[1])
	}
}

func socks5UserPassAuthSrv(conn net.Conn, username, password string) error {
	authReq := make([]byte, 3+len(username)+len(password))
	authReq[0], authReq[1] = 0x01, byte(len(username))
	copy(authReq[2:], username)
	authReq[2+len(username)] = byte(len(password))
	copy(authReq[3+len(username):], password)

	if _, err := conn.Write(authReq); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[1] != 0x00 {
		return errors.New("认证失败")
	}
	return nil
}

func socks5Connect(conn net.Conn, addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	var request []byte
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = make([]byte, 10)
			request[0], request[1], request[2], request[3] = 0x05, 0x01, 0x00, 0x01
			copy(request[4:8], ip4)
			request[8], request[9] = byte(port>>8), byte(port)
		} else {
			request = make([]byte, 22)
			request[0], request[1], request[2], request[3] = 0x05, 0x01, 0x00, 0x04
			copy(request[4:20], ip)
			request[20], request[21] = byte(port>>8), byte(port)
		}
	} else {
		request = make([]byte, 7+len(host))
		request[0], request[1], request[2], request[3] = 0x05, 0x01, 0x00, 0x03
		request[4] = byte(len(host))
		copy(request[5:], host)
		request[5+len(host)], request[6+len(host)] = byte(port>>8), byte(port)
	}

	if _, err := conn.Write(request); err != nil {
		return err
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[1] != 0x00 {
		return fmt.Errorf("状态码: %d", response[1])
	}
	switch response[3] {
	case 0x01:
		_, _ = io.ReadFull(conn, make([]byte, 6))
	case 0x03:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return err
		}
		_, _ = io.ReadFull(conn, make([]byte, int(lenBuf[0])+2))
	case 0x04:
		_, _ = io.ReadFull(conn, make([]byte, 18))
	}
	return nil
}

// GenerateSelfSignedCert 生成自签名 TLS 证书
func GenerateSelfSignedCert() (tls.Certificate, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"SelfSigned"}},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}),
	)
}

// ValidateConfig 验证服务端配置
func ValidateConfig(cfg *config.ServerConfig) error {
	if cfg.Listen == "" {
		return fmt.Errorf("监听地址不能为空")
	}
	if !strings.HasPrefix(cfg.Listen, "ws://") && !strings.HasPrefix(cfg.Listen, "wss://") {
		return fmt.Errorf("监听地址必须以 ws:// 或 wss:// 开头")
	}
	return nil
}
