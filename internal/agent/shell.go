package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/channel"
)

// ShellHandlers serves the shell capability: one-off commands and an
// interactive terminal, both as the Agent's own user (root on a managed host).
func ShellHandlers() map[string]channel.Handler {
	return map[string]channel.Handler{
		channel.KindExec:     execHandler,
		channel.KindTerminal: terminalHandler,
	}
}

func execHandler(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var params channel.ExecParams
	if err := json.Unmarshal(raw, &params); err != nil || strings.TrimSpace(params.Command) == "" || len(params.Command) > channel.MaxCommand {
		return send(conn, channel.Message{Error: "命令无效", Exit: intPtr(-1)})
	}
	timeout := params.Timeout
	if timeout <= 0 || timeout > channel.MaxExecTimeout {
		timeout = 60
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	// The Server closing the stream (the admin gave up) ends the command too.
	go func() {
		io.Copy(io.Discard, conn)
		cancel()
	}()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", params.Command)
	cmd.Env = shellEnv()
	cmd.Dir = home()
	cmd.WaitDelay = 2 * time.Second
	out := &limitedSender{conn: conn, left: channel.MaxExecOutput}
	cmd.Stdout, cmd.Stderr = out, out
	setProcessGroup(cmd)
	err := cmd.Run()
	code := 0
	message := ""
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		code, message = -1, err.Error()
	}
	if ctx.Err() == context.DeadlineExceeded {
		message = "命令超时，已终止"
	}
	return send(conn, channel.Message{Exit: &code, Error: message, Truncated: out.truncated})
}

// limitedSender forwards output as Output messages, at most left bytes.
type limitedSender struct {
	mu        sync.Mutex
	conn      net.Conn
	left      int
	truncated bool
}

func (s *limitedSender) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	if s.left <= 0 {
		s.truncated = true
		return n, nil
	}
	if len(p) > s.left {
		p, s.truncated = p[:s.left], true
	}
	s.left -= len(p)
	send(s.conn, channel.Message{Output: append([]byte{}, p...)})
	return n, nil
}

func send(conn net.Conn, m channel.Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(b, '\n'))
	return err
}

func intPtr(v int) *int { return &v }

func shellEnv() []string {
	env := []string{"TERM=xterm-256color", "LANG=C.UTF-8", "HOME=" + home(), "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	if user := os.Getenv("USER"); user != "" {
		env = append(env, "USER="+user)
	}
	return env
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if _, err := os.Stat(h); err == nil {
			return h
		}
	}
	return "/"
}

func loginShell() string {
	for _, sh := range []string{os.Getenv("SHELL"), "/bin/bash", "/bin/sh"} {
		if sh != "" {
			if _, err := os.Stat(sh); err == nil {
				return sh
			}
		}
	}
	return "/bin/sh"
}
