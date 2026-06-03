package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// 流类型常量
const (
	StreamKindTCP  byte = 1
	StreamKindUDP  byte = 2
	StreamKindPing byte = 3
)

// IP 策略常量
const (
	IPStrategyDefault  byte = 0
	IPStrategyIPv4Only byte = 1
	IPStrategyIPv6Only byte = 2
	IPStrategyPv4Pv6   byte = 3
	IPStrategyPv6Pv4   byte = 4
)

// WriteSmuxOpenHeader 写入 smux 流打开头部
// 格式: [kind(1)] [strategy(1)] [targetLen(2)] [target(N)]
func WriteSmuxOpenHeader(w io.Writer, kind byte, strategy byte, target string) error {
	if len(target) > 65535 {
		return fmt.Errorf("目标地址过长")
	}
	head := make([]byte, 4)
	head[0] = kind
	head[1] = strategy
	binary.BigEndian.PutUint16(head[2:4], uint16(len(target)))
	if _, err := w.Write(head); err != nil {
		return err
	}
	if len(target) == 0 {
		return nil
	}
	_, err := w.Write([]byte(target))
	return err
}

// ReadSmuxOpenHeader 读取 smux 流打开头部
func ReadSmuxOpenHeader(r io.Reader) (byte, byte, string, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, 0, "", err
	}
	kind := head[0]
	strategy := head[1]
	targetLen := int(binary.BigEndian.Uint16(head[2:4]))
	targetRaw := make([]byte, targetLen)
	if targetLen > 0 {
		if _, err := io.ReadFull(r, targetRaw); err != nil {
			return 0, 0, "", err
		}
	}
	return kind, strategy, string(targetRaw), nil
}

// ParseIPStrategy 解析 IP 策略字符串
func ParseIPStrategy(s string) byte {
	switch s {
	case "4":
		return IPStrategyIPv4Only
	case "6":
		return IPStrategyIPv6Only
	case "4,6":
		return IPStrategyPv4Pv6
	case "6,4":
		return IPStrategyPv6Pv4
	default:
		return IPStrategyDefault
	}
}
