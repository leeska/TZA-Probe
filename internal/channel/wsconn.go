package channel

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// conn is a net.Conn over a WebSocket: each Write is one binary message,
// Read drains messages in order. yamux runs on top of it.
type conn struct {
	ws     *websocket.Conn
	reader io.Reader
	rmu    sync.Mutex
	wmu    sync.Mutex
}

// NewConn wraps ws; the WebSocket's own read limit should be off (yamux
// frames can be up to its window size).
func NewConn(ws *websocket.Conn) net.Conn {
	ws.SetReadLimit(-1)
	return &conn{ws: ws}
}

func (c *conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if c.reader == nil {
			kind, r, err := c.ws.NextReader()
			if err != nil {
				var closeErr *websocket.CloseError
				if errors.As(err, &closeErr) {
					return 0, io.EOF
				}
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				continue
			}
			c.reader = r
		}
		n, err := c.reader.Read(p)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *conn) Close() error                       { return c.ws.Close() }
func (c *conn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *conn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
func (c *conn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}
