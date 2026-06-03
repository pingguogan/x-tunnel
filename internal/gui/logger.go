package gui

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// LogWriter 自定义日志写入器
type LogWriter struct {
	mu      sync.Mutex
	app     *App
 writers []io.Writer
}

// NewLogWriter 创建日志写入器
func NewLogWriter(app *App) *LogWriter {
	lw := &LogWriter{
		app:     app,
		writers: []io.Writer{os.Stdout},
	}
	return lw
}

func (lw *LogWriter) Write(p []byte) (n int, err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	msg := string(p)

	// 解析日志级别
	level := "INFO"
	if strings.Contains(msg, "[ERROR]") || strings.Contains(msg, "失败") || strings.Contains(msg, "错误") {
		level = "ERROR"
	} else if strings.Contains(msg, "[WARN]") || strings.Contains(msg, "警告") {
		level = "WARN"
	}

	// 提取消息内容（去掉时间戳和前缀）
	msg = strings.TrimSpace(msg)
	// 去掉 Go log 的时间戳前缀
	if idx := strings.Index(msg, " "); idx > 0 {
		// 检查是否是时间戳格式
		parts := strings.SplitN(msg, " ", 2)
		if len(parts) > 1 {
			msg = parts[1]
		}
	}

	// 添加到应用日志
	lw.app.addLog(level, msg)

	// 写入其他 writer
	for _, w := range lw.writers {
		w.Write(p)
	}

	return len(p), nil
}

// InitLogger 初始化日志系统
func (a *App) InitLogger() {
	logWriter := NewLogWriter(a)
	log.SetOutput(logWriter)
	log.SetFlags(log.Ltime) // 只显示时间
}

// addLog 添加日志（增强版）
func (a *App) addLogEnhanced(level, message string) {
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	}
	a.logMu.Lock()
	a.logs = append(a.logs, entry)
	// 保留最近 2000 条日志
	if len(a.logs) > 2000 {
		a.logs = a.logs[len(a.logs)-2000:]
	}
	a.logMu.Unlock()

	// 同时输出到 stdout（调试用）
	fmt.Printf("[%s] %s\n", level, message)
}
