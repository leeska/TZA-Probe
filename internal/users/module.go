package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/httpjson"
	"github.com/leeska/TZA-ControlCenter/internal/module"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

// Module serves 使用者. Store is nil until the database is open (only
// Migrations is used then).
type Module struct {
	Store *Store
	Now   func() time.Time // tests; nil is time.Now
	// OnChange runs after a user is added, edited, deleted or gets a new
	// token or period (the proxy module resyncs its configurations).
	OnChange func()
}

func (m *Module) changed() {
	if m.OnChange != nil {
		m.OnChange()
	}
}

func (*Module) ID() string                      { return "users" }
func (*Module) Migrations() []storage.Migration { return Migrations() }

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// View is a user as the admin API returns it: the stored fields plus the
// derived status and the end of the current period.
type View struct {
	User
	Status    string `json:"status"`
	NextReset int64  `json:"next_reset"`
}

func (m *Module) view(u User) View {
	return View{User: u, Status: u.Status(m.now()), NextReset: u.NextReset()}
}

// Public is what the token holder sees on /u/{token}: no id, no note.
type Public struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	TrafficLimit int64  `json:"traffic_limit"`
	TrafficUsed  int64  `json:"traffic_used"`
	TrafficUp    int64  `json:"traffic_up"`
	TrafficDown  int64  `json:"traffic_down"`
	ResetDay     int    `json:"reset_day"`
	PeriodStart  int64  `json:"period_start"`
	NextReset    int64  `json:"next_reset"`
	ExpiresAt    string `json:"expires_at"`
}

func (m *Module) Routes(r module.Routes) {
	if m.Store == nil {
		return
	}
	s := m.Store
	fail := func(w http.ResponseWriter, err error) {
		if errors.Is(err, ErrNotFound) {
			httpjson.Error(w, 404, err.Error())
			return
		}
		httpjson.Fail(w, err)
	}
	r.Admin("GET /api/v1/admin/users", func(w http.ResponseWriter, req *http.Request) {
		list, err := s.List(req.Context())
		if err != nil {
			fail(w, err)
			return
		}
		out := make([]View, 0, len(list))
		for _, u := range list {
			out = append(out, m.view(u))
		}
		httpjson.Write(w, 200, out)
	})
	r.Admin("POST /api/v1/admin/users", func(w http.ResponseWriter, req *http.Request) {
		var in User
		if !httpjson.Decode(w, req, &in) {
			return
		}
		u, err := s.Create(req.Context(), in, m.now())
		if err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		m.changed()
		httpjson.Write(w, 201, m.view(u))
	})
	r.Admin("PUT /api/v1/admin/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		var in User
		if !httpjson.Decode(w, req, &in) {
			return
		}
		if err := in.Validate(); err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		u, err := s.Update(req.Context(), req.PathValue("id"), in)
		if err != nil {
			fail(w, err)
			return
		}
		m.changed()
		httpjson.Write(w, 200, m.view(u))
	})
	r.Admin("DELETE /api/v1/admin/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := s.Delete(req.Context(), req.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		m.changed()
		w.WriteHeader(204)
	})
	r.Admin("POST /api/v1/admin/users/{id}/token", func(w http.ResponseWriter, req *http.Request) {
		u, err := s.RotateToken(req.Context(), req.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		m.changed()
		httpjson.Write(w, 200, m.view(u))
	})
	r.Admin("POST /api/v1/admin/users/{id}/reset", func(w http.ResponseWriter, req *http.Request) {
		u, err := s.ResetUsage(req.Context(), req.PathValue("id"), m.now())
		if err != nil {
			fail(w, err)
			return
		}
		m.changed()
		httpjson.Write(w, 200, m.view(u))
	})
	// 使用者自己看用量：只凭订阅 token，不需要后台登录（前台需要登录时也照常可用）。
	r.Public("GET /api/v1/u/{token}", func(w http.ResponseWriter, req *http.Request) {
		u, err := s.ByToken(req.Context(), req.PathValue("token"))
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpjson.Write(w, 200, Public{Name: u.Name, Status: u.Status(m.now()), TrafficLimit: u.TrafficLimit, TrafficUsed: u.TrafficUsed, TrafficUp: u.TrafficUp, TrafficDown: u.TrafficDown, ResetDay: u.ResetDay, PeriodStart: u.PeriodStart, NextReset: u.NextReset(), ExpiresAt: u.ExpiresAt})
	})
}

// Run starts new periods on each reset day until ctx ends.
func (m *Module) Run(ctx context.Context) {
	if m.Store == nil {
		return
	}
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		if n, err := m.Store.RollPeriods(ctx, m.now()); err != nil && ctx.Err() == nil {
			slog.Warn("users: roll periods", "error", err)
		} else if n > 0 {
			slog.Info("users: new traffic period", "users", n)
			m.changed()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Overview is one card: how many users, and who is expired, over quota or
// expiring within a week.
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	if m.Store == nil {
		return nil, nil
	}
	list, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	card := module.Card{ID: "users", Title: "使用者", Page: "users"}
	now := m.now()
	active, attention := 0, 0
	for _, u := range list {
		status := u.Status(now)
		note, tone := "", ""
		switch status {
		case StatusActive:
			active++
			if t, err := time.Parse(time.RFC3339, u.ExpiresAt); err == nil && t.Sub(now) < 7*24*time.Hour {
				note, tone = fmt.Sprintf("%s 到期", t.Local().Format("01-02")), "warn"
			} else if u.TrafficLimit > 0 && u.TrafficUsed*10 >= u.TrafficLimit*9 {
				note, tone = fmt.Sprintf("流量已用 %.0f%%", float64(u.TrafficUsed)*100/float64(u.TrafficLimit)), "warn"
			}
		case StatusExpired:
			note, tone = "已过期", "bad"
		case StatusExceeded:
			note, tone = "流量已用完", "bad"
		}
		if note != "" {
			attention++
			if len(card.Items) < module.MaxCardItems {
				card.Items = append(card.Items, module.CardItem{Label: u.Name, Note: note, Tone: tone})
			}
		}
	}
	switch {
	case len(list) == 0:
		card.Value, card.Note = "0", "还没有使用者"
	default:
		card.Value = fmt.Sprintf("%d / %d 可用", active, len(list))
		card.Tone = "good"
		card.Note = "没有需要处理的"
		if attention > 0 {
			card.Tone, card.Note = "warn", fmt.Sprintf("%d 位需要关注", attention)
		}
	}
	return []module.Card{card}, nil
}
