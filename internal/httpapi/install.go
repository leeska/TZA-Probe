package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/leeska/TZA-Probe/internal/model"
)

type installIdentityStore interface {
	InstallIdentity(ctx context.Context, token string) (model.RuntimeTarget, error)
}

// InstallIdentity answers the install command's provisioning step: with a
// valid node token it returns the controller-assigned handshake identity,
// the CC base this Server trusts for runtime artifacts, and its version, so
// the node can write a complete runtime installer configuration itself.
func (a *API) installIdentity(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &v) {
		return
	}
	installStore, supported := a.Store.(installIdentityStore)
	if !supported {
		write(w, 404, map[string]string{"error": "主控版本不支持插件组件自动配置"})
		return
	}
	target, err := installStore.InstallIdentity(r.Context(), v.Token)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]string{
		"controller_id": target.ControllerID,
		"node_id":       target.NodeID,
		"cc_url":        a.Market.Base(),
		"probe_version": strings.TrimPrefix(model.Version, "v"),
	})
}
