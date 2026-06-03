package server

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/x-tunnel/internal/protocol"
)

// DialTCPWithStrategy 根据 IP 策略拨号 TCP
func DialTCPWithStrategy(addr string, strategy byte, timeout time.Duration) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return net.DialTimeout("tcp", addr, timeout)
	}

	if ip := net.ParseIP(host); ip != nil {
		return net.DialTimeout("tcp", addr, timeout)
	}

	if strategy == protocol.IPStrategyIPv4Only {
		return net.DialTimeout("tcp4", addr, timeout)
	}
	if strategy == protocol.IPStrategyIPv6Only {
		return net.DialTimeout("tcp6", addr, timeout)
	}

	if strategy == protocol.IPStrategyPv4Pv6 || strategy == protocol.IPStrategyPv6Pv4 {
		resolver := &net.Resolver{}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		addrs, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}

		var v4, v6 net.IP
		for _, a := range addrs {
			if a.IP.To4() != nil {
				if v4 == nil {
					v4 = a.IP
				}
			} else {
				if v6 == nil {
					v6 = a.IP
				}
			}
			if v4 != nil && v6 != nil {
				break
			}
		}

		var selected net.IP
		if strategy == protocol.IPStrategyPv4Pv6 {
			if v4 != nil {
				selected = v4
			} else {
				selected = v6
			}
		} else {
			if v6 != nil {
				selected = v6
			} else {
				selected = v4
			}
		}

		if selected == nil {
			return nil, fmt.Errorf("未找到可用IP: %s", host)
		}

		target := net.JoinHostPort(selected.String(), port)
		return net.DialTimeout("tcp", target, timeout)
	}

	return net.DialTimeout("tcp", addr, timeout)
}

// ResolveUDPWithStrategy 根据 IP 策略解析 UDP 地址
func ResolveUDPWithStrategy(addr string, strategy byte, timeout time.Duration) (*net.UDPAddr, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return net.ResolveUDPAddr("udp", addr)
	}
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	if ip := net.ParseIP(host); ip != nil {
		return &net.UDPAddr{IP: ip, Port: port}, nil
	}

	if strategy == protocol.IPStrategyIPv4Only {
		return net.ResolveUDPAddr("udp4", addr)
	}
	if strategy == protocol.IPStrategyIPv6Only {
		return net.ResolveUDPAddr("udp6", addr)
	}

	if strategy == protocol.IPStrategyPv4Pv6 || strategy == protocol.IPStrategyPv6Pv4 {
		resolver := &net.Resolver{}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		addrs, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}

		var v4, v6 net.IP
		for _, a := range addrs {
			if a.IP.To4() != nil {
				if v4 == nil {
					v4 = a.IP
				}
			} else {
				if v6 == nil {
					v6 = a.IP
				}
			}
			if v4 != nil && v6 != nil {
				break
			}
		}

		var selected net.IP
		if strategy == protocol.IPStrategyPv4Pv6 {
			if v4 != nil {
				selected = v4
			} else {
				selected = v6
			}
		} else {
			if v6 != nil {
				selected = v6
			} else {
				selected = v4
			}
		}

		if selected == nil {
			return nil, fmt.Errorf("未找到可用IP: %s", host)
		}
		return &net.UDPAddr{IP: selected, Port: port}, nil
	}

	return net.ResolveUDPAddr("udp", addr)
}
