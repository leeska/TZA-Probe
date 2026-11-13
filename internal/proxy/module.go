package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/leeska/TZA-Probe/internal/httpjson"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/users"
)

// Module serves 代理. Fields are nil until the database is open (only
// Migrations is used then).
type Module struct {
	Store   *Store
	Users   *users.Store
	Hub     Opener
	Nodes   NodeLister
	Kernels *Cache
	state   syncState
}

func (*Module) ID() string                      { return "proxy" }
func (*Module) Migrations() []storage.Migration { return Migrations() }

// Init makes the kick channel; call before Run and Routes.
func (m *Module) Init() { m.state.kick = make(chan struct{}, 1) }

// KernelInfo is a pinned kernel as the panel lists it.
type KernelInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Link is one user's share link on an inbound.
type Link struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Link   string `json:"link"`
}

func (m *Module) Routes(r module.Routes) {
	if m.Store == nil {
		return
	}
	fail := func(w http.ResponseWriter, err error) {
		if errors.Is(err, ErrNotFound) {
			httpjson.Error(w, 404, err.Error())
			return
		}
		httpjson.Fail(w, err)
	}
	r.Admin("GET /api/v1/admin/proxy", func(w http.ResponseWriter, req *http.Request) {
		inbounds, err := m.Store.List(req.Context())
		if err != nil {
			fail(w, err)
			return
		}
		kernels := []KernelInfo{}
		for _, name := range []string{KernelXray, KernelSingBox} {
			kernels = append(kernels, KernelInfo{Name: name, Version: Kernels[name].Version})
		}
		httpjson.Write(w, 200, map[string]any{"inbounds": inbounds, "nodes": m.NodeViews(), "kernels": kernels})
	})
	save := func(w http.ResponseWriter, req *http.Request, id string, code int) {
		var in Inbound
		if !httpjson.Decode(w, req, &in) {
			return
		}
		in.ID = id
		if ok, err := m.nodeExists(req.Context(), in.NodeID); err != nil {
			fail(w, err)
			return
		} else if !ok {
			httpjson.Error(w, 400, "节点不存在")
			return
		}
		saved, err := m.Store.Save(req.Context(), in)
		if errors.Is(err, ErrNotFound) {
			fail(w, err)
			return
		} else if err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		m.Kick()
		httpjson.Write(w, code, saved)
	}
	r.Admin("POST /api/v1/admin/proxy/inbounds", func(w http.ResponseWriter, req *http.Request) { save(w, req, "", 201) })
	r.Admin("PUT /api/v1/admin/proxy/inbounds/{id}", func(w http.ResponseWriter, req *http.Request) { save(w, req, req.PathValue("id"), 200) })
	r.Admin("DELETE /api/v1/admin/proxy/inbounds/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := m.Store.Delete(req.Context(), req.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		m.Kick()
		w.WriteHeader(204)
	})
	r.Admin("GET /api/v1/admin/proxy/inbounds/{id}/links", func(w http.ResponseWriter, req *http.Request) {
		in, err := m.Store.get(req.Context(), req.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		if in.Address == "" {
			httpjson.Error(w, 409, "先给这个入站填写连接地址（域名或 IP）")
			return
		}
		members, err := m.members(req.Context())
		if err != nil {
			fail(w, err)
			return
		}
		links := []Link{}
		for _, mem := range in.Members(members) {
			links = append(links, Link{UserID: mem.ID, Name: mem.Name, Link: in.ShareLink(mem)})
		}
		httpjson.Write(w, 200, links)
	})
	// 立即同步：等这一轮做完再回，后台据此刷新状态。
	r.Admin("POST /api/v1/admin/proxy/sync", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 10*time.Minute)
		defer cancel()
		if err := m.Sync(ctx); err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"nodes": m.NodeViews()})
	})
}

func (m *Module) nodeExists(ctx context.Context, id string) (bool, error) {
	nodes, err := m.Nodes.Nodes(ctx, time.Now())
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(nodes, func(n model.NodeView) bool { return n.ID == id }), nil
}

// Overview is one card: inbounds in service and nodes with a problem.
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	if m.Store == nil {
		return nil, nil
	}
	inbounds, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	card := module.Card{ID: "proxy", Title: "代理", Page: "proxy"}
	enabled := 0
	for _, in := range inbounds {
		if in.Enabled {
			enabled++
		}
	}
	problems := 0
	for _, v := range m.NodeViews() {
		msg := v.Error
		for name, k := range v.Kernels {
			if msg == "" && k.Error != "" {
				msg = name + "：" + k.Error
			}
		}
		if msg != "" {
			problems++
			if len(card.Items) < module.MaxCardItems {
				card.Items = append(card.Items, module.CardItem{Label: v.Name, Note: msg, Tone: "bad"})
			}
		}
	}
	switch {
	case len(inbounds) == 0:
		card.Value, card.Note = "0", "还没有入站"
	case problems > 0:
		card.Value, card.Tone, card.Note = fmt.Sprintf("%d 个入站", enabled), "bad", fmt.Sprintf("%d 台节点同步出错", problems)
	default:
		card.Value, card.Tone, card.Note = fmt.Sprintf("%d 个入站", enabled), "good", "各节点配置已同步"
	}
	return []module.Card{card}, nil
}
