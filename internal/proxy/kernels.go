// Package proxy is the 代理 module: inbounds on managed nodes, served by xray
// or sing-box. The Server downloads pinned kernel releases from their GitHub
// releases, checks them against the SHA-256 recorded here, and hands the
// binary to each node's Agent over the management channel, so nodes never
// fetch anything themselves.
package proxy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	KernelXray    = "xray"
	KernelSingBox = "sing-box"
)

// Asset is one release archive: where it is, its SHA-256 and the binary's
// path inside it.
type Asset struct {
	URL    string
	SHA256 string
	Member string
}

type Kernel struct {
	Name    string
	Version string
	Assets  map[string]Asset // GOARCH → archive
}

// Kernels are the pinned releases. Bump a version together with both
// archives' SHA-256 (GitHub shows them as the asset digest).
var Kernels = map[string]Kernel{
	KernelXray: {Name: KernelXray, Version: "26.10.31", Assets: map[string]Asset{
		"amd64": {URL: "https://github.com/XTLS/Xray-core/releases/download/v26.10.31/Xray-linux-64.zip", SHA256: "e4bd9119f50ae7c41658b1abd0679ae06f98bd864a2907ff33f2f2cf7081b214", Member: "xray"},
		"arm64": {URL: "https://github.com/XTLS/Xray-core/releases/download/v26.10.31/Xray-linux-arm64-v8a.zip", SHA256: "1811c8f1da0c3b6dc707ebaf1c8390e987ba39859a8f0a7aadd8526157f2ec8d", Member: "xray"},
	}},
	KernelSingBox: {Name: KernelSingBox, Version: "1.14.4", Assets: map[string]Asset{
		"amd64": {URL: "https://github.com/SagerNet/sing-box/releases/download/v1.14.4/sing-box-1.14.4-linux-amd64.tar.gz", SHA256: "699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968", Member: "sing-box-1.14.4-linux-amd64/sing-box"},
		"arm64": {URL: "https://github.com/SagerNet/sing-box/releases/download/v1.14.4/sing-box-1.14.4-linux-arm64.tar.gz", SHA256: "436441f76ee2f7fe21b5b2d426d3cb261a480455a704865789ad533e4273ec26", Member: "sing-box-1.14.4-linux-arm64/sing-box"},
	}},
}

// MaxArchive bounds a download; the real archives are 15–32 MB.
const MaxArchive = 64 << 20

// Binary is an extracted kernel binary ready to send.
type Binary struct {
	Kernel, Version, Arch string
	Path                  string
	SHA256                string
	Size                  int64
}

// Cache keeps extracted kernel binaries in Dir (the Server's data directory
// kernels/), downloading each archive once.
type Cache struct {
	Dir    string
	Client *http.Client // nil: a client with a 5-minute timeout
	mu     sync.Mutex
}

// Get returns the binary for kernel on arch, downloading and checking the
// pinned archive the first time.
func (c *Cache) Get(ctx context.Context, kernel, arch string) (Binary, error) {
	k, ok := Kernels[kernel]
	if !ok {
		return Binary{}, fmt.Errorf("未知内核 %q", kernel)
	}
	asset, ok := k.Assets[arch]
	if !ok {
		return Binary{}, fmt.Errorf("%s 没有 %s 架构的版本", kernel, arch)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	path := filepath.Join(c.Dir, fmt.Sprintf("%s-%s-%s", kernel, k.Version, arch))
	if b, err := describe(path); err == nil {
		b.Kernel, b.Version, b.Arch = kernel, k.Version, arch
		return b, nil
	}
	archive, err := c.download(ctx, asset)
	if err != nil {
		return Binary{}, err
	}
	binary, err := extract(archive, asset)
	if err != nil {
		return Binary{}, err
	}
	if err := os.MkdirAll(c.Dir, 0700); err != nil {
		return Binary{}, err
	}
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, binary, 0600); err != nil {
		return Binary{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Binary{}, err
	}
	b, err := describe(path)
	b.Kernel, b.Version, b.Arch = kernel, k.Version, arch
	return b, err
}

func describe(path string) (Binary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Binary{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Binary{}, err
	}
	if n == 0 {
		return Binary{}, errors.New("empty")
	}
	return Binary{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

func (c *Cache) download(ctx context.Context, asset Asset) ([]byte, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载内核失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载内核失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxArchive+1))
	if err != nil {
		return nil, fmt.Errorf("下载内核失败：%w", err)
	}
	if len(data) > MaxArchive {
		return nil, errors.New("内核压缩包过大")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != asset.SHA256 {
		return nil, errors.New("内核压缩包校验失败（SHA-256 不符），已丢弃")
	}
	return data, nil
}

// MaxBinary bounds the extracted binary.
const MaxBinary = 128 << 20

func extract(archive []byte, asset Asset) ([]byte, error) {
	read := func(r io.Reader) ([]byte, error) {
		data, err := io.ReadAll(io.LimitReader(r, MaxBinary+1))
		if err == nil && len(data) > MaxBinary {
			err = errors.New("内核文件过大")
		}
		return data, err
	}
	if bytes.HasPrefix(archive, []byte("PK")) {
		z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range z.File {
			if f.Name == asset.Member {
				r, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer r.Close()
				return read(r)
			}
		}
		return nil, fmt.Errorf("压缩包里没有 %s", asset.Member)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("压缩包里没有 %s", asset.Member)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == asset.Member && h.Typeflag == tar.TypeReg {
			return read(tr)
		}
	}
}
