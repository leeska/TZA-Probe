// Package certs is the certificates module: the Server issues certificates
// from Let's Encrypt with Cloudflare DNS-01, renews them 30 days before they
// expire and writes them to nodes (the certs capability) followed by a reload
// command.
package certs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/httpjson"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/storage"
)

// NodeLister is the part of the core store the module reads.
type NodeLister interface {
	Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error)
}

// Opener opens a management channel stream (channel.Hub).
type Opener interface {
	Open(ctx context.Context, node, kind string, params any) (net.Conn, error)
	Status(node string) channel.Status
}

// Module serves 证书. Store is nil until the database is open.
type Module struct {
	Store *Store
	Hub   Opener
	Nodes NodeLister
	// NewIssuer and NewDNS make the ACME client and the DNS provider (tests
	// replace them); nil are Let's Encrypt and Cloudflare.
	NewIssuer func(directory string) Issuer
	NewDNS    func(token string) DNSProvider
	// Events records alerts for the notification channels (nil: none).
	Events interface {
		RecordEvent(context.Context, model.Event) error
	}
	kick    chan struct{}
	run     sync.Mutex
	mu      sync.Mutex
	working string // the certificate being issued now
}

func (*Module) ID() string                      { return "certs" }
func (*Module) Migrations() []storage.Migration { return Migrations() }

// Init makes the kick channel; call before Run and Routes.
func (m *Module) Init() { m.kick = make(chan struct{}, 1) }

// Kick asks Run to go through the certificates now.
func (m *Module) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Module) issuer(dir string) Issuer {
	if m.NewIssuer != nil {
		return m.NewIssuer(dir)
	}
	return &ACME{Store: m.Store, Directory: dir}
}

func (m *Module) dns(token string) DNSProvider {
	if m.NewDNS != nil {
		return m.NewDNS(token)
	}
	return &Cloudflare{Token: token}
}

func (m *Module) directory(ctx context.Context) string {
	if v, _ := m.Store.State(ctx, "staging"); v == "1" {
		return DirectoryStaging
	}
	return DirectoryProduction
}

// Run checks every certificate now, after each Kick and every hour.
func (m *Module) Run(ctx context.Context) {
	if m.Store == nil {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := m.Process(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("certs", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.kick:
		}
	}
}

// backoff is the wait after the n-th failure in a row.
func backoff(n int) time.Duration {
	steps := []time.Duration{time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}
	return steps[min(n, len(steps))-1]
}

// Process issues what is due and deploys what changed.
func (m *Module) Process(ctx context.Context) error {
	m.run.Lock()
	defer m.run.Unlock()
	list, err := m.Store.Certs(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.Due(time.Now()) {
			m.issue(ctx, c)
		}
		if c, err = m.Store.Cert(ctx, c.ID); err == nil && c.CertPEM != "" {
			m.deploy(ctx, c)
		}
		if c, err = m.Store.Cert(ctx, c.ID); err == nil {
			m.alert(ctx, c)
		}
	}
	return nil
}

// Problem is what is wrong with c now ("" when nothing): a failed issuance,
// an expired or soon expiring certificate, a failed deployment.
func (m *Module) Problem(ctx context.Context, c Cert, now time.Time) string {
	left := time.Unix(c.NotAfter, 0).Sub(now)
	switch {
	case c.Status == StatusError:
		return "签发失败：" + c.Error
	case c.CertPEM != "" && left <= 0:
		return "证书已过期，自动续期没有成功"
	case c.CertPEM != "" && left < 14*24*time.Hour:
		return fmt.Sprintf("证书 %d 天后到期，自动续期还没有成功", int(left.Hours()/24))
	}
	for _, t := range c.Targets {
		if t.Error != "" {
			return "下发到节点 " + m.nodeName(ctx, t.NodeID) + " 失败：" + t.Error
		}
	}
	return ""
}

func (m *Module) nodeName(ctx context.Context, id string) string {
	if m.Nodes != nil {
		if nodes, err := m.Nodes.Nodes(ctx, time.Now()); err == nil {
			for _, n := range nodes {
				if n.ID == id {
					return n.Name
				}
			}
		}
	}
	return id
}

// alert sends a firing event when c turns bad and a recovery when it is
// good again; nothing while the state holds.
func (m *Module) alert(ctx context.Context, c Cert) {
	if m.Events == nil {
		return
	}
	problem := m.Problem(ctx, c, time.Now())
	if (problem != "") == c.Alerted {
		return
	}
	message := problem
	if problem == "" {
		message = "已恢复正常"
		if c.NotAfter > 0 {
			message += "，证书有效期到 " + time.Unix(c.NotAfter, 0).Local().Format("2006-01-02")
		}
	}
	e := model.Event{NodeID: "cert:" + c.ID, NodeName: "证书 " + strings.Join(c.Domains, "、"), Kind: model.EventCert, Message: message, Active: problem != ""}
	if err := m.Events.RecordEvent(ctx, e); err != nil {
		slog.Warn("certs: alert not recorded", "error", err)
		return
	}
	m.Store.Update(ctx, c.ID, func(c *Cert) { c.Alerted = problem != "" })
}

func (m *Module) issue(ctx context.Context, c Cert) {
	m.mu.Lock()
	m.working = c.ID
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.working = ""
		m.mu.Unlock()
	}()
	log := func(line string) { slog.Info("certs: "+line, "domains", c.Domains) }
	var issued Issued
	account, err := m.Store.DNSAccount(ctx, c.DNSID)
	if err == nil {
		log("开始签发")
		issued, err = m.issuer(m.directory(ctx)).Issue(ctx, c.Domains, m.dns(account.Token), log)
	}
	now := time.Now()
	m.Store.Update(ctx, c.ID, func(c *Cert) {
		c.LastAttempt = now.Unix()
		if err != nil {
			c.Failures++
			c.Status, c.Error = StatusError, err.Error()
			c.NextAttempt = now.Add(backoff(c.Failures)).Unix()
			return
		}
		c.CertPEM, c.KeyPEM, c.Serial, c.Issuer = issued.CertPEM, issued.KeyPEM, issued.Serial, issued.Issuer
		c.NotBefore, c.NotAfter = issued.NotBefore.Unix(), issued.NotAfter.Unix()
		c.Status, c.Error, c.Failures, c.NextAttempt, c.Force = StatusValid, "", 0, 0, false
	})
	if err != nil {
		slog.Warn("certs: issuance failed", "domains", c.Domains, "error", err)
	} else {
		log("签发成功")
	}
}

func (m *Module) capable(node string) bool {
	st := m.Hub.Status(node)
	return st.Connected && slices.Contains(st.Capabilities, channel.CapabilityCerts)
}

// deploy writes the certificate to every target that has an older one.
func (m *Module) deploy(ctx context.Context, c Cert) {
	for i, t := range c.Targets {
		if t.Deployed == c.Serial {
			continue
		}
		var result channel.CertDeployResult
		var err error
		if !m.capable(t.NodeID) {
			err = errors.New("节点没有连接管理通道或没开证书能力：在节点上重跑接入命令并加 --manage")
		} else {
			err = m.call(ctx, t.NodeID, channel.KindCertDeploy, channel.CertDeployParams{Dir: t.Dir, Fullchain: c.CertPEM, Key: c.KeyPEM, Reload: t.Reload}, &result)
		}
		if err == nil && result.Error != "" {
			err = errors.New(result.Error)
		}
		serial := c.Serial
		m.Store.Update(ctx, c.ID, func(c *Cert) {
			if i >= len(c.Targets) || c.Targets[i].NodeID != t.NodeID || c.Targets[i].Dir != t.Dir {
				return // edited meanwhile
			}
			tt := &c.Targets[i]
			tt.Output = result.Output
			if err != nil {
				tt.Error = err.Error()
				// A failed reload still wrote the files; retrying would only
				// rerun a command that fails.
				if result.Error != "" && strings.HasPrefix(result.Error, "证书已写入") {
					tt.Deployed, tt.DeployedAt = serial, time.Now().Unix()
				}
				return
			}
			tt.Error, tt.Deployed, tt.DeployedAt = "", serial, time.Now().Unix()
		})
	}
}

func (m *Module) call(ctx context.Context, node, kind string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := m.Hub.Open(ctx, node, kind, params)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Minute))
	return channel.ReadLine(conn, out)
}

// CertView is a certificate as the panel sees it: no key, no PEM.
type CertView struct {
	Cert
	Issuing bool `json:"issuing"`
}

func (m *Module) view(c Cert) CertView {
	m.mu.Lock()
	issuing := m.working == c.ID
	m.mu.Unlock()
	c.CertPEM, c.KeyPEM = "", ""
	return CertView{Cert: c, Issuing: issuing}
}

// DNSView hides the token but its last characters.
type DNSView struct {
	DNSAccount
	TokenHint string `json:"token_hint"`
}

func dnsView(a DNSAccount) DNSView {
	hint := ""
	if len(a.Token) > 4 {
		hint = "…" + a.Token[len(a.Token)-4:]
	}
	a.Token = ""
	return DNSView{DNSAccount: a, TokenHint: hint}
}

func (m *Module) Routes(r module.Routes) {
	if m.Store == nil {
		return
	}
	fail := func(w http.ResponseWriter, err error) {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrDNSNotFound) {
			httpjson.Error(w, 404, err.Error())
			return
		}
		httpjson.Fail(w, err)
	}
	r.Admin("GET /api/v1/admin/certs", func(w http.ResponseWriter, req *http.Request) {
		certs, err := m.Store.Certs(req.Context())
		if err != nil {
			fail(w, err)
			return
		}
		accounts, err := m.Store.DNSAccounts(req.Context())
		if err != nil {
			fail(w, err)
			return
		}
		out := map[string]any{"certs": []CertView{}, "dns": []DNSView{}, "staging": m.directory(req.Context()) == DirectoryStaging}
		views := []CertView{}
		for _, c := range certs {
			views = append(views, m.view(c))
		}
		dns := []DNSView{}
		for _, a := range accounts {
			dns = append(dns, dnsView(a))
		}
		out["certs"], out["dns"] = views, dns
		httpjson.Write(w, 200, out)
	})
	saveCert := func(w http.ResponseWriter, req *http.Request, id string, code int) {
		var in CertInput
		if !httpjson.Decode(w, req, &in) {
			return
		}
		nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		for _, t := range in.Targets {
			if !slices.ContainsFunc(nodes, func(n model.NodeView) bool { return n.ID == t.NodeID }) {
				httpjson.Error(w, 400, "节点不存在")
				return
			}
		}
		c, err := m.Store.SaveCert(req.Context(), id, in)
		if errors.Is(err, ErrNotFound) {
			fail(w, err)
			return
		} else if err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		m.Kick()
		httpjson.Write(w, code, m.view(c))
	}
	r.Admin("POST /api/v1/admin/certs", func(w http.ResponseWriter, req *http.Request) { saveCert(w, req, "", 201) })
	r.Admin("PUT /api/v1/admin/certs/{id}", func(w http.ResponseWriter, req *http.Request) { saveCert(w, req, req.PathValue("id"), 200) })
	r.Admin("DELETE /api/v1/admin/certs/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := m.Store.DeleteCert(req.Context(), req.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	// 续期：下一轮重新签发（不等到期前 30 天），也清掉失败后的等待。
	r.Admin("POST /api/v1/admin/certs/{id}/renew", func(w http.ResponseWriter, req *http.Request) {
		c, err := m.Store.Update(req.Context(), req.PathValue("id"), func(c *Cert) { c.Force, c.NextAttempt = true, 0 })
		if err != nil {
			fail(w, err)
			return
		}
		m.Kick()
		httpjson.Write(w, 200, m.view(c))
	})
	// 重新下发：所有目标再写一次并执行重载命令。
	r.Admin("POST /api/v1/admin/certs/{id}/deploy", func(w http.ResponseWriter, req *http.Request) {
		c, err := m.Store.Update(req.Context(), req.PathValue("id"), func(c *Cert) {
			for i := range c.Targets {
				c.Targets[i].Deployed = ""
			}
		})
		if err != nil {
			fail(w, err)
			return
		}
		m.Kick()
		httpjson.Write(w, 200, m.view(c))
	})
	// 下载证书或私钥（私钥只在这里出现，写审计日志）。
	r.Admin("GET /api/v1/admin/certs/{id}/download", func(w http.ResponseWriter, req *http.Request) {
		c, err := m.Store.Cert(req.Context(), req.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		part := req.URL.Query().Get("part")
		body, name := c.CertPEM, "fullchain.pem"
		if part == "key" {
			body, name = c.KeyPEM, "privkey.pem"
		}
		if body == "" {
			httpjson.Error(w, 409, "还没有签发成功的证书")
			return
		}
		r.Audit(req, model.AuditEntry{Action: "certs.download", Status: 200, Detail: c.Domains[0] + " " + name})
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s"`, strings.ReplaceAll(c.Domains[0], "*", "_"), name))
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(body))
	})
	saveDNS := func(w http.ResponseWriter, req *http.Request, id string, code int) {
		var in DNSAccount
		if !httpjson.Decode(w, req, &in) {
			return
		}
		in.ID = id
		if in.Token != "" {
			ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
			err := m.dnsVerify(ctx, in.Token)
			cancel()
			if err != nil {
				httpjson.Error(w, 400, "令牌无效："+err.Error())
				return
			}
		}
		a, err := m.Store.SaveDNS(req.Context(), in)
		if errors.Is(err, ErrDNSNotFound) {
			fail(w, err)
			return
		} else if err != nil {
			httpjson.Error(w, 400, err.Error())
			return
		}
		httpjson.Write(w, code, dnsView(a))
	}
	r.Admin("POST /api/v1/admin/certs/dns", func(w http.ResponseWriter, req *http.Request) { saveDNS(w, req, "", 201) })
	r.Admin("PUT /api/v1/admin/certs/dns/{id}", func(w http.ResponseWriter, req *http.Request) { saveDNS(w, req, req.PathValue("id"), 200) })
	r.Admin("DELETE /api/v1/admin/certs/dns/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := m.Store.DeleteDNS(req.Context(), req.PathValue("id")); errors.Is(err, ErrDNSNotFound) {
			fail(w, err)
		} else if err != nil {
			httpjson.Error(w, 409, err.Error())
		} else {
			w.WriteHeader(204)
		}
	})
	r.Admin("PUT /api/v1/admin/certs/settings", func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			Staging bool `json:"staging"`
		}
		if !httpjson.Decode(w, req, &in) {
			return
		}
		v := "0"
		if in.Staging {
			v = "1"
		}
		if err := m.Store.SetState(req.Context(), "staging", v); err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]bool{"staging": in.Staging})
	})
}

// dnsVerify checks a token with Cloudflare (tests' DNS providers skip it).
func (m *Module) dnsVerify(ctx context.Context, token string) error {
	if v, ok := m.dns(token).(interface{ Verify(context.Context) error }); ok {
		return v.Verify(ctx)
	}
	return nil
}

// Overview is one card: certificates and what needs attention (failed,
// expiring within 14 days, not deployed).
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	if m.Store == nil {
		return nil, nil
	}
	list, err := m.Store.Certs(ctx)
	if err != nil {
		return nil, err
	}
	card := module.Card{ID: "certs", Title: "证书", Page: "certs"}
	if len(list) == 0 {
		card.Value, card.Note = "0", "还没有证书"
		return []module.Card{card}, nil
	}
	now := time.Now()
	valid, attention := 0, 0
	for _, c := range list {
		note, tone := "", ""
		left := time.Unix(c.NotAfter, 0).Sub(now)
		switch {
		case c.CertPEM != "" && left <= 0:
			note, tone = "已过期", "bad"
		case c.Status == StatusError:
			note, tone = "签发失败："+c.Error, "bad"
		case c.CertPEM == "":
			note, tone = "等待签发", ""
		case left < 14*24*time.Hour:
			note, tone = fmt.Sprintf("%d 天后到期", int(left.Hours()/24)), "warn"
		}
		if note == "" {
			for _, t := range c.Targets {
				if t.Error != "" {
					note, tone = "下发失败："+t.Error, "bad"
					break
				}
			}
		}
		if c.CertPEM != "" && left > 0 {
			valid++
		}
		if note != "" {
			attention++
			if len(card.Items) < module.MaxCardItems {
				card.Items = append(card.Items, module.CardItem{Label: c.Domains[0], Note: note, Tone: tone})
			}
		}
	}
	card.Value = fmt.Sprintf("%d / %d 有效", valid, len(list))
	card.Tone, card.Note = "good", "都在有效期内，到期前自动续期"
	if attention > 0 {
		card.Tone, card.Note = "warn", fmt.Sprintf("%d 张需要关注", attention)
	}
	return []module.Card{card}, nil
}
