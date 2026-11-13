package proxy

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Networks a forward carries.
const (
	NetworkTCP  = "tcp"
	NetworkUDP  = "udp"
	NetworkBoth = "tcp,udp"
)

// Forward relays a port on an entry node to a target address. Without Exit
// the entry node connects to the target itself (port forwarding); with Exit
// the traffic crosses an encrypted Shadowsocks 2022 tunnel to the exit node,
// which connects to the target (relay). Both run in the nodes' xray.
type Forward struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// UserID is whose quota the traffic counts against; while that user is
	// not active the forward is off. Empty only counts.
	UserID     string  `json:"user_id"`
	NodeID     string  `json:"node_id"`
	Port       int     `json:"port"`
	Network    string  `json:"network"`
	Target     string  `json:"target"`
	TargetPort int     `json:"target_port"`
	Exit       *Tunnel `json:"exit,omitempty"`
	// TrafficUp / TrafficDown are counted since the forward was made (client
	// to target / back), kept by the Server.
	TrafficUp   int64 `json:"traffic_up"`
	TrafficDown int64 `json:"traffic_down"`
	Created     int64 `json:"created_at"`
}

// Tunnel is the exit side of a relay: the entry node connects to Address:Port
// (the exit node's public address and a port opened there for this forward)
// with Key.
type Tunnel struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Key     string `json:"key"`
}

func (f Forward) exitNode() string {
	if f.Exit == nil {
		return ""
	}
	return f.Exit.NodeID
}

// Tags of the xray objects a forward makes.
func (f Forward) entryTag() string  { return "fw-" + f.ID }
func (f Forward) tunnelTag() string { return "fw-" + f.ID + "-out" }
func (f Forward) exitTag() string   { return "tun-" + f.ID }

// ForwardFromTag finds the forward id in an entry inbound's tag.
func ForwardFromTag(tag string) (string, bool) {
	id, ok := strings.CutPrefix(tag, "fw-")
	return id, ok && id != "" && !strings.Contains(id, "-")
}

func validPort(p int) error {
	if p < 1 || p > 65535 {
		return errors.New("端口为 1–65535")
	}
	if p == StatsAPIPort {
		return fmt.Errorf("端口 %d 留给 xray 的流量统计接口，请换一个", StatsAPIPort)
	}
	return nil
}

// Prepare checks what the administrator sent, keeps the Server-kept fields
// (key, counters, creation) from previous and makes a tunnel key when needed.
func (f *Forward) Prepare(previous *Forward) error {
	f.Name, f.Target = strings.TrimSpace(f.Name), strings.TrimSpace(f.Target)
	if f.Name == "" || utf8.RuneCountInString(f.Name) > MaxInboundName {
		return fmt.Errorf("名称需要 1–%d 个字", MaxInboundName)
	}
	if f.NodeID == "" {
		return errors.New("请选择入口节点")
	}
	if err := validPort(f.Port); err != nil {
		return fmt.Errorf("入口%w", err)
	}
	switch f.Network {
	case NetworkTCP, NetworkUDP, NetworkBoth:
	case "":
		f.Network = NetworkBoth
	default:
		return errors.New("协议须是 TCP、UDP 或两者")
	}
	if !validHost(f.Target) {
		return errors.New("目标须是域名或 IP")
	}
	if f.TargetPort < 1 || f.TargetPort > 65535 {
		return errors.New("目标端口为 1–65535")
	}
	f.TrafficUp, f.TrafficDown, f.Created = 0, 0, 0
	if previous != nil {
		f.TrafficUp, f.TrafficDown, f.Created = previous.TrafficUp, previous.TrafficDown, previous.Created
	}
	if f.Exit == nil {
		return nil
	}
	e := f.Exit
	e.Address = strings.TrimSpace(e.Address)
	if e.NodeID == "" {
		return errors.New("请选择出口节点")
	}
	if e.NodeID == f.NodeID {
		return errors.New("出口节点不能和入口相同；只在一台机器上转发请选「直接转发」")
	}
	if !validHost(e.Address) {
		return errors.New("出口地址须是出口节点的域名或 IP")
	}
	if err := validPort(e.Port); err != nil {
		return fmt.Errorf("隧道%w", err)
	}
	if previous != nil && previous.Exit != nil && previous.Exit.Key != "" {
		e.Key = previous.Exit.Key
	} else {
		var key [16]byte
		rand.Read(key[:])
		e.Key = base64.StdEncoding.EncodeToString(key[:])
	}
	return nil
}

// portUse is one port taken on a node, for conflict checks.
type portUse struct {
	node, owner, name string
	port              int
}

// ports lists what enabled inbounds and forwards occupy on each node.
func ports(inbounds []Inbound, forwards []Forward) []portUse {
	out := []portUse{}
	for _, in := range inbounds {
		if in.Enabled {
			out = append(out, portUse{in.NodeID, "in:" + in.ID, in.Name, in.Port})
		}
	}
	for _, f := range forwards {
		if !f.Enabled {
			continue
		}
		out = append(out, portUse{f.NodeID, "fw:" + f.ID, f.Name, f.Port})
		if f.Exit != nil {
			out = append(out, portUse{f.Exit.NodeID, "fw:" + f.ID, f.Name + "（隧道出口）", f.Exit.Port})
		}
	}
	return out
}

// conflict reports a port of want (owner) already taken by someone else.
func conflict(taken []portUse, want []portUse) error {
	for _, w := range want {
		for _, t := range taken {
			if t.owner != w.owner && t.node == w.node && t.port == w.port {
				return fmt.Errorf("端口 %d 已被「%s」使用", w.port, t.name)
			}
		}
	}
	return nil
}
