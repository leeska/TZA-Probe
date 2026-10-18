// Package markettest is a fake TZA-CC for tests: the P0 instance API of
// TZA-CC docs/DESIGN.md §9, packages signed with a throwaway Ed25519 key, and
// knobs that make it misbehave (tampered archives, wrong key ids, lying sizes).
package markettest

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// Token is the instance token the fake hands out once a device code is approved.
const Token = "tza_ins_test_token_0123456789"

// Release is one published version.
type Release struct {
	Version string
	Probe   string
	Archive []byte
	// SignAs overrides the package id that gets signed (default: the package id).
	SignAs string
	// CCIncompatible makes the detail page mark this release compatible=false.
	CCIncompatible bool
}

// Package is one market entry. Kind is set for plugins (notify | targets).
type Package struct {
	ID, Type, Name, Kind string
	Releases             []Release // newest first
}

// Copies of TZA-CC's official plugin manifests (plugins/official/*), so the
// tests run against realistic plugins without reaching into the CC repository.
//
//go:embed testdata/plugins/*.json
var pluginFiles embed.FS

// PluginJSON is the plugin.json of an official plugin fixture (ntfy, gotify,
// feishu, dingtalk, public-dns).
func PluginJSON(t testing.TB, id string) []byte {
	t.Helper()
	raw, err := pluginFiles.ReadFile("testdata/plugins/" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Zip packs files into an archive.
func Zip(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// PluginZip is a plugin archive of an official fixture, optionally with its
// version replaced.
func PluginZip(t testing.TB, id, version string) []byte {
	t.Helper()
	raw := string(PluginJSON(t, id))
	if version != "" {
		raw = strings.Replace(raw, `"version": "1.0.0"`, `"version": "`+version+`"`, 1)
	}
	return Zip(t, map[string]string{id + "/plugin.json": raw})
}

// Seen is one request the fake received.
type Seen struct {
	Method, Path, Query, Auth, Body string
}

// CC is the fake. Lock it before changing the knobs while the server runs.
type CC struct {
	sync.Mutex
	Server *httptest.Server
	KeyID  string
	Key    ed25519.PrivateKey
	Pub    ed25519.PublicKey

	Packages map[string]*Package
	Seen     []Seen

	// Knobs.
	Tamper             bool   // flip a byte of the archive after signing
	SigKeyID           string // claim this key id in signatures
	SignKey            ed25519.PrivateKey
	ExtraBytes         int    // serve this many bytes more than the reported size
	Size               int64  // report this size instead of the real one
	Receipt            string // optional successful-install receipt (empty emulates older CC)
	ConfirmationStatus int    // fail the receipt endpoint when nonzero
	Device             string // device-code state: "pending" (default), "approved", "expired", "slow", "limit"
	Revoked            bool   // answer 401 to any request carrying a token
	Account            string // account name returned on approval
	Updates            []map[string]string
}

// New starts a fake CC with one key.
func New(t testing.TB) *CC {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cc := &CC{KeyID: "cc-test", Key: key, Pub: pub, Packages: map[string]*Package{}, Device: "pending", Account: "alice"}
	cc.Server = httptest.NewServer(http.HandlerFunc(cc.serve))
	t.Cleanup(cc.Server.Close)
	return cc
}

// URL is the fake's base address.
func (cc *CC) URL() string { return cc.Server.URL }

// Keys is the trust list a client needs for this fake.
func (cc *CC) Keys() map[string]ed25519.PublicKey {
	return map[string]ed25519.PublicKey{cc.KeyID: cc.Pub}
}

// Add publishes a package (releases newest first).
func (cc *CC) Add(p *Package) {
	cc.Lock()
	defer cc.Unlock()
	cc.Packages[p.ID] = p
}

// Requests returns a copy of what the fake has seen.
func (cc *CC) Requests() []Seen {
	cc.Lock()
	defer cc.Unlock()
	return append([]Seen{}, cc.Seen...)
}

// Skin builds a valid skin archive with the given theme.json id and version.
func Skin(t testing.TB, id, version string) []byte {
	t.Helper()
	manifest := fmt.Sprintf(`{"id":%q,"name":"市场主题","version":%q,"chrome":{"light":"#eef0e2","dark":"#141811"},"swatch":["#eef0e2","#fbfaf3","#5f8f3a"],"styles":["theme.css"]}`, id, version)
	return Zip(t, map[string]string{"theme.json": manifest, "theme.css": `:root[data-skin="` + id + `"]{--accent:#5f8f3a}`})
}

// PNG is a tiny body served as a preview image.
var PNG = []byte("\x89PNG\r\n\x1a\nfake")

func (cc *CC) item(p *Package) map[string]any {
	latest := ""
	if len(p.Releases) > 0 {
		latest = p.Releases[0].Version
	}
	out := map[string]any{
		"id": p.ID, "type": p.Type, "name": p.Name, "summary": "一句话", "author": map[string]string{"name": "Dev", "handle": "dev"},
		"latest": latest, "price": 0, "downloads": 12, "updated_at": "2026-10-01T00:00:00Z",
		"swatch": []string{"#eef0e2", "#fbfaf3", "#5f8f3a"}, "cover": cc.Server.URL + "/media/" + p.ID + ".png",
	}
	if p.Type == tzapkg.TypePlugin {
		// Plugins: no swatch or cover; kind and what the newest release may do.
		delete(out, "cover")
		out["swatch"] = []string{}
		out["kind"] = p.Kind
		if len(p.Releases) > 0 {
			if info, err := tzapkg.Inspect(p.Releases[0].Archive); err == nil {
				out["permissions"] = info.Permissions
			}
		}
	}
	return out
}

func (cc *CC) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	cc.Lock()
	defer cc.Unlock()
	cc.Seen = append(cc.Seen, Seen{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: string(body)})
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, msg string) { reply(status, map[string]string{"error": msg}) }
	if cc.Revoked && r.Header.Get("Authorization") != "" {
		fail(401, "实例凭据无效或已解绑")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/catalog":
		typ, q := r.URL.Query().Get("type"), r.URL.Query().Get("q")
		items := []any{}
		for _, p := range cc.Packages {
			if (typ == "" || p.Type == typ) && strings.Contains(p.ID+p.Name, q) {
				items = append(items, cc.item(p))
			}
		}
		// An entry the Probe cannot install (P1 plugin) and one with a bad id.
		if typ == "" {
			items = append(items, map[string]any{"id": "feishu", "type": "plugin", "name": "飞书"}, map[string]any{"id": "../x", "type": "skin", "name": "bad"})
		}
		reply(200, map[string]any{"items": items, "page": 1, "more": false})

	case r.Method == "GET" && len(parts) == 4 && parts[2] == "packages":
		p, ok := cc.Packages[parts[3]]
		if !ok {
			fail(404, "没有这个包")
			return
		}
		out := cc.item(p)
		versions := []any{}
		for _, rel := range p.Releases {
			permissions := []string{}
			if info, err := tzapkg.Inspect(rel.Archive); err == nil {
				permissions = info.Permissions
			}
			versions = append(versions, map[string]any{"version": rel.Version, "changelog": "改动 " + rel.Version, "probe": rel.Probe, "size": len(rel.Archive), "published_at": "2026-10-01T00:00:00Z", "compatible": !rel.CCIncompatible, "permissions": permissions})
		}
		out["description"] = "详细说明"
		out["screenshots"] = []string{cc.Server.URL + "/media/shot.png", "https://elsewhere.example/shot.png"}
		out["versions"] = versions
		reply(200, out)

	case r.Method == "POST" && len(parts) == 5 && parts[2] == "packages" && parts[4] == "download":
		p, ok := cc.Packages[parts[3]]
		if !ok {
			fail(404, "没有这个包")
			return
		}
		var req struct {
			Version string `json:"version"`
		}
		json.Unmarshal(body, &req)
		var rel *Release
		for i := range p.Releases {
			if req.Version == "" || p.Releases[i].Version == req.Version {
				rel = &p.Releases[i]
				break
			}
		}
		if rel == nil {
			fail(404, "没有这个版本")
			return
		}
		key, keyID, signAs := cc.Key, cc.KeyID, p.ID
		if cc.SignKey != nil {
			key = cc.SignKey
		}
		if cc.SigKeyID != "" {
			keyID = cc.SigKeyID
		}
		if rel.SignAs != "" {
			signAs = rel.SignAs
		}
		sig := tzapkg.Sign(key, keyID, signAs, rel.Version, rel.Archive)
		size := int64(len(rel.Archive))
		if cc.Size != 0 {
			size = cc.Size
		}
		reply(200, map[string]any{"url": cc.Server.URL + "/dl/" + p.ID + "/" + rel.Version, "size": size, "signature": sig, "receipt": cc.Receipt})
	case r.Method == "POST" && r.URL.Path == "/api/v1/installations":
		if cc.ConfirmationStatus != 0 {
			fail(cc.ConfirmationStatus, "confirmation unavailable")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == "GET" && len(parts) == 3 && parts[0] == "dl":
		p, ok := cc.Packages[parts[1]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		for _, rel := range p.Releases {
			if rel.Version == parts[2] {
				data := append([]byte{}, rel.Archive...)
				if cc.Tamper {
					data[len(data)/2] ^= 0xff
				}
				data = append(data, bytes.Repeat([]byte{0}, cc.ExtraBytes)...)
				w.Header().Set("Content-Type", "application/zip")
				w.Write(data)
				return
			}
		}
		http.NotFound(w, r)

	case r.Method == "POST" && r.URL.Path == "/api/v1/updates":
		updates := cc.Updates
		if updates == nil {
			updates = []map[string]string{}
		}
		reply(200, map[string]any{"updates": updates})

	case r.Method == "POST" && r.URL.Path == "/api/v1/device/code":
		reply(200, map[string]any{"device_code": "dev-code-1", "user_code": "ABCD-EFGH", "verify_url": cc.Server.URL + "/device", "expires_in": 600, "interval": 5})

	case r.Method == "POST" && r.URL.Path == "/api/v1/device/token":
		switch cc.Device {
		case "approved":
			reply(200, map[string]any{"instance_id": "ins_1", "instance_token": Token, "account": map[string]string{"name": cc.Account}})
		case "expired":
			fail(410, "expired_token")
		case "slow":
			fail(429, "slow_down")
		case "limit":
			fail(409, "账号最多绑定 5 台实例")
		default:
			fail(428, "authorization_pending")
		}

	case r.Method == "DELETE" && r.URL.Path == "/api/v1/instance":
		if r.Header.Get("Authorization") != "Bearer "+Token {
			fail(401, "unauthorized")
			return
		}
		w.WriteHeader(204)

	case r.Method == "GET" && len(parts) == 2 && parts[0] == "media":
		switch parts[1] {
		case "evil.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte("<svg onload=alert(1)>"))
		case "big.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(bytes.Repeat([]byte{1}, 6<<20))
		case "away.png":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "image/png")
			w.Write(PNG)
		}

	default:
		fail(404, "接口不存在")
	}
}
