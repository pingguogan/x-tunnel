package protocol

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSNetConn 将 websocket.Conn 包装为 net.Conn 兼容接口
type WSNetConn struct {
	ws       *websocket.Conn
	readMu   sync.Mutex
	writeMu  sync.Mutex
	reader   io.Reader
	deadCh   chan struct{}
	deadMu   sync.Mutex
	deadErr  error
	deadOnce sync.Once
}

// NewWSNetConn 创建新的 WSNetConn
func NewWSNetConn(ws *websocket.Conn) *WSNetConn {
	return &WSNetConn{ws: ws, deadCh: make(chan struct{})}
}

func (c *WSNetConn) signalDead(err error) {
	if err == nil {
		return
	}
	c.deadMu.Lock()
	if c.deadErr == nil {
		c.deadErr = err
	}
	c.deadMu.Unlock()
	c.deadOnce.Do(func() {
		close(c.deadCh)
	})
}

// Dead 返回连接断开信号通道
func (c *WSNetConn) Dead() <-chan struct{} { return c.deadCh }

// DeadErr 返回导致连接断开的错误
func (c *WSNetConn) DeadErr() error {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	return c.deadErr
}

func (c *WSNetConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		if c.reader == nil {
			mt, r, err := c.ws.NextReader()
			if err != nil {
				c.signalDead(err)
				return 0, err
			}
			if mt != websocket.BinaryMessage {
				continue
			}
			c.reader = r
		}
		n, err := c.reader.Read(p)
		if errors.Is(err, io.EOF) {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			c.signalDead(err)
		}
		return n, err
	}
}

func (c *WSNetConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	w, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		c.signalDead(err)
		return 0, err
	}
	n, writeErr := w.Write(p)
	closeErr := w.Close()
	if writeErr != nil {
		c.signalDead(writeErr)
		return n, writeErr
	}
	if closeErr != nil {
		c.signalDead(closeErr)
		return n, closeErr
	}
	return n, nil
}

func (c *WSNetConn) Close() error {
	err := c.ws.Close()
	if err != nil {
		c.signalDead(err)
	} else {
		c.signalDead(io.EOF)
	}
	return err
}

func (c *WSNetConn) LocalAddr() net.Addr {
	if nc := c.ws.UnderlyingConn(); nc != nil {
		return nc.LocalAddr()
	}
	return nil
}

func (c *WSNetConn) RemoteAddr() net.Addr {
	if nc := c.ws.UnderlyingConn(); nc != nil {
		return nc.RemoteAddr()
	}
	return nil
}

func (c *WSNetConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *WSNetConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *WSNetConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

// ProxyConnStream 在两个连接之间双向转发数据
func ProxyConnStream(c net.Conn, stream io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() {
		buf := make([]byte, 64*1024)
		_, _ = io.CopyBuffer(stream, c, buf)
		done <- struct{}{}
	}()
	go func() {
		buf := make([]byte, 64*1024)
		_, _ = io.CopyBuffer(c, stream, buf)
		done <- struct{}{}
	}()
	<-done
	_ = stream.Close()
	_ = c.Close()
	<-done
}
