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
// The output is deterministic (sorted keys), so its hash tells whether a node
// is up to date.
func Config(kernel string, inbounds []Inbound, members []Member) ([]byte, error) {
	var out obj
	switch kernel {
	case KernelXray:
		list := []any{}
		for _, in := range inbounds {
			list = append(list, xrayInbound(in, in.Members(members)))
		}
		out = obj{"log": obj{"loglevel": "warning"}, "inbounds": list, "outbounds": []any{obj{"protocol": "freedom", "tag": "direct"}, obj{"protocol": "blackhole", "tag": "block"}}}
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
