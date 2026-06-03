package server

import (
	"io"
	"log"
	"net"
	"time"

	"github.com/xtaci/smux"

	"github.com/x-tunnel/internal/protocol"
)

// GlobalConfig 全局配置的本地引用
type GlobalCfg struct {
	DialTimeout     time.Duration
	ReadBuf         int
}

// HandleWebSocketChannel 处理 WebSocket 通道
func HandleWebSocketChannel(ch *WSChannel, sm *SessionManager, socks5Cfg *SOCKS5Config, cfg GlobalCfg) {
	wsConn := ch.conn
	session := ch.session

	defer func() {
		_ = wsConn.Close()
		session.RemoveChannel(ch.id, ch)
		sm.stats.UpdateClientStats(session.clientID, func(stats *ClientStats) {
			stats.ChannelCount = session.ChannelCount()
		})
	}()
	netConn := protocol.NewWSNetConn(wsConn)
	sess, err := smux.Server(netConn, nil)
	if err != nil {
		log.Printf("[服务端] 通道 %d smux 初始化失败: %v", ch.id, err)
		return
	}
	defer sess.Close()
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			log.Printf("[服务端] 客户端通道 %d 断开", ch.id)
			return
		}
		go handleSmuxStream(session, ch, stream, sm, socks5Cfg, cfg)
	}
}

func handleSmuxStream(session *ClientSession, ch *WSChannel, stream *smux.Stream, sm *SessionManager, socks5Cfg *SOCKS5Config, cfg GlobalCfg) {
	defer stream.Close()
	kind, strategy, target, err := protocol.ReadSmuxOpenHeader(stream)
	if err != nil {
		return
	}
	switch kind {
	case protocol.StreamKindPing:
		payload := make([]byte, 8)
		if _, err := io.ReadFull(stream, payload); err != nil {
			return
		}
		_, _ = stream.Write(payload)
	case protocol.StreamKindTCP:
		log.Printf("[服务端] 客户ID:%s TCP 打开: %s, 通道:%d", shortID(session.clientID), target, ch.id)
		sm.stats.RecordConnection(session.clientID, shortID(session.clientID), session.remoteIP, target, "tcp", int(ch.id))

		var tcpConn net.Conn
		if socks5Cfg != nil {
			tcpConn, err = DialViaSocks5("tcp", target, socks5Cfg, cfg.DialTimeout)
		} else {
			tcpConn, err = DialTCPWithStrategy(target, strategy, cfg.DialTimeout)
		}
		if err != nil {
			return
		}
		start := time.Now()
		protocol.ProxyConnStream(tcpConn, stream)
		duration := time.Since(start)
		sm.stats.CloseConnection(session.clientID, target, "tcp", duration, 0, 0)
		log.Printf("[服务端] 客户ID:%s TCP 关闭: %s, 通道:%d", shortID(session.clientID), target, ch.id)
	case protocol.StreamKindUDP:
		log.Printf("[服务端] 客户ID:%s SOCKS5 UDP 访问: %s, 通道:%d", shortID(session.clientID), target, ch.id)
		sm.stats.RecordConnection(session.clientID, shortID(session.clientID), session.remoteIP, target, "udp", int(ch.id))
		HandleUDPRelay(stream, session, ch, target, strategy, socks5Cfg, cfg.DialTimeout)
	}
}
