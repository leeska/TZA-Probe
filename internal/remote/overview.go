package remote

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/module"
)

// Overview counts the nodes whose management channel is up and how many of
// them accept shell sessions. Nodes without the channel are simply not managed.
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	nodes, err := m.Nodes.Nodes(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	connected, shell := 0, 0
	for _, n := range nodes {
		s := m.Hub.Status(n.ID)
		if !s.Connected {
			continue
		}
		connected++
		if slices.Contains(s.Capabilities, channel.CapabilityShell) {
			shell++
		}
	}
	card := module.Card{ID: "remote", Title: "远程管理", Page: "remote", Value: fmt.Sprintf("%d / %d", connected, len(nodes))}
	if connected == 0 {
		card.Note = "没有节点开启管理通道"
	} else {
		card.Tone = "good"
		card.Note = fmt.Sprintf("已连接管理通道，%d 台可开终端", shell)
	}
	return []module.Card{card}, nil
}
