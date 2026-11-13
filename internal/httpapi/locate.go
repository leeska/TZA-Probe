package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

// agentAddr is the public address an Agent last connected from: what the
// node form's 自动检测 looks up. Only kept in memory; a restart forgets it
// until the next report a few seconds later.
type agentAddr struct {
	IP string
	At int64
}

type agentAddrs struct {
	mu   sync.Mutex
	byID map[string]agentAddr
}

func (s *agentAddrs) set(node, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = map[string]agentAddr{}
	}
	s.byID[node] = agentAddr{IP: ip, At: time.Now().Unix()}
}

func (s *agentAddrs) get(node string) (agentAddr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byID[node]
	return v, ok
}

// publicClientIP is the request's public source address: the peer itself, or,
// when the peer is a local reverse proxy, the last X-Forwarded-For entry (the
// address nginx saw). Anything not public returns "".
func publicClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	if peer.IsLoopback() || peer.IsPrivate() {
		forwarded := r.Header.Get("X-Forwarded-For")
		if forwarded == "" {
			return ""
		}
		parts := strings.Split(forwarded, ",")
		if peer, err = netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err != nil {
			return ""
		}
		peer = peer.Unmap()
	}
	if !ipguard.PublicAddress(peer) {
		return ""
	}
	return peer.String()
}

// locateNode answers GET /api/v1/admin/nodes/{id}/locate: where the node's
// Agent connects from (ipwho.is, Chinese names), for 节点所在地区 and the
// region drift target.
func (a *API) locateNode(w http.ResponseWriter, r *http.Request) {
	addr, ok := a.agentAddrs.get(r.PathValue("id"))
	if !ok || addr.IP == "" {
		write(w, 409, map[string]string{"error": "还没有收到这台节点 Agent 的公网连接，接入并上报后再试"})
		return
	}
	place, err := a.locator().Describe(r.Context(), addr.IP)
	if err != nil {
		write(w, 502, map[string]string{"error": "定位失败：" + err.Error()})
		return
	}
	write(w, 200, place)
}

// nodeAddress answers GET /api/v1/admin/nodes/{id}/address: the public IP
// the node's Agent last connected from (for filling connection addresses).
func (a *API) nodeAddress(w http.ResponseWriter, r *http.Request) {
	addr, ok := a.agentAddrs.get(r.PathValue("id"))
	if !ok || addr.IP == "" {
		write(w, 409, map[string]string{"error": "还没有收到这台节点 Agent 的公网连接，接入并上报后再试"})
		return
	}
	write(w, 200, map[string]string{"ip": addr.IP})
}

func (a *API) locator() *ipguard.Locator {
	a.locatorOnce.Do(func() {
		if a.Locator == nil {
			a.Locator = ipguard.NewLocator()
		}
	})
	return a.Locator
}
