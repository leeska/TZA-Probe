package httpapi

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// publicPolicy is what the visitor-facing endpoints may return: whether the
// whole front page needs a login, and which sections are turned off.
type publicPolicy struct {
	login  bool
	hidden []string
}

func (p publicPolicy) hides(section string) bool { return slices.Contains(p.hidden, section) }

// publicPolicyTTL bounds how long a settings change takes to reach visitors;
// the public panel polls every second, so the two settings are not read on
// every request.
const publicPolicyTTL = 2 * time.Second

type policyCache struct {
	mu     sync.Mutex
	value  publicPolicy
	expiry time.Time
}

// policy reads the two settings, cached briefly. A read error fails closed for
// the login switch only when it was already on.
func (a *API) policy(r *http.Request) publicPolicy {
	c := &a.publicPolicy
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expiry) {
		return c.value
	}
	hidden, err1 := a.Store.Setting(r.Context(), model.SettingPublicHidden)
	login, err2 := a.Store.Setting(r.Context(), model.SettingPublicLogin)
	if err1 != nil || err2 != nil {
		return c.value
	}
	c.value = publicPolicy{login: login == "1", hidden: model.PublicHidden(hidden)}
	c.expiry = time.Now().Add(publicPolicyTTL)
	return c.value
}

// forgetPolicy makes the next public request read the settings again (after
// the administrator saved them).
func (a *API) forgetPolicy() {
	a.publicPolicy.mu.Lock()
	a.publicPolicy.expiry = time.Time{}
	a.publicPolicy.mu.Unlock()
}

// public wraps a visitor-facing handler: when the front page needs a login,
// an anonymous request gets 401 with login:true (the panel shows a sign-in
// prompt). section, when set, is the section the endpoint belongs to: turned
// off, an anonymous request gets 404 as if the endpoint did not exist.
func (a *API) public(section string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAdmin(r) {
			p := a.policy(r)
			if p.login {
				write(w, 401, map[string]any{"error": "此面板需要登录后查看", "login": true})
				return
			}
			if section != "" && p.hides(section) {
				write(w, 404, map[string]string{"error": "接口不存在"})
				return
			}
		}
		h(w, r)
	}
}

// strip removes from an anonymous node view what the hidden sections cover.
func (p publicPolicy) strip(n model.NodeView) model.NodeView {
	if p.hides("network") {
		n.ProbeTargets = nil
		n.Display = model.NodeDisplay{}
	}
	if p.hides("unlock") {
		n.Unlock = nil
	}
	if p.hides("billing") {
		n.Billing = model.Billing{ResetAt: n.Billing.ResetAt, TrafficLimit: n.Billing.TrafficLimit, TrafficMode: n.Billing.TrafficMode}
		n.RemainingValue = nil
	}
	return n
}

// keepObservation reports whether an anonymous visitor may see o.
func (p publicPolicy) keepObservation(o model.Observation) bool {
	if o.Task.Kind == model.UnlockKind {
		return !p.hides("unlock")
	}
	return !p.hides("network")
}
