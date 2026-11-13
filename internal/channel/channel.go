// Package channel is the Agent's long-lived link to the Server (protocol v2):
// one WebSocket per Agent, multiplexed with yamux. The Server opens a stream
// per operation (a command, a terminal, later a log tail or a config push);
// each stream starts with a one-line JSON Request naming a kind, the Agent
// answers with a one-line Response and then the kind's own messages follow.
//
// The polling HTTP endpoints of protocol 1 stay as they are; the channel only
// adds what polling cannot do: reaching an Agent right now and streaming both
// ways.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

// Path is where an Agent dials the channel.
const Path = "/api/v1/agent/channel"

// CapabilityChannel is reported by an Agent that keeps the channel open.
const CapabilityChannel = "channel"

// maxLine bounds the request / response header lines.
const maxLine = 64 << 10

// Request opens a stream: Kind picks the Agent-side handler.
type Request struct {
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the Agent's first line on a stream; a non-empty Error means the
// stream is closed right after it.
type Response struct {
	Error string `json:"error,omitempty"`
}

// ErrOffline means the node has no channel open.
var ErrOffline = errors.New("节点的管理通道未连接（Agent 离线、版本过旧或未开启管理能力）")

func config() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.KeepAliveInterval = 20 * time.Second
	c.ConnectionWriteTimeout = 15 * time.Second
	c.StreamOpenTimeout = 15 * time.Second
	return c
}

// readLine reads one newline-terminated JSON value without reading past it,
// so the bytes after the header stay on the stream for the handler.
func readLine(r io.Reader, v any) error {
	var line []byte
	buf := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		if buf[0] == '\n' {
			break
		}
		line = append(line, buf[0])
		if len(line) > maxLine {
			return errors.New("channel header too long")
		}
	}
	return json.Unmarshal(line, v)
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Hub keeps the open channel of every connected node on the Server.
type Hub struct {
	mutex    sync.Mutex
	sessions map[string]*session
}

type session struct {
	mux          *yamux.Session
	capabilities []string
	since        time.Time
}

// Serve upgrades an authenticated Agent request to the channel and keeps it
// until either side closes. A newer channel from the same node replaces the
// older one. capabilities is what the Agent offers on this channel.
func (h *Hub) Serve(conn *websocket.Conn, node string, capabilities []string) error {
	// The Server opens the streams, so it is the yamux client.
	mux, err := yamux.Client(NewConn(conn), config())
	if err != nil {
		conn.Close()
		return err
	}
	s := &session{mux: mux, capabilities: capabilities, since: time.Now()}
	h.mutex.Lock()
	if h.sessions == nil {
		h.sessions = map[string]*session{}
	}
	if old := h.sessions[node]; old != nil {
		old.mux.Close()
	}
	h.sessions[node] = s
	h.mutex.Unlock()
	<-mux.CloseChan()
	h.mutex.Lock()
	if h.sessions[node] == s {
		delete(h.sessions, node)
	}
	h.mutex.Unlock()
	return nil
}

// Status describes a node's channel for the panel.
type Status struct {
	Connected    bool      `json:"connected"`
	Since        time.Time `json:"since,omitzero"`
	Capabilities []string  `json:"capabilities"`
}

// Status reports whether node has a channel and what it offers.
func (h *Hub) Status(node string) Status {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	s := h.sessions[node]
	if s == nil || s.mux.IsClosed() {
		return Status{Capabilities: []string{}}
	}
	return Status{Connected: true, Since: s.since, Capabilities: append([]string{}, s.capabilities...)}
}

// Open starts a stream of the given kind on node's channel. The returned conn
// carries the kind's messages; close it to end the operation.
func (h *Hub) Open(ctx context.Context, node, kind string, params any) (net.Conn, error) {
	h.mutex.Lock()
	s := h.sessions[node]
	h.mutex.Unlock()
	if s == nil || s.mux.IsClosed() {
		return nil, ErrOffline
	}
	stream, err := s.mux.OpenStream()
	if err != nil {
		return nil, ErrOffline
	}
	raw, err := json.Marshal(params)
	if err != nil {
		stream.Close()
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	stream.SetDeadline(deadline)
	var response Response
	if err = writeLine(stream, Request{Kind: kind, Params: raw}); err == nil {
		err = readLine(stream, &response)
	}
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("管理通道没有应答：%w", err)
	}
	if response.Error != "" {
		stream.Close()
		return nil, errors.New(response.Error)
	}
	stream.SetDeadline(time.Time{})
	return stream, nil
}

// Handler serves one stream on the Agent. params is the Request's Params;
// conn is closed after the handler returns.
type Handler func(ctx context.Context, params json.RawMessage, conn net.Conn) error

// ServeAgent runs the Agent side of one channel connection until it drops or ctx
// ends: every stream the Server opens is dispatched to handlers by kind.
func ServeAgent(ctx context.Context, conn *websocket.Conn, handlers map[string]Handler) error {
	mux, err := yamux.Server(NewConn(conn), config())
	if err != nil {
		conn.Close()
		return err
	}
	defer mux.Close()
	go func() {
		select {
		case <-ctx.Done():
			mux.Close()
		case <-mux.CloseChan():
		}
	}()
	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go serveStream(ctx, stream, handlers)
	}
}

func serveStream(ctx context.Context, stream net.Conn, handlers map[string]Handler) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(15 * time.Second))
	var request Request
	if err := readLine(stream, &request); err != nil {
		return
	}
	handler := handlers[request.Kind]
	if handler == nil {
		writeLine(stream, Response{Error: "这个 Agent 不支持「" + request.Kind + "」或已在本机关闭"})
		return
	}
	if err := writeLine(stream, Response{}); err != nil {
		return
	}
	stream.SetDeadline(time.Time{})
	handler(ctx, request.Params, stream)
}
