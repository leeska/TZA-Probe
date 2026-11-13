//go:build !unix

package agent

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"

	"github.com/leeska/TZA-Probe/internal/channel"
)

func setProcessGroup(*exec.Cmd) {}

func terminalHandler(_ context.Context, _ json.RawMessage, conn net.Conn) error {
	return send(conn, channel.Message{Error: "这个系统不支持终端", Exit: intPtr(-1)})
}

func runTerminal(_ context.Context, conn net.Conn, _, _ int, _ []string, _ string, _ ...string) error {
	return send(conn, channel.Message{Error: "这个系统不支持终端", Exit: intPtr(-1)})
}
