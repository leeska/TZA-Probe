//go:build unix

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"github.com/leeska/TZA-Probe/internal/channel"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

func terminalHandler(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var params channel.TerminalParams
	json.Unmarshal(raw, &params)
	size := &pty.Winsize{Cols: clampSize(params.Cols, 80), Rows: clampSize(params.Rows, 24)}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, loginShell(), "-l")
	cmd.Env = shellEnv()
	cmd.Dir = home()
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	tty, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return send(conn, channel.Message{Error: "无法打开终端：" + err.Error(), Exit: intPtr(-1)})
	}
	defer tty.Close()
	// Server → shell: keystrokes and resizes. The stream closing hangs up.
	go func() {
		defer cancel()
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			var m channel.Message
			if json.Unmarshal(scanner.Bytes(), &m) != nil {
				continue
			}
			if len(m.Input) > 0 {
				if _, err := tty.Write(m.Input); err != nil {
					return
				}
			}
			if m.Resize != nil {
				pty.Setsize(tty, &pty.Winsize{Cols: clampSize(m.Resize.Cols, 80), Rows: clampSize(m.Resize.Rows, 24)})
			}
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := tty.Read(buf)
		if n > 0 {
			if send(conn, channel.Message{Output: append([]byte{}, buf[:n]...)}) != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	cmd.Wait()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return send(conn, channel.Message{Exit: &code})
}

func clampSize(v, fallback int) uint16 {
	if v < 2 || v > 1000 {
		return uint16(fallback)
	}
	return uint16(v)
}
