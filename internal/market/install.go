package market

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// Install downloads a release of package id from CC, verifies it and installs
// it into the theme store or the plugin store (by what the archive holds),
// replacing whatever is installed under that id. version "" means the
// newest release this Probe can run. Nothing is written unless every check
// passes: the reported size, the SHA-256 and Ed25519 signature (by a trusted
// key) of the archive, and theme.json naming the same id and version.
func (c *Client) Install(ctx context.Context, id, version string) (Result, error) {
	if c == nil {
		return Result{}, ErrNotConfigured
	}
	if !tzapkg.ValidID(id) {
		return Result{}, model.Invalid("包 id 须为 2–32 位小写字母、数字或连字符，以字母开头")
	}
	if version != "" && !tzapkg.ValidVersion(version) {
		return Result{}, model.Invalid("version 须为 MAJOR.MINOR.PATCH，如 1.2.0；省略即装最新版")
	}
	if len(c.keys) == 0 {
		return Result{}, ErrNoTrustedKeys
	}
	// The release list says which versions this Probe can run; CC also filters,
	// but "install the latest" must mean the latest compatible one.
	detail, err := c.Package(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if version, err = c.pick(detail, version); err != nil {
		return Result{}, err
	}

	var grant struct {
		URL       string           `json:"url"`
		Size      int64            `json:"size"`
		Signature tzapkg.Signature `json:"signature"`
		Receipt   string           `json:"receipt"`
	}
	body := map[string]string{"probe": c.probe}
	if version != "" {
		body["version"] = version
	}
	if err := c.call(ctx, http.MethodPost, []string{"packages", id, "download"}, nil, body, &grant, true); err != nil {
		return Result{}, err
	}
	sig := grant.Signature
	switch {
	case sig.Package != id:
		return Result{}, rejected("签名里的包 %q 不是 %q", sig.Package, id)
	case !tzapkg.ValidVersion(sig.Version):
		return Result{}, rejected("签名里的版本 %q 不是 MAJOR.MINOR.PATCH", sig.Version)
	case version != "" && sig.Version != version:
		return Result{}, rejected("签名里的版本 %s 不是请求的 %s", sig.Version, version)
	case grant.Size <= 0 || grant.Size > tzapkg.MaxUpload:
		return Result{}, rejected("包大小 %d 字节超出范围（最大 %d MB）", grant.Size, tzapkg.MaxUpload>>20)
	}
	if _, ok := c.keys[sig.KeyID]; !ok {
		return Result{}, rejected("签名密钥 %q 不在信任列表里", sig.KeyID)
	}
	archive, err := c.download(ctx, grant.URL, grant.Size)
	if err != nil {
		return Result{}, err
	}
	if err := sig.Verify(c.keys, archive); err != nil {
		return Result{}, fmt.Errorf("%w：%v", ErrRejected, err)
	}
	info, err := tzapkg.Inspect(archive)
	switch {
	case err != nil:
		return Result{}, fmt.Errorf("%w：%v", ErrRejected, err)
	case info.ID != id:
		return Result{}, rejected("包内清单的 id %q 与市场包 %q 不符", info.ID, id)
	case info.Version != sig.Version:
		return Result{}, rejected("包内清单的 version %q 与签名版本 %s 不符", info.Version, sig.Version)
	}
	at := c.now().UTC()
	if info.Type == tzapkg.TypePlugin {
		if c.plugins == nil || c.plugins.Dir == "" {
			return Result{}, errors.New("这个 Server 没有配置插件目录")
		}
		p, err := c.plugins.InstallMarket(archive, plugin.Origin{Version: sig.Version, SHA256: sig.SHA256, KeyID: sig.KeyID, InstalledAt: at})
		if err != nil {
			return Result{}, err
		}
		c.confirmInstall(ctx, grant.Receipt)
		return Result{ID: p.ID, Type: p.Type, Kind: p.Kind, Name: p.Name, Version: p.Version, Source: p.Source, Permissions: p.Permissions()}, nil
	}
	if c.themes == nil || c.themes.Dir == "" {
		return Result{}, errors.New("这个 Server 没有配置主题目录")
	}
	m, err := c.themes.InstallMarket(archive, theme.Origin{Version: sig.Version, SHA256: sig.SHA256, KeyID: sig.KeyID, InstalledAt: at})
	if err != nil {
		return Result{}, err
	}
	c.confirmInstall(ctx, grant.Receipt)
	return Result{ID: m.ID, Type: m.Type, Name: m.Name, Version: m.Version, Source: m.Source}, nil
}

// A receipt is only acknowledged after validation and installation succeed.
// Old CCs omit it. Reporting failure must not turn a completed install into
// an error that invites the administrator to install the same package again.
func (c *Client) confirmInstall(ctx context.Context, receipt string) {
	if receipt == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := c.call(ctx, http.MethodPost, []string{"installations"}, nil, map[string]string{"receipt": receipt, "probe": c.probe}, nil, true); err != nil {
		slog.Warn("market package installed; confirmation unavailable")
	}
	if c.Status().Bound {
		if _, err := c.Updates(ctx); err != nil {
			slog.Warn("market inventory synchronization unavailable")
		}
	}
}

// Result is what an install put in place: a theme / add-on, or a
// plugin (with Kind and what it may do).
type Result struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Kind        string   `json:"kind,omitempty"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Source      string   `json:"source"`
	Permissions []string `json:"permissions,omitempty"`
}

// pick resolves the version to install against the release list.
func (c *Client) pick(detail Package, version string) (string, error) {
	if len(detail.Versions) == 0 {
		// CC listed no releases: let its download endpoint decide.
		return version, nil
	}
	for _, r := range detail.Versions {
		if version == "" && r.Compatible {
			return r.Version, nil
		}
		if r.Version == version {
			if !r.Compatible {
				return "", model.Invalid(fmt.Sprintf("%s v%s 需要 Probe %s，当前是 %s", detail.ID, r.Version, r.Probe, c.probe))
			}
			return version, nil
		}
	}
	if version == "" {
		return "", model.Invalid(fmt.Sprintf("%s 没有兼容当前 Probe（%s）的版本", detail.ID, c.probe))
	}
	return "", model.NotFound(fmt.Sprintf("%s 没有 v%s 这个版本", detail.ID, version))
}

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w：%s", ErrRejected, fmt.Sprintf(format, args...))
}

// download fetches the archive from the signed URL CC handed out. The URL
// may be on object storage, so no token goes with it; the signature is what
// makes the bytes trustworthy. Exactly size bytes are accepted.
func (c *Client) download(ctx context.Context, raw string, size int64) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || !c.fetchable(u) {
		return nil, rejected("下载地址无效")
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, rejected("下载地址无效")
	}
	req.Header.Set("User-Agent", "tza-probe/"+c.probe)
	resp, err := c.files.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &RemoteError{Status: resp.StatusCode, Message: "下载安装包失败（HTTP " + strconv.Itoa(resp.StatusCode) + "），下载地址 5 分钟内有效，请重试"}
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return nil, rejected("下载大小 %d 字节与市场登记的 %d 字节不符", resp.ContentLength, size)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, size+1))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if int64(len(data)) != size {
		return nil, rejected("下载大小与市场登记的 %d 字节不符", size)
	}
	return data, nil
}

// Media is a preview image fetched for the panel.
type Media struct {
	Type string
	Body []byte
}

// mediaTypes are the image types the proxy passes on (no SVG: it can carry script).
var mediaTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true, "image/avif": true}

// Media fetches a cover or screenshot for the panel, so the browser never
// contacts CC. Only URLs on CC's own origin are fetched, only image types
// come back, at most 5 MB.
func (c *Client) Media(ctx context.Context, raw string) (Media, error) {
	if c == nil {
		return Media{}, ErrNotConfigured
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || !c.onOrigin(u) {
		return Media{}, model.Invalid("只代理 TZA-CC 上的图片")
	}
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Media{}, model.Invalid("图片地址无效")
	}
	req.Header.Set("User-Agent", "tza-probe/"+c.probe)
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif")
	resp, err := c.files.Do(req)
	if err != nil {
		return Media{}, transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Media{}, &RemoteError{Status: resp.StatusCode, Message: "图片不可用（HTTP " + strconv.Itoa(resp.StatusCode) + "）"}
	}
	ctype, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !mediaTypes[ctype] {
		return Media{}, rejected("不是允许的图片类型")
	}
	if resp.ContentLength > maxMedia {
		return Media{}, rejected("图片超过 %d MB", maxMedia>>20)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMedia+1))
	if err != nil {
		return Media{}, transportError(ctx, err)
	}
	if len(data) > maxMedia {
		return Media{}, rejected("图片超过 %d MB", maxMedia>>20)
	}
	return Media{Type: ctype, Body: data}, nil
}
