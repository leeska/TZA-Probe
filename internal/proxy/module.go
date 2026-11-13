package proxy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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
		forwards, err := m.Store.Forwards(req.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"inbounds": inbounds, "forwards": forwards, "nodes": m.NodeViews(), "kernels": kernels})
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
	saveForward := func(w http.ResponseWriter, req *http.Request, id string, code int) {
		var f Forward
		if !httpjson.Decode(w, req, &f) {
			return
		}
		f.ID = id
		for _, node := range []string{f.NodeID, f.exitNode()} {
			if node == "" {
				continue
			}
			if ok, err := m.nodeExists(req.Context(), node); err != nil {
				fail(w, err)
				return
			} else if !ok {
				httpjson.Error(w, 400, "节点不存在")
				return
			}
		}
		if f.UserID != "" {
			if _, err := m.Users.Get(req.Context(), f.UserID); errors.Is(err, users.ErrNotFound) {
				httpjson.Error(w, 400, "使用者不存在")
				return
			} else if err != nil {
				fail(w, err)
				return
			}
		}
		saved, err := m.Store.SaveForward(req.Context(), f)
		if errors.Is(err, ErrForwardNotFound) {
			httpjson.Error(w, 404, err.Error())
			return
		} else if err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		m.Kick()
		httpjson.Write(w, code, saved)
	}
	r.Admin("POST /api/v1/admin/proxy/forwards", func(w http.ResponseWriter, req *http.Request) { saveForward(w, req, "", 201) })
	r.Admin("PUT /api/v1/admin/proxy/forwards/{id}", func(w http.ResponseWriter, req *http.Request) {
		saveForward(w, req, req.PathValue("id"), 200)
	})
	r.Admin("DELETE /api/v1/admin/proxy/forwards/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := m.Store.DeleteForward(req.Context(), req.PathValue("id")); errors.Is(err, ErrForwardNotFound) {
			httpjson.Error(w, 404, err.Error())
			return
		} else if err != nil {
			fail(w, err)
			return
		}
		m.Kick()
		w.WriteHeader(204)
	})
	// 订阅：只凭使用者的 token，不需要登录（前台需要登录时也照常可用）。
	r.Public("GET /api/v1/sub/{token}", m.subscription)
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
	card := module.Card{ID: "proxy", Title: "代理与转发", Page: "proxy"}
	enabled := 0
	for _, in := range inbounds {
		if in.Enabled {
			enabled++
		}
	}
	forwards, err := m.Store.Forwards(ctx)
	if err != nil {
		return nil, err
	}
	forwarding := 0
	for _, f := range forwards {
		if f.Enabled {
			forwarding++
		}
	}
	value := fmt.Sprintf("%d 个入站", enabled)
	if forwarding > 0 {
		value += fmt.Sprintf(" · %d 条转发", forwarding)
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
	case len(inbounds) == 0 && len(forwards) == 0:
		card.Value, card.Note = "0", "还没有入站或转发"
	case problems > 0:
		card.Value, card.Tone, card.Note = value, "bad", fmt.Sprintf("%d 台节点同步出错", problems)
	default:
		card.Value, card.Tone, card.Note = value, "good", "各节点配置已同步"
	}
	return []module.Card{card}, nil
}

// subscription serves a user's subscription in the format the client wants
// (?format=clash|singbox|base64, else from the User-Agent; ?mode=global
// proxies everything). The subscription-userinfo header carries usage,
// quota and expiry, which most clients show.
func (m *Module) subscription(w http.ResponseWriter, req *http.Request) {
	u, err := m.Users.ByToken(req.Context(), req.PathValue("token"))
	if errors.Is(err, users.ErrNotFound) {
		http.Error(w, "订阅不存在或链接已更换", 404)
		return
	} else if err != nil {
		http.Error(w, "读取失败", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch u.Status(time.Now()) {
	case users.StatusDisabled:
		http.Error(w, "这个使用者已停用", 403)
		return
	case users.StatusExpired:
		http.Error(w, "这个使用者已过期", 403)
		return
	}
	inbounds, err := m.Store.List(req.Context())
	if err != nil {
		http.Error(w, "读取失败", 500)
		return
	}
	member := MemberOf(u)
	format := FormatFor(req.URL.Query().Get("format"), req.UserAgent())
	mode := ModeRule
	if req.URL.Query().Get("mode") == ModeGlobal {
		mode = ModeGlobal
	}
	body, contentType, err := Subscription(format, mode, Entries(inbounds, member), member)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	info := fmt.Sprintf("upload=%d; download=%d; total=%d", u.TrafficUp, u.TrafficDown, u.TrafficLimit)
	if t, err := time.Parse(time.RFC3339, u.ExpiresAt); err == nil {
		info += "; expire=" + strconv.FormatInt(t.Unix(), 10)
	}
	w.Header().Set("Subscription-Userinfo", info)
	w.Header().Set("Profile-Update-Interval", "12")
	title := "TZA · " + u.Name
	w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(title)))
	ext := map[string]string{FormatClash: ".yaml", FormatSingBox: ".json", FormatBase64: ".txt"}[format]
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(title+ext))
	w.Header().Set("Content-Type", contentType)
	w.Write(body)
}
