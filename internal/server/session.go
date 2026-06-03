package server

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// WSChannel WebSocket 通道
type WSChannel struct {
	id      uint64
	conn    *websocket.Conn
	session *ClientSession
}

// ClientSession 客户端会话
type ClientSession struct {
	nextChanID uint64
	clientID   string
	remoteIP   string

	mu       sync.RWMutex
	channels map[uint64]*WSChannel
}

// SessionManager 会话管理器
type SessionManager struct {
	sessions sync.Map // map[string]*ClientSession
	stats    *StatsCollector
}

// NewSessionManager 创建会话管理器
func NewSessionManager(stats *StatsCollector) *SessionManager {
	return &SessionManager{
		stats: stats,
	}
}

// GetOrCreateSession 获取或创建客户端会话
func (sm *SessionManager) GetOrCreateSession(clientID, remoteIP string) *ClientSession {
	if v, ok := sm.sessions.Load(clientID); ok {
		if cs, okType := v.(*ClientSession); okType && cs != nil {
			return cs
		}
		sm.sessions.Delete(clientID)
	}
	s := &ClientSession{
		clientID: clientID,
		remoteIP: remoteIP,
		channels: make(map[uint64]*WSChannel),
	}
	actual, _ := sm.sessions.LoadOrStore(clientID, s)
	if cs, ok := actual.(*ClientSession); ok && cs != nil {
		return cs
	}
	sm.sessions.Store(clientID, s)

	// 更新统计
	sm.stats.UpdateClientStats(clientID, func(stats *ClientStats) {
		stats.ShortID = shortID(clientID)
		stats.RemoteIP = remoteIP
		stats.ChannelCount = len(s.channels)
	})

	return s
}

// AddChannel 添加通道
func (s *ClientSession) AddChannel(wsConn *websocket.Conn, preferredID uint64) *WSChannel {
	newID := preferredID
	if newID == 0 {
		newID = atomic.AddUint64(&s.nextChanID, 1)
	}
	ch := &WSChannel{
		id:      newID,
		conn:    wsConn,
		session: s,
	}
	var replaced *WSChannel
	s.mu.Lock()
	if old, ok := s.channels[ch.id]; ok {
		replaced = old
	}
	s.channels[ch.id] = ch
	s.mu.Unlock()
	if replaced != nil {
		_ = replaced.conn.Close()
	}
	return ch
}

// RemoveChannel 移除通道
func (s *ClientSession) RemoveChannel(id uint64, current *WSChannel) {
	s.mu.Lock()
	if ch, ok := s.channels[id]; ok && ch == current {
		delete(s.channels, id)
	}
	empty := len(s.channels) == 0
	s.mu.Unlock()

	if empty {
		log.Printf("[服务端] 客户端会话 %s 断开", s.clientID)
	}
}

// ChannelCount 返回通道数量
func (s *ClientSession) ChannelCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.channels)
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}
