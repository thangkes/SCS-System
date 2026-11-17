package httpx

import (
	"net"
	"sync"
	"sync/atomic"
)

// countListener đếm số kết nối đang mở (chỉ để hiển thị, không giới hạn gì).
type countListener struct {
	net.Listener
	n *atomic.Int64
}

func (l *countListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.n.Add(1)
	return &countedConn{Conn: c, n: l.n}, nil
}

type countedConn struct {
	net.Conn
	once sync.Once
	n    *atomic.Int64
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.n.Add(-1) })
	return err
}
