package server

import (
	"sync"
	"sync/atomic"
	"time"
)

// BandwidthLimiter 带宽限速器 (基于令牌桶)
type BandwidthLimiter struct {
	uploadBPS   atomic.Int64 // 上行限速 (bytes/sec), 0 = 不限速
	downloadBPS atomic.Int64 // 下行限速 (bytes/sec), 0 = 不限速

	mu        sync.RWMutex
	limiters  map[string]*clientLimiter // per-client limiters
}

type clientLimiter struct {
	uploadTokens   atomic.Int64
	downloadTokens atomic.Int64
	lastUpdate     atomic.Int64 // unix nano
}

// NewBandwidthLimiter 创建带宽限速器
func NewBandwidthLimiter() *BandwidthLimiter {
	return &BandwidthLimiter{
		limiters: make(map[string]*clientLimiter),
	}
}

// SetLimits 设置全局带宽限制 (bytes/sec, 0 = 不限速)
func (bl *BandwidthLimiter) SetLimits(uploadBPS, downloadBPS int64) {
	bl.uploadBPS.Store(uploadBPS)
	bl.downloadBPS.Store(downloadBPS)
}

// GetLimits 获取当前带宽限制
func (bl *BandwidthLimiter) GetLimits() (uploadBPS, downloadBPS int64) {
	return bl.uploadBPS.Load(), bl.downloadBPS.Load()
}

// AllowUpload 检查是否允许上传 n 字节
func (bl *BandwidthLimiter) AllowUpload(clientID string, n int) bool {
	limit := bl.uploadBPS.Load()
	if limit <= 0 {
		return true
	}
	return bl.allow(clientID, n, true)
}

// AllowDownload 检查是否允许下载 n 字节
func (bl *BandwidthLimiter) AllowDownload(clientID string, n int) bool {
	limit := bl.downloadBPS.Load()
	if limit <= 0 {
		return true
	}
	return bl.allow(clientID, n, false)
}

func (bl *BandwidthLimiter) allow(clientID string, n int, isUpload bool) bool {
	bl.mu.RLock()
	cl, ok := bl.limiters[clientID]
	bl.mu.RUnlock()
	if !ok {
		bl.mu.Lock()
		cl = &clientLimiter{}
		cl.lastUpdate.Store(nowNano())
		bl.limiters[clientID] = cl
		bl.mu.Unlock()
	}

	now := nowNano()
	last := cl.lastUpdate.Swap(now)
	elapsed := now - last
	if elapsed < 0 {
		elapsed = 0
	}

	var tokens *atomic.Int64
	var limit int64
	if isUpload {
		tokens = &cl.uploadTokens
		limit = bl.uploadBPS.Load()
	} else {
		tokens = &cl.downloadTokens
		limit = bl.downloadBPS.Load()
	}

	// 补充令牌
	added := elapsed * limit / 1e9
	cur := tokens.Load() + added
	if cur > limit*2 {
		cur = limit * 2
	}
	tokens.Store(cur)

	if cur >= int64(n) {
		tokens.Add(-int64(n))
		return true
	}
	return false
}

// RemoveClient 移除客户端限速器
func (bl *BandwidthLimiter) RemoveClient(clientID string) {
	bl.mu.Lock()
	delete(bl.limiters, clientID)
	bl.mu.Unlock()
}

func nowNano() int64 {
	return time.Now().UnixNano()
}
