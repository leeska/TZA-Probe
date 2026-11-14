package sites

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/certs"
	"github.com/leeska/TZA-ControlCenter/internal/channel"
	"github.com/leeska/TZA-ControlCenter/internal/httpjson"
	"github.com/leeska/TZA-ControlCenter/internal/model"
	"github.com/leeska/TZA-ControlCenter/internal/module"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

// Opener opens a management channel stream (channel.Hub).
type Opener interface {
	Open(ctx context.Context, node, kind string, params any) (net.Conn, error)
	Status(node string) channel.Status
}

// NodeLister is the part of the core store the module reads.
type NodeLister interface {
	Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error)
}

// Module serves 网站. Store is nil until the database is open.
type Module struct {
	Store *Store
	Hub   Opener
	Nodes NodeLister
	// Certs links TLS sites to the certificates module; CertsKick asks it to
	// deploy now.
	Certs     *certs.Store
	CertsKick func()
	kick      chan struct{}
	run       sync.Mutex
	mu        sync.Mutex
	views     map[string]SiteView // site id → state after the last sync
	nodes     map[string]NodeView // node id → nginx state
	stale     map[string]bool     // nodes that lost their last site: clear them
}

func (*Module) ID() string                      { return "sites" }
func (*Module) Migrations() []storage.Migration { return Migrations() }

// Init makes the kick channel; call before Run and Routes.
func (m *Module) Init() {
	m.kick = make(chan struct{}, 1)
	m.views, m.nodes, m.stale = map[string]SiteView{}, map[string]NodeView{}, map[string]bool{}
}

func (m *Module) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Site states.
const (
	StateOK       = "ok"
	StateWaiting  = "waiting" // its certificate is not on the node yet
	StateError    = "error"
	StateDisabled = "disabled"
)

// SiteView is a site's state after the last sync.
type SiteView struct {
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// NodeView is a node's nginx as the last sync saw it.
type NodeView struct {
	Capable   bool                `json:"capable"`
	Installed bool                `json:"installed"`
	Version   string              `json:"version,omitempty"`
	Active    bool                `json:"active"`
	Error     string              `json:"error,omitempty"`
	Output    string              `json:"output,omitempty"`
	Others    []channel.NginxSite `json:"others"`
	At        int64               `json:"synced_at,omitempty"`
}

// Run syncs now, after every Kick and once a minute.
func (m *Module) Run(ctx context.Context) {
	if m.Store == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := m.Sync(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("sites sync", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.kick:
		}
	}
}

func (m *Module) capable(node string) bool {
	st := m.Hub.Status(node)
	return st.Connected && slices.Contains(st.Capabilities, channel.CapabilitySites)
}

func (m *Module) call(ctx context.Context, node, kind string, params any, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := m.Hub.Open(ctx, node, kind, params)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Minute))
	if body != nil {
		if _, err := conn.Write(body); err != nil {
			return err
		}
	}
	return channel.ReadLine(conn, out)
}

// Sync brings every node's managed nginx files in line with the sites.
func (m *Module) Sync(ctx context.Context) error {
	m.run.Lock()
	defer m.run.Unlock()
	all, err := m.Store.List(ctx)
	if err != nil {
		return err
	}
	byNode := map[string][]Site{}
	for _, s := range all {
		byNode[s.NodeID] = append(byNode[s.NodeID], s)
	}
	m.mu.Lock()
	for node := range m.stale {
		if _, ok := byNode[node]; !ok {
			byNode[node] = nil
		}
	}
	m.mu.Unlock()
	views := map[string]SiteView{}
	nodes := map[string]NodeView{}
	for node, list := range byNode {
		nv, sv := m.syncNode(ctx, node, list)
		nodes[node] = nv
		for id, v := range sv {
			views[id] = v
		}
		if len(list) == 0 && nv.Error == "" {
			m.mu.Lock()
			delete(m.stale, node)
			m.mu.Unlock()
		}
	}
	m.mu.Lock()
	m.views = views
	for id, v := range nodes {
		m.nodes[id] = v
	}
	m.mu.Unlock()
	return nil
}

func (m *Module) syncNode(ctx context.Context, node string, list []Site) (NodeView, map[string]SiteView) {
	views := map[string]SiteView{}
	nv := NodeView{Others: []channel.NginxSite{}}
	setAll := func(state, note string) {
		for _, s := range list {
			if s.Enabled {
				views[s.ID] = SiteView{State: state, Note: note}
			} else {
				views[s.ID] = SiteView{State: StateDisabled}
			}
		}
	}
	if !m.capable(node) {
		nv.Error = "节点没有连接管理通道或没开网站能力：在节点上重跑接入命令并加 --manage"
		setAll(StateError, nv.Error)
		return nv, views
	}
	nv.Capable = true
	var st channel.SitesStatus
	if err := m.call(ctx, node, channel.KindSitesStatus, nil, nil, &st); err != nil {
		nv.Error = "读取 nginx 状态失败：" + err.Error()
		setAll(StateError, nv.Error)
		return nv, views
	}
	nv.Installed, nv.Version, nv.Active, nv.At = st.Installed, st.Version, st.Active, time.Now().Unix()
	for _, o := range st.Sites {
		if !o.Managed {
			nv.Others = append(nv.Others, o)
		}
	}
	if !st.Installed {
		nv.Error = "这台节点没有安装 nginx"
		setAll(StateError, nv.Error)
		return nv, views
	}
	files := map[string]string{}
	for _, s := range list {
		if !s.Enabled {
			views[s.ID] = SiteView{State: StateDisabled}
			continue
		}
		crt, key := "", ""
		if s.TLS == TLSCert {
			c, waiting := m.certFor(ctx, s)
			if waiting != "" {
				views[s.ID] = SiteView{State: StateWaiting, Note: waiting}
				continue
			}
			crt, key = s.Paths(c)
		} else {
			crt, key = s.Paths(nil)
		}
		files[s.FileName()] = s.Config(crt, key, st.Version)
		views[s.ID] = SiteView{State: StateOK}
	}
	if !sameFiles(files, st.Managed) {
		body, _ := json.Marshal(files)
		sum := sha256.Sum256(body)
		var res channel.SitesResult
		err := m.call(ctx, node, channel.KindSitesApply, channel.SitesApplyParams{Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}, body, &res)
		if err == nil && res.Error != "" {
			err = errors.New(res.Error)
		}
		if err != nil {
			nv.Error, nv.Output = err.Error(), res.Output
			for id, v := range views {
				if v.State == StateOK {
					views[id] = SiteView{State: StateError, Note: err.Error()}
				}
			}
		}
	}
	return nv, views
}

// certFor finds a TLS site's certificate on its node; waiting says why the
// site cannot be served yet.
func (m *Module) certFor(ctx context.Context, s Site) (*certs.Cert, string) {
	if m.Certs == nil {
		return nil, "证书模块不可用"
	}
	c, err := m.Certs.Cert(ctx, s.CertID)
	if err != nil {
		return nil, "选的证书已被删除"
	}
	if c.CertPEM == "" {
		return nil, "证书还没签发成功"
	}
	dir := certs.DefaultDir(c.Domains[0])
	for _, t := range c.Targets {
		if t.NodeID == s.NodeID && t.Dir == dir {
			if t.Deployed == c.Serial {
				return &c, ""
			}
			if t.Error != "" {
				return nil, "证书下发失败：" + t.Error
			}
			return nil, "等证书下发到这台节点"
		}
	}
	return nil, "等证书下发到这台节点"
}

func sameFiles(want map[string]string, have map[string]string) bool {
	if len(want) != len(have) {
		return false
	}
	for name, content := range want {
		sum := sha256.Sum256([]byte(content))
		if have[name] != hex.EncodeToString(sum[:]) {
			return false
		}
	}
	return true
}

// linkCert adds the site's node as a target of its certificate, reloading
// nginx after each renewal, and asks the certificates module to deploy.
func (m *Module) linkCert(ctx context.Context, s Site) error {
	if s.TLS != TLSCert || !s.Enabled || m.Certs == nil {
		return nil
	}
	c, err := m.Certs.Cert(ctx, s.CertID)
	if err != nil {
		return errors.New("证书不存在")
	}
	if !certCovers(c.Domains, s.Domains) {
		return errors.New("这张证书不包含站点的全部域名：" + uncovered(c.Domains, s.Domains))
	}
	added, err := m.Certs.EnsureTarget(ctx, c.ID, s.NodeID, certs.DefaultDir(c.Domains[0]), "systemctl reload nginx || nginx -s reload")
	if err != nil {
		return err
	}
	if added && m.CertsKick != nil {
		m.CertsKick()
	}
	return nil
}

// certCovers reports whether certificate names cover every site domain (a
// wildcard covers one label).
func certCovers(cert, domains []string) bool { return uncovered(cert, domains) == "" }

func uncovered(cert, domains []string) string {
	for _, d := range domains {
		ok := slices.Contains(cert, d)
		if !ok {
			if i := indexDot(d); i > 0 {
				ok = slices.Contains(cert, "*"+d[i:])
			}
		}
		if !ok {
			return d
		}
	}
	return ""
}

func indexDot(s string) int {
	for i := range s {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// Routes: list and edit sites, and look at a node's nginx.
func (m *Module) Routes(r module.Routes) {
	if m.Store == nil {
		return
	}
	r.Admin("GET /api/v1/admin/sites", func(w http.ResponseWriter, req *http.Request) {
		list, err := m.Store.List(req.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		m.mu.Lock()
		views := map[string]SiteView{}
		for k, v := range m.views {
			views[k] = v
		}
		nodes := map[string]NodeView{}
		for k, v := range m.nodes {
			nodes[k] = v
		}
		m.mu.Unlock()
		capable := []string{}
		all, _ := m.Nodes.Nodes(req.Context(), time.Now())
		for _, n := range all {
			if m.capable(n.ID) {
				capable = append(capable, n.ID)
			}
		}
		sort.Strings(capable)
		httpjson.Write(w, 200, map[string]any{"sites": list, "views": views, "nodes": nodes, "capable": capable})
	})
	// 一台节点上的 nginx：版本、是否运行、不是面板建的站点（只读）。
	r.Admin("GET /api/v1/admin/sites/nodes/{node}", func(w http.ResponseWriter, req *http.Request) {
		node := req.PathValue("node")
		if !m.capable(node) {
			httpjson.Error(w, 409, "节点没有连接管理通道或没开网站能力")
			return
		}
		var st channel.SitesStatus
		if err := m.call(req.Context(), node, channel.KindSitesStatus, nil, nil, &st); err != nil {
			httpjson.Error(w, 502, err.Error())
			return
		}
		httpjson.Write(w, 200, st)
	})
	save := func(w http.ResponseWriter, req *http.Request, id string, code int) {
		var s Site
		if !httpjson.Decode(w, req, &s) {
			return
		}
		s.ID = id
		nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		if !slices.ContainsFunc(nodes, func(n model.NodeView) bool { return n.ID == s.NodeID }) {
			httpjson.Error(w, 400, "节点不存在")
			return
		}
		if err := s.Validate(); err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		if err := m.linkCert(req.Context(), s); err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		var previous *Site
		if id != "" {
			if old, err := m.Store.Get(req.Context(), id); err == nil {
				previous = &old
			}
		}
		saved, err := m.Store.Save(req.Context(), s)
		if errors.Is(err, ErrNotFound) {
			httpjson.Error(w, 404, err.Error())
			return
		} else if err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		if previous != nil && previous.NodeID != saved.NodeID {
			m.mu.Lock()
			m.stale[previous.NodeID] = true
			m.mu.Unlock()
		}
		m.Kick()
		httpjson.Write(w, code, saved)
	}
	r.Admin("POST /api/v1/admin/sites", func(w http.ResponseWriter, req *http.Request) { save(w, req, "", 201) })
	r.Admin("PUT /api/v1/admin/sites/{id}", func(w http.ResponseWriter, req *http.Request) { save(w, req, req.PathValue("id"), 200) })
	r.Admin("DELETE /api/v1/admin/sites/{id}", func(w http.ResponseWriter, req *http.Request) {
		site, err := m.Store.Delete(req.Context(), req.PathValue("id"))
		if errors.Is(err, ErrNotFound) {
			httpjson.Error(w, 404, err.Error())
			return
		} else if err != nil {
			httpjson.Fail(w, err)
			return
		}
		m.mu.Lock()
		m.stale[site.NodeID] = true
		m.mu.Unlock()
		m.Kick()
		w.WriteHeader(204)
	})
	// 立即同步：等这一轮做完（站点不多，通常几秒）。
	r.Admin("POST /api/v1/admin/sites/sync", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 3*time.Minute)
		defer cancel()
		if err := m.Sync(ctx); err != nil {
			httpjson.Fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]bool{"ok": true})
	})
}

// Overview is one card: sites serving and those with a problem.
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	if m.Store == nil {
		return nil, nil
	}
	list, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	card := module.Card{ID: "sites", Title: "网站", Page: "sites"}
	if len(list) == 0 {
		card.Value, card.Note = "0", "还没有网站"
		return []module.Card{card}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ok, problems := 0, 0
	for _, s := range list {
		v := m.views[s.ID]
		switch v.State {
		case StateOK:
			ok++
		case StateError, StateWaiting:
			problems++
			if len(card.Items) < module.MaxCardItems {
				tone := "bad"
				if v.State == StateWaiting {
					tone = "warn"
				}
				card.Items = append(card.Items, module.CardItem{Label: s.Name, Note: v.Note, Tone: tone})
			}
		}
	}
	card.Value = fmt.Sprintf("%d / %d 运行", ok, len(list))
	card.Tone, card.Note = "good", "nginx 配置已同步"
	if problems > 0 {
		card.Tone, card.Note = "warn", fmt.Sprintf("%d 个需要关注", problems)
	}
	return []module.Card{card}, nil
}
