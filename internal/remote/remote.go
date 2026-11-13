// Package remote is the remote management module: it accepts the Agents'
// management channels (internal/channel) and serves the shell capability to
// the panel: one-off commands and an interactive terminal. Every command and
// terminal session is written to the audit log.
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/httpjson"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
)

// NodeLister is the part of the core store the module reads.
type NodeLister interface {
	Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error)
}

// Module serves remote management. Hub holds the open channels.
type Module struct {
	Hub   *channel.Hub
	Nodes NodeLister
}

func (*Module) ID() string { return "remote" }

func (m *Module) Routes(r module.Routes) {
	if m.Hub == nil {
		return
	}
	r.Agent("GET "+channel.Path, func(w http.ResponseWriter, req *http.Request, node string) {
		caps := []string{}
		if raw := strings.TrimSpace(req.Header.Get("X-Channel-Capabilities")); raw != "" {
			caps = strings.Split(raw, ",")
		}
		if !model.ValidCapabilities(caps) {
			httpjson.Error(w, 400, "invalid capabilities")
			return
		}
		rc := http.NewResponseController(w)
		rc.SetReadDeadline(time.Time{})
		rc.SetWriteDeadline(time.Time{})
		// An Agent sends no Origin; a browser always does, so this also
		// keeps pages from opening the channel with a stolen token.
		conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, req, nil)
		if err != nil {
			return
		}
		m.Hub.Serve(conn, node, caps)
	})
	r.Admin("GET /api/v1/admin/remote", func(w http.ResponseWriter, req *http.Request) {
		nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		type entry struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			channel.Status
		}
		out := []entry{}
		for _, n := range nodes {
			out = append(out, entry{ID: n.ID, Name: n.Name, Status: m.Hub.Status(n.ID)})
		}
		httpjson.Write(w, 200, out)
	})
	r.Admin("POST /api/v1/admin/remote/exec", func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Node    string `json:"node_id"`
			Command string `json:"command"`
			Timeout int    `json:"timeout_seconds"`
		}
		if !httpjson.Decode(w, req, &input) {
			return
		}
		if strings.TrimSpace(input.Command) == "" || len(input.Command) > channel.MaxCommand {
			httpjson.Error(w, 400, "请填写命令（最多 16 KB）")
			return
		}
		if input.Timeout <= 0 {
			input.Timeout = 60
		}
		if input.Timeout > channel.MaxExecTimeout {
			httpjson.Error(w, 400, "超时最长 3600 秒")
			return
		}
		if !m.known(req.Context(), input.Node) {
			httpjson.Error(w, 404, "节点不存在")
			return
		}
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Duration(input.Timeout+30) * time.Second))
		started := time.Now()
		result, err := m.exec(req.Context(), input.Node, channel.ExecParams{Command: input.Command, Timeout: input.Timeout})
		entry := model.AuditEntry{Action: "shell.exec", NodeID: input.Node, Detail: input.Command}
		if err != nil {
			entry.Status = 502
			entry.Detail += "\n# " + err.Error()
			r.Audit(req, entry)
			httpjson.Error(w, 502, err.Error())
			return
		}
		result.Duration = time.Since(started).Milliseconds()
		entry.Status = 200
		entry.Detail += "\n# exit " + strconv.Itoa(result.ExitCode)
		r.Audit(req, entry)
		httpjson.Write(w, 200, result)
	})
	r.Admin("GET /api/v1/admin/remote/terminal", func(w http.ResponseWriter, req *http.Request) {
		m.terminal(w, req, r)
	})
}

func (m *Module) known(ctx context.Context, node string) bool {
	nodes, err := m.Nodes.Nodes(ctx, time.Now())
	if err != nil {
		return false
	}
	for _, n := range nodes {
		if n.ID == node {
			return true
		}
	}
	return false
}

// ExecResult is a finished command.
type ExecResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Error     string `json:"error,omitempty"`
	Truncated bool   `json:"truncated"`
	Duration  int64  `json:"duration_ms"`
}

func (m *Module) exec(ctx context.Context, node string, params channel.ExecParams) (ExecResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.Timeout+15)*time.Second)
	defer cancel()
	conn, err := m.Hub.Open(ctx, node, channel.KindExec, params)
	if err != nil {
		return ExecResult{}, err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()
	var out []byte
	var result ExecResult
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var msg channel.Message
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		if len(msg.Output) > 0 && len(out) < channel.MaxExecOutput {
			out = append(out, msg.Output...)
		}
		if msg.Exit != nil {
			result.ExitCode, result.Error, result.Truncated = *msg.Exit, msg.Error, msg.Truncated
			result.Output = strings.ToValidUTF8(string(out), "�")
			return result, nil
		}
	}
	if ctx.Err() != nil {
		return ExecResult{}, errors.New("等待命令结果超时")
	}
	return ExecResult{}, errors.New("管理通道在命令结束前断开")
}

// sameOrigin is the check the API applies to admin writes, here for the
// terminal's WebSocket (a GET, so the API does not check it): without it any
// page could open a shell with the administrator's cookie.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "https" || u.Scheme == "http")
}

func (m *Module) terminal(w http.ResponseWriter, req *http.Request, r module.Routes) {
	if !sameOrigin(req) {
		httpjson.Error(w, 403, "请求来源不受信任")
		return
	}
	node := req.URL.Query().Get("node")
	if !m.known(req.Context(), node) {
		httpjson.Error(w, 404, "节点不存在")
		return
	}
	cols, _ := strconv.Atoi(req.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(req.URL.Query().Get("rows"))
	agent, err := m.Hub.Open(req.Context(), node, channel.KindTerminal, channel.TerminalParams{Cols: cols, Rows: rows})
	if err != nil {
		r.Audit(req, model.AuditEntry{Action: "shell.terminal", NodeID: node, Status: 502, Detail: err.Error()})
		httpjson.Error(w, 502, err.Error())
		return
	}
	defer agent.Close()
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{})
	rc.SetWriteDeadline(time.Time{})
	browser, err := (&websocket.Upgrader{CheckOrigin: sameOrigin}).Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer browser.Close()
	started := time.Now()
	r.Audit(req, model.AuditEntry{Action: "shell.terminal", NodeID: node, Status: 101})
	var bytesIn atomic.Int64
	var once sync.Once
	var writeMu sync.Mutex
	done := make(chan struct{})
	finish := func() { once.Do(func() { close(done); agent.Close(); browser.Close() }) }
	// Keepalive for proxies that drop idle WebSockets (nginx: 60 s).
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				writeMu.Lock()
				err := browser.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				writeMu.Unlock()
				if err != nil {
					finish()
					return
				}
			}
		}
	}()
	// Browser → Agent: {"i": "keys"} and {"resize": {"cols","rows"}}.
	go func() {
		defer finish()
		browser.SetReadLimit(1 << 20)
		for {
			_, data, err := browser.ReadMessage()
			if err != nil {
				return
			}
			var in struct {
				Input  string        `json:"i"`
				Resize *channel.Size `json:"resize"`
			}
			if json.Unmarshal(data, &in) != nil {
				continue
			}
			msg := channel.Message{Resize: in.Resize}
			if in.Input != "" {
				msg.Input = []byte(in.Input)
				bytesIn.Add(int64(len(msg.Input)))
			}
			raw, _ := json.Marshal(msg)
			if _, err := agent.Write(append(raw, '\n')); err != nil {
				return
			}
		}
	}()
	// Agent → browser: output as binary frames, the exit code as text.
	exit := -1
	scanner := bufio.NewScanner(agent)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var msg channel.Message
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		writeMu.Lock()
		if len(msg.Output) > 0 {
			err = browser.WriteMessage(websocket.BinaryMessage, msg.Output)
		}
		if msg.Exit != nil {
			exit = *msg.Exit
			text, _ := json.Marshal(map[string]any{"exit": exit, "error": msg.Error})
			err = browser.WriteMessage(websocket.TextMessage, text)
		}
		writeMu.Unlock()
		if err != nil || msg.Exit != nil {
			break
		}
	}
	finish()
	r.Audit(req, model.AuditEntry{Action: "shell.terminal.closed", NodeID: node, Status: exit,
		Detail: "持续 " + time.Since(started).Round(time.Second).String() + "，键入 " + strconv.FormatInt(bytesIn.Load(), 10) + " 字节"})
}
