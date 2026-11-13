package proxy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 主控的内核缓存：按固定的 SHA-256 校验下载的压缩包（zip 与 tar.gz），取出内核文件，只下载一次。
func TestCache(t *testing.T) {
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	w, _ := zw.Create("xray")
	w.Write([]byte("xray-binary"))
	zw.Create("geoip.dat")
	zw.Close()
	var g bytes.Buffer
	gz := gzip.NewWriter(&g)
	tw := tar.NewWriter(gz)
	body := []byte("sing-box-binary")
	tw.WriteHeader(&tar.Header{Name: "sb/sing-box", Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	saved := Kernels
	defer func() { Kernels = saved }()
	Kernels = map[string]Kernel{
		KernelXray:    {Name: KernelXray, Version: "1", Assets: map[string]Asset{"amd64": {URL: "https://x/xray.zip", SHA256: sum(z.Bytes()), Member: "xray"}, "arm64": {URL: "https://x/bad.zip", SHA256: strings.Repeat("0", 64), Member: "xray"}}},
		KernelSingBox: {Name: KernelSingBox, Version: "2", Assets: map[string]Asset{"amd64": {URL: "https://x/sb.tgz", SHA256: sum(g.Bytes()), Member: "sb/sing-box"}}},
	}
	downloads := 0
	c := &Cache{Dir: t.TempDir(), Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		downloads++
		data := map[string][]byte{"/xray.zip": z.Bytes(), "/bad.zip": z.Bytes(), "/sb.tgz": g.Bytes()}[r.URL.Path]
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}}
	ctx := context.Background()
	for range 2 {
		b, err := c.Get(ctx, KernelXray, "amd64")
		if err != nil || b.SHA256 != sum([]byte("xray-binary")) || b.Version != "1" || b.Size != 11 {
			t.Fatal(b, err)
		}
	}
	if downloads != 1 {
		t.Fatal("取过一次就用缓存", downloads)
	}
	if b, err := c.Get(ctx, KernelSingBox, "amd64"); err != nil || b.SHA256 != sum(body) {
		t.Fatal(b, err)
	}
	if _, err := c.Get(ctx, KernelXray, "arm64"); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatal("SHA-256 不符要拒绝", err)
	}
	if _, err := c.Get(ctx, KernelSingBox, "riscv64"); err == nil {
		t.Fatal("没有的架构")
	}
}
