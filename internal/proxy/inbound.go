package proxy

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	ProtocolVLESSReality = "vless-reality"
	ProtocolSS2022       = "ss2022"
	// SSMethod is the only Shadowsocks 2022 method offered: 16-byte keys, the
	// size users.User.ProxyKey has.
	SSMethod = "2022-blake3-aes-128-gcm"
	// VLESSFlow is the flow every VLESS Reality user gets.
	VLESSFlow = "xtls-rprx-vision"
)

// Inbound is one listening port on a node.
type Inbound struct {
	ID       string `json:"id"`
	NodeID   string `json:"node_id"`
	Name     string `json:"name"`
	Kernel   string `json:"kernel"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Enabled  bool   `json:"enabled"`
	// Address is what clients connect to (a domain or the node's IP); it only
	// goes into share links and subscriptions.
	Address string `json:"address"`
	// UserIDs limits the inbound to these users; empty is every user.
	UserIDs []string `json:"user_ids"`
	Reality *Reality `json:"reality,omitempty"`
	SS      *SS      `json:"ss,omitempty"`
	Created int64    `json:"created_at"`
}

// Reality borrows a real TLS site's handshake. Dest is that site (host:port);
// ServerName is the SNI clients send. The key pair and short id are made by
// the Server.
type Reality struct {
	Dest       string `json:"dest"`
	ServerName string `json:"server_name"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	ShortID    string `json:"short_id"`
}

// SS holds the Shadowsocks 2022 server key (base64 of 16 bytes).
type SS struct {
	ServerKey string `json:"server_key"`
}

const MaxInboundName = 40

// Prepare checks what the administrator sent and fills the secrets a new
// inbound needs (keeping existing ones from previous).
func (in *Inbound) Prepare(previous *Inbound) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > MaxInboundName {
		return fmt.Errorf("名称需要 1–%d 个字", MaxInboundName)
	}
	if in.NodeID == "" {
		return errors.New("请选择节点")
	}
	if _, ok := Kernels[in.Kernel]; !ok {
		return errors.New("请选择 xray 或 sing-box")
	}
	if in.Port < 1 || in.Port > 65535 {
		return errors.New("端口为 1–65535")
	}
	if in.Address != "" && !validHost(in.Address) {
		return errors.New("连接地址须是域名或 IP")
	}
	seen := map[string]bool{}
	for _, id := range in.UserIDs {
		if id == "" || seen[id] {
			return errors.New("使用者为空或重复")
		}
		seen[id] = true
	}
	switch in.Protocol {
	case ProtocolVLESSReality:
		in.SS = nil
		if in.Reality == nil {
			in.Reality = &Reality{}
		}
		r := in.Reality
		r.Dest, r.ServerName = strings.TrimSpace(r.Dest), strings.TrimSpace(r.ServerName)
		// No default target: the well-known sites everyone copies (microsoft,
		// apple …) are what xray itself warns makes an IP easier to block.
		if r.Dest == "" {
			return errors.New("请填写 Reality 目标网站（域名:443）")
		}
		host, port, err := net.SplitHostPort(r.Dest)
		if err != nil || !validHost(host) {
			return errors.New("Reality 目标须是 域名:端口")
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return errors.New("Reality 目标端口无效")
		}
		if r.ServerName == "" {
			r.ServerName = host
		}
		if !validHost(r.ServerName) {
			return errors.New("SNI 须是域名")
		}
		if previous != nil && previous.Reality != nil && previous.Reality.PrivateKey != "" {
			r.PrivateKey, r.PublicKey, r.ShortID = previous.Reality.PrivateKey, previous.Reality.PublicKey, previous.Reality.ShortID
		} else {
			r.PrivateKey, r.PublicKey = NewRealityKeys()
			var sid [4]byte
			rand.Read(sid[:])
			r.ShortID = hex.EncodeToString(sid[:])
		}
	case ProtocolSS2022:
		in.Reality = nil
		if previous != nil && previous.SS != nil && previous.SS.ServerKey != "" {
			in.SS = &SS{ServerKey: previous.SS.ServerKey}
		} else {
			var key [16]byte
			rand.Read(key[:])
			in.SS = &SS{ServerKey: base64.StdEncoding.EncodeToString(key[:])}
		}
	default:
		return errors.New("请选择 VLESS Reality 或 Shadowsocks 2022")
	}
	return nil
}

func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// NewRealityKeys makes an X25519 key pair in the encoding xray and sing-box
// use (raw URL-safe base64).
func NewRealityKeys() (private, public string) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(key.Bytes()), enc.EncodeToString(key.PublicKey().Bytes())
}

// Member is a user as the inbound configs need them.
type Member struct {
	ID, Name, UUID, Key string
}

// Members picks who may use in: the active users, limited to in.UserIDs when
// set. It keeps the order of all.
func (in Inbound) Members(all []Member) []Member {
	if len(in.UserIDs) == 0 {
		return all
	}
	out := []Member{}
	for _, m := range all {
		if slices.Contains(in.UserIDs, m.ID) {
			out = append(out, m)
		}
	}
	return out
}

// ShareLink is the URI a client imports for member on in.
func (in Inbound) ShareLink(m Member) string {
	host := in.Address
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	name := url.PathEscape(in.Name + " · " + m.Name)
	switch in.Protocol {
	case ProtocolVLESSReality:
		q := url.Values{"encryption": {"none"}, "flow": {VLESSFlow}, "security": {"reality"}, "sni": {in.Reality.ServerName}, "fp": {"chrome"}, "pbk": {in.Reality.PublicKey}, "sid": {in.Reality.ShortID}, "type": {"tcp"}}
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s", m.UUID, host, in.Port, q.Encode(), name)
	case ProtocolSS2022:
		return fmt.Sprintf("ss://%s:%s@%s:%d#%s", SSMethod, url.QueryEscape(in.SS.ServerKey+":"+m.Key), host, in.Port, name)
	}
	return ""
}
