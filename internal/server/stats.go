package server

import (
	"sync"
	"sync/atomic"
	"time"
)

// TrafficStats 流量统计
type TrafficStats struct {
	UploadBytes   int64 `json:"upload_bytes"`
	DownloadBytes int64 `json:"download_bytes"`
}

// ClientStats 客户端统计数据
type ClientStats struct {
	ClientID      string    `json:"client_id"`
	ShortID       string    `json:"short_id"`
	RemoteIP      string    `json:"remote_ip"`
	ChannelCount  int       `json:"channel_count"`
	UploadBytes   int64     `json:"upload_bytes"`
	DownloadBytes int64     `json:"download_bytes"`
	ActiveStreams  int64     `json:"active_streams"`
	ConnectedAt   time.Time `json:"connected_at"`
	LastActiveAt  time.Time `json:"last_active_at"`
}

// ConnectionRecord 连接历史记录
type ConnectionRecord struct {
	ClientID    string    `json:"client_id"`
	ShortID     string    `json:"short_id"`
	SourceIP    string    `json:"source_ip"`
	Target      string    `json:"target"`
	Protocol    string    `json:"protocol"` // "tcp" or "udp"
	StartedAt   time.Time `json:"started_at"`
	Duration    float64   `json:"duration_sec"`
	BytesSent   int64     `json:"bytes_sent"`
	BytesRecv   int64     `json:"bytes_recv"`
	ChannelID   int       `json:"channel_id"`
}

// TrafficPoint 流量时间点
type TrafficPoint struct {
	Timestamp    int64 `json:"t"` // unix timestamp
	UploadBytes  int64 `json:"u"`
	DownloadBytes int64 `json:"d"`
}

// StatsCollector 统计数据收集器
type StatsCollector struct {
	// 全局统计
	totalConns    atomic.Int64
	totalUpload   atomic.Int64
	totalDownload atomic.Int64
	connRate      atomic.Int64 // 每分钟连接数

	// 客户端统计 (key: clientID)
	clients sync.Map // map[string]*ClientStats

	// 连接历史 (环形缓冲区)
	historyMu   sync.RWMutex
	history     []ConnectionRecord
	historyHead int
	historySize int
	historyCap  int

	// 流量历史 (每分钟一个点，保留24小时)
	trafficMu    sync.RWMutex
	traffic      []TrafficPoint
	trafficHead  int
	trafficSize  int
	trafficCap   int // 1440 = 24 * 60

	// 当前分钟的流量累加
	currentMinuteUpload   atomic.Int64
	currentMinuteDownload atomic.Int64
	currentMinuteStart    int64

	// 日志缓冲
	logMu   sync.RWMutex
	logs    []LogEntry
	logHead int
	logSize int
	logCap  int

	// 实时连接速率计数
	rateWindow   atomic.Int64
	rateCounter  atomic.Int64
}

// LogEntry 日志条目
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// NewStatsCollector 创建统计收集器
func NewStatsCollector() *StatsCollector {
	now := time.Now()
	sc := &StatsCollector{
		history:        make([]ConnectionRecord, 1000),
		historyCap:     1000,
		traffic:        make([]TrafficPoint, 1440),
		trafficCap:     1440,
		logs:           make([]LogEntry, 5000),
		logCap:         5000,
		currentMinuteStart: now.Unix() / 60,
	}
	// 启动分钟级流量快照
	go sc.trafficSnapshotLoop()
	// 启动连接速率计数器重置
	go sc.rateResetLoop()
	return sc
}

func (sc *StatsCollector) trafficSnapshotLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		upload := sc.currentMinuteUpload.Swap(0)
		download := sc.currentMinuteDownload.Swap(0)
		sc.currentMinuteStart = time.Now().Unix() / 60

		sc.trafficMu.Lock()
		point := TrafficPoint{
			Timestamp:     time.Now().Unix(),
			UploadBytes:   upload,
			DownloadBytes: download,
		}
		if sc.trafficSize < sc.trafficCap {
			sc.traffic[sc.trafficSize] = point
			sc.trafficSize++
		} else {
			sc.traffic[sc.trafficHead] = point
			sc.trafficHead = (sc.trafficHead + 1) % sc.trafficCap
		}
		sc.trafficMu.Unlock()
	}
}

func (sc *StatsCollector) rateResetLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rate := sc.rateCounter.Swap(0)
		sc.rateWindow.Store(rate)
	}
}

// RecordConnection 记录新连接
func (sc *StatsCollector) RecordConnection(clientID, shortID, sourceIP, target, protocol string, channelID int) {
	sc.totalConns.Add(1)
	sc.rateCounter.Add(1)

	record := ConnectionRecord{
		ClientID:  clientID,
		ShortID:   shortID,
		SourceIP:  sourceIP,
		Target:    target,
		Protocol:  protocol,
		StartedAt: time.Now(),
		ChannelID: channelID,
	}
	sc.historyMu.Lock()
	if sc.historySize < sc.historyCap {
		sc.history[sc.historySize] = record
		sc.historySize++
	} else {
		sc.history[sc.historyHead] = record
		sc.historyHead = (sc.historyHead + 1) % sc.historyCap
	}
	sc.historyMu.Unlock()
}

// CloseConnection 关闭连接记录
func (sc *StatsCollector) CloseConnection(clientID, target, protocol string, duration time.Duration, bytesSent, bytesRecv int64) {
	sc.historyMu.Lock()
	// 查找并更新最近的匹配记录
	for i := sc.historySize - 1; i >= 0; i-- {
		idx := (sc.historyHead + i) % sc.historyCap
		r := &sc.history[idx]
		if r.ClientID == clientID && r.Target == target && r.Protocol == protocol && r.Duration == 0 {
			r.Duration = duration.Seconds()
			r.BytesSent = bytesSent
			r.BytesRecv = bytesRecv
			break
		}
	}
	sc.historyMu.Unlock()
}

// AddTraffic 添加流量统计
func (sc *StatsCollector) AddTraffic(uploadBytes, downloadBytes int64) {
	sc.totalUpload.Add(uploadBytes)
	sc.totalDownload.Add(downloadBytes)
	sc.currentMinuteUpload.Add(uploadBytes)
	sc.currentMinuteDownload.Add(downloadBytes)
}

// UpdateClientStats 更新客户端统计
func (sc *StatsCollector) UpdateClientStats(clientID string, update func(*ClientStats)) {
	val, _ := sc.clients.LoadOrStore(clientID, &ClientStats{
		ClientID:    clientID,
		ConnectedAt: time.Now(),
	})
	stats := val.(*ClientStats)
	update(stats)
}

// RemoveClient 移除客户端统计
func (sc *StatsCollector) RemoveClient(clientID string) {
	sc.clients.Delete(clientID)
}

// GetClientStats 获取所有客户端统计
func (sc *StatsCollector) GetClientStats() []ClientStats {
	var result []ClientStats
	sc.clients.Range(func(key, value any) bool {
		stats := value.(*ClientStats)
		result = append(result, *stats)
		return true
	})
	return result
}

// GetClientStatsByID 获取单个客户端统计
func (sc *StatsCollector) GetClientStatsByID(clientID string) *ClientStats {
	val, ok := sc.clients.Load(clientID)
	if !ok {
		return nil
	}
	return val.(*ClientStats)
}

// GetDashboard 获取仪表盘数据
func (sc *StatsCollector) GetDashboard() map[string]interface{} {
	onlineCount := 0
	sc.clients.Range(func(key, value any) bool {
		onlineCount++
		return true
	})
	return map[string]interface{}{
		"online_clients":   onlineCount,
		"total_connections": sc.totalConns.Load(),
		"total_upload":     sc.totalUpload.Load(),
		"total_download":   sc.totalDownload.Load(),
		"connection_rate":  sc.rateWindow.Load(),
	}
}

// GetTrafficHistory 获取流量历史
func (sc *StatsCollector) GetTrafficHistory(limit int) []TrafficPoint {
	sc.trafficMu.RLock()
	defer sc.trafficMu.RUnlock()

	if limit <= 0 || limit > sc.trafficSize {
		limit = sc.trafficSize
	}
	result := make([]TrafficPoint, limit)
	for i := 0; i < limit; i++ {
		idx := (sc.trafficHead + sc.trafficSize - limit + i) % sc.trafficCap
		result[i] = sc.traffic[idx]
	}
	return result
}

// GetConnectionHistory 获取连接历史
func (sc *StatsCollector) GetConnectionHistory(clientID string, limit int) []ConnectionRecord {
	sc.historyMu.RLock()
	defer sc.historyMu.RUnlock()

	if limit <= 0 || limit > sc.historySize {
		limit = sc.historySize
	}
	var result []ConnectionRecord
	for i := sc.historySize - 1; i >= 0 && len(result) < limit; i-- {
		idx := (sc.historyHead + i) % sc.historyCap
		r := sc.history[idx]
		if clientID == "" || r.ClientID == clientID {
			result = append(result, r)
		}
	}
	return result
}

// AddLog 添加日志
func (sc *StatsCollector) AddLog(level, message string) {
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	}
	sc.logMu.Lock()
	if sc.logSize < sc.logCap {
		sc.logs[sc.logSize] = entry
		sc.logSize++
	} else {
		sc.logs[sc.logHead] = entry
		sc.logHead = (sc.logHead + 1) % sc.logCap
	}
	sc.logMu.Unlock()

	// 通知日志监听器
	sc.notifyLogListeners(entry)
}

// GetLogs 获取日志
func (sc *StatsCollector) GetLogs(level string, limit int) []LogEntry {
	sc.logMu.RLock()
	defer sc.logMu.RUnlock()

	if limit <= 0 || limit > sc.logSize {
		limit = sc.logSize
	}
	var result []LogEntry
	for i := sc.logSize - 1; i >= 0 && len(result) < limit; i-- {
		idx := (sc.logHead + i) % sc.logCap
		e := sc.logs[idx]
		if level == "" || e.Level == level {
			result = append(result, e)
		}
	}
	return result
}

// 日志 WebSocket 监听器
var (
	logListenersMu sync.RWMutex
	logListeners   = make(map[chan LogEntry]struct{})
)

// SubscribeLogs 订阅日志推送
func (sc *StatsCollector) SubscribeLogs() chan LogEntry {
	ch := make(chan LogEntry, 100)
	logListenersMu.Lock()
	logListeners[ch] = struct{}{}
	logListenersMu.Unlock()
	return ch
}

// UnsubscribeLogs 取消订阅日志推送
func (sc *StatsCollector) UnsubscribeLogs(ch chan LogEntry) {
	logListenersMu.Lock()
	delete(logListeners, ch)
	logListenersMu.Unlock()
	close(ch)
}

func (sc *StatsCollector) notifyLogListeners(entry LogEntry) {
	logListenersMu.RLock()
	defer logListenersMu.RUnlock()
	for ch := range logListeners {
		select {
		case ch <- entry:
		default:
			// 队列满则跳过
		}
	}
}
