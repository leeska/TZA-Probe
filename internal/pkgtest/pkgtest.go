// Package pkgtest builds package archives for tests: realistic plugin
// manifests, theme skins and zips assembled from file maps.
package pkgtest

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"strings"
	"testing"
)

// Official-style plugin manifests (ntfy, gotify, feishu, dingtalk,
// public-dns), so tests run against realistic plugins.
//
//go:embed testdata/plugins/*.json
var pluginFiles embed.FS

// PluginJSON is the plugin.json of a plugin fixture.
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

// PluginZip is a plugin archive of a fixture, optionally with its version
// replaced.
func PluginZip(t testing.TB, id, version string) []byte {
	t.Helper()
	raw := string(PluginJSON(t, id))
	if version != "" {
		raw = strings.Replace(raw, `"version": "1.0.0"`, `"version": "`+version+`"`, 1)
	}
	return Zip(t, map[string]string{id + "/plugin.json": raw})
}

// Skin builds a valid skin archive with the given theme.json id and version.
func Skin(t testing.TB, id, version string) []byte {
	t.Helper()
	manifest := fmt.Sprintf(`{"id":%q,"name":"测试主题","version":%q,"chrome":{"light":"#eef0e2","dark":"#141811"},"swatch":["#eef0e2","#fbfaf3","#5f8f3a"],"styles":["theme.css"]}`, id, version)
	return Zip(t, map[string]string{"theme.json": manifest, "theme.css": `:root[data-skin="` + id + `"]{--accent:#5f8f3a}`})
}
