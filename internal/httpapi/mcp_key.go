package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Store only a digest; the secret is returned once when generated.
const mcpKeySetting = "mcp_api_key"

type mcpKeyRecord struct {
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *API) loadMCPKey(r *http.Request) (mcpKeyRecord, error) {
	value, err := a.Store.Setting(r.Context(), mcpKeySetting)
	var record mcpKeyRecord
	if err == nil && value != "" {
		err = json.Unmarshal([]byte(value), &record)
	}
	return record, err
}

func (a *API) mcpKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/mcp-key", a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		record, err := a.loadMCPKey(r)
		if err != nil {
			fail(w, err)
			return
		}
		out := map[string]any{"configured": record.Hash != ""}
		if record.Hash != "" {
			out["created_at"] = record.CreatedAt
		}
		write(w, 200, out)
	}))
	mux.HandleFunc("POST /api/v1/admin/mcp-key", a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key := "tza_mcp_" + model.Secret()
		now := time.Now().UTC()
		value, err := json.Marshal(mcpKeyRecord{Hash: model.Hash(key), CreatedAt: now})
		if err == nil {
			err = a.Store.SetSetting(r.Context(), mcpKeySetting, string(value))
		}
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]any{"configured": true, "created_at": now, "api_key": key})
	}))
	mux.HandleFunc("DELETE /api/v1/admin/mcp-key", a.admin(func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.Store.SetSetting(r.Context(), mcpKeySetting, ""))
	}))
}

func (a *API) mcpAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || key == "" {
			write(w, 401, map[string]string{"error": "MCP API Key 无效或已撤销"})
			return
		}
		record, err := a.loadMCPKey(r)
		if err != nil {
			fail(w, err)
			return
		}
		if record.Hash == "" || subtle.ConstantTimeCompare([]byte(model.Hash(key)), []byte(record.Hash)) != 1 {
			write(w, 401, map[string]string{"error": "MCP API Key 无效或已撤销"})
			return
		}
		if !sameOrigin(r) {
			write(w, 403, map[string]string{"error": "请求来源不受信任"})
			return
		}
		next(w, r)
	}
}
