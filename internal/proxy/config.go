package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
)

type obj = map[string]any

// Config renders the whole configuration one kernel runs on a node: every
// enabled inbound for that kernel with the members allowed on it. Users are
// named by their id (the email in xray), which traffic accounting keys on.
// Only xray counts traffic per user: the official sing-box builds leave out
// the v2ray API that would expose it.
// The output is deterministic (sorted keys), so its hash tells whether a node
// is up to date.
func Config(kernel string, inbounds []Inbound, members []Member) ([]byte, error) {
	return NodeConfig(kernel, Plan{Inbounds: inbounds}, members)
}

// Plan is what one kernel runs on one node: proxy inbounds, the forwards
// that enter here and the tunnels that leave here (forwards are xray only).
type Plan struct {
	Inbounds []Inbound
	Entry    []Forward
	Exit     []Forward
}

// NodeConfig renders a node's whole configuration for kernel.
func NodeConfig(kernel string, plan Plan, members []Member) ([]byte, error) {
	inbounds := plan.Inbounds
	var out obj
	switch kernel {
	case KernelXray:
		list := []any{}
		for _, in := range inbounds {
			list = append(list, xrayInbound(in, in.Members(members)))
		}
		outbounds := []any{obj{"protocol": "freedom", "tag": "direct"}, obj{"protocol": "blackhole", "tag": "block"}}
		rules := []any{}
		for _, f := range plan.Entry {
			list = append(list, obj{"tag": f.entryTag(), "listen": "0.0.0.0", "port": f.Port, "protocol": "dokodemo-door",
				"settings": obj{"address": f.Target, "port": f.TargetPort, "network": f.Network}})
			if f.Exit != nil {
				outbounds = append(outbounds, obj{"tag": f.tunnelTag(), "protocol": "shadowsocks", "settings": obj{"servers": []any{
					obj{"address": f.Exit.Address, "port": f.Exit.Port, "method": SSMethod, "password": f.Exit.Key},
				}}})
				rules = append(rules, obj{"type": "field", "inboundTag": []string{f.entryTag()}, "outboundTag": f.tunnelTag()})
			}
		}
		for _, f := range plan.Exit {
			list = append(list, obj{"tag": f.exitTag(), "listen": "0.0.0.0", "port": f.Exit.Port, "protocol": "shadowsocks",
				"settings": obj{"method": SSMethod, "password": f.Exit.Key, "network": f.Network}})
		}
		out = obj{"log": obj{"loglevel": "warning"}, "inbounds": list, "outbounds": outbounds,
			// Per-user traffic: the Agent reads and resets these counters
			// through the stats API (loopback only) at every sync.
			"api":   obj{"tag": "api", "listen": fmt.Sprintf("127.0.0.1:%d", StatsAPIPort), "services": []string{"StatsService"}},
			"stats": obj{},
			"policy": obj{"levels": obj{"0": obj{"statsUserUplink": true, "statsUserDownlink": true}},
				"system": obj{"statsInboundUplink": true, "statsInboundDownlink": true}},
		}
		if len(rules) > 0 {
			out["routing"] = obj{"rules": rules}
		}
	case KernelSingBox:
		list := []any{}
		for _, in := range inbounds {
			list = append(list, singBoxInbound(in, in.Members(members)))
		}
		out = obj{"log": obj{"level": "warn"}, "inbounds": list, "outbounds": []any{obj{"type": "direct", "tag": "direct"}}}
	default:
		return nil, fmt.Errorf("未知内核 %q", kernel)
	}
	return json.MarshalIndent(out, "", "  ")
}

// Hash is the SHA-256 of a rendered config.
func Hash(config []byte) string {
	sum := sha256.Sum256(config)
	return hex.EncodeToString(sum[:])
}

func xrayInbound(in Inbound, members []Member) obj {
	tag := "in-" + in.ID
	switch in.Protocol {
	case ProtocolVLESSReality:
		clients := []any{}
		for _, m := range members {
			clients = append(clients, obj{"id": m.UUID, "flow": VLESSFlow, "email": m.ID})
		}
		return obj{"tag": tag, "listen": "0.0.0.0", "port": in.Port, "protocol": "vless",
			"settings": obj{"clients": clients, "decryption": "none"},
			"streamSettings": obj{"network": "tcp", "security": "reality", "realitySettings": obj{
				"dest": in.Reality.Dest, "serverNames": []string{in.Reality.ServerName}, "privateKey": in.Reality.PrivateKey, "shortIds": []string{in.Reality.ShortID},
			}},
			"sniffing": obj{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true},
		}
	default: // ProtocolSS2022
		clients := []any{}
		for _, m := range members {
			clients = append(clients, obj{"password": m.Key, "email": m.ID})
		}
		return obj{"tag": tag, "listen": "0.0.0.0", "port": in.Port, "protocol": "shadowsocks",
			"settings": obj{"method": SSMethod, "password": in.SS.ServerKey, "clients": clients, "network": "tcp,udp"},
		}
	}
}

func singBoxInbound(in Inbound, members []Member) obj {
	tag := "in-" + in.ID
	switch in.Protocol {
	case ProtocolVLESSReality:
		users := []any{}
		for _, m := range members {
			users = append(users, obj{"name": m.ID, "uuid": m.UUID, "flow": VLESSFlow})
		}
		host, port, _ := net.SplitHostPort(in.Reality.Dest)
		p, _ := strconv.Atoi(port)
		return obj{"type": "vless", "tag": tag, "listen": "::", "listen_port": in.Port, "users": users,
			"tls": obj{"enabled": true, "server_name": in.Reality.ServerName, "reality": obj{
				"enabled": true, "handshake": obj{"server": host, "server_port": p}, "private_key": in.Reality.PrivateKey, "short_id": []string{in.Reality.ShortID},
			}},
		}
	default:
		users := []any{}
		for _, m := range members {
			users = append(users, obj{"name": m.ID, "password": m.Key})
		}
		return obj{"type": "shadowsocks", "tag": tag, "listen": "::", "listen_port": in.Port, "method": SSMethod, "password": in.SS.ServerKey, "users": users}
	}
}
