package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/channel"
)

// NginxHost serves the sites capability.
type NginxHost struct {
	// ConfDir is where managed files go (/etc/nginx/conf.d).
	ConfDir string
	// Run runs a command and returns its combined output (tests replace it).
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	mu  sync.Mutex
}

func NewNginxHost() *NginxHost {
	return &NginxHost{ConfDir: "/etc/nginx/conf.d", Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = shellEnv()
		cmd.WaitDelay = 2 * time.Second
		return cmd.CombinedOutput()
	}}
}

func (h *NginxHost) Handlers() map[string]channel.Handler {
	return map[string]channel.Handler{
		channel.KindSitesStatus: h.status,
		channel.KindSitesApply:  h.apply,
	}
}

var (
	siteFile      = regexp.MustCompile(`^` + channel.SiteFilePrefix + `[a-z0-9]{1,32}\.conf$`)
	nginxVersion  = regexp.MustCompile(`nginx/([0-9]+\.[0-9]+\.[0-9]+)`)
	configFile    = regexp.MustCompile(`^# configuration file (.+):$`)
	serverNameRe  = regexp.MustCompile(`^\s*server_name\s+([^;]+);`)
	serverBlockRe = regexp.MustCompile(`^\s*server\s*\{`)
)

func (h *NginxHost) managed() (map[string][]byte, error) {
	out := map[string][]byte{}
	entries, err := os.ReadDir(h.ConfDir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && siteFile.MatchString(e.Name()) {
			raw, err := os.ReadFile(filepath.Join(h.ConfDir, e.Name()))
			if err != nil {
				return nil, err
			}
			out[e.Name()] = raw
		}
	}
	return out, nil
}

func (h *NginxHost) status(ctx context.Context, _ json.RawMessage, conn net.Conn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st := channel.SitesStatus{Managed: map[string]string{}, Sites: []channel.NginxSite{}}
	out, err := h.Run(ctx, "nginx", "-v")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return channel.WriteLine(conn, st)
		}
	}
	st.Installed = true
	if m := nginxVersion.FindStringSubmatch(string(out)); m != nil {
		st.Version = m[1]
	}
	if active, _ := h.Run(ctx, "systemctl", "is-active", "nginx"); strings.TrimSpace(string(active)) == "active" {
		st.Active = true
	}
	files, err := h.managed()
	if err != nil {
		st.Error = err.Error()
		return channel.WriteLine(conn, st)
	}
	for name, raw := range files {
		sum := sha256.Sum256(raw)
		st.Managed[name] = hex.EncodeToString(sum[:])
	}
	if dump, err := h.Run(ctx, "nginx", "-T"); err == nil {
		st.Sites = parseNginxDump(string(dump), h.ConfDir)
	}
	return channel.WriteLine(conn, st)
}

// parseNginxDump lists the server blocks in `nginx -T` output with their
// server names, by file.
func parseNginxDump(dump, confDir string) []channel.NginxSite {
	byFile := map[string]*channel.NginxSite{}
	order := []string{}
	file := ""
	for _, line := range strings.Split(dump, "\n") {
		if m := configFile.FindStringSubmatch(line); m != nil {
			file = m[1]
			continue
		}
		if file == "" {
			continue
		}
		if serverBlockRe.MatchString(line) && byFile[file] == nil {
			byFile[file] = &channel.NginxSite{File: file, Names: []string{}, Managed: filepath.Dir(file) == confDir && siteFile.MatchString(filepath.Base(file))}
			order = append(order, file)
		}
		if m := serverNameRe.FindStringSubmatch(line); m != nil && byFile[file] != nil {
			for _, n := range strings.Fields(m[1]) {
				if n != "_" && !containsString(byFile[file].Names, n) {
					byFile[file].Names = append(byFile[file].Names, n)
				}
			}
		}
	}
	out := []channel.NginxSite{}
	for _, f := range order {
		out = append(out, *byFile[f])
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// apply replaces the managed set and keeps it only if nginx accepts it.
func (h *NginxHost) apply(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.SitesApplyParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Size < 2 || p.Size > channel.MaxSitesPayload {
		return channel.WriteLine(conn, channel.SitesResult{Error: "请求无效"})
	}
	body, err := io.ReadAll(io.LimitReader(conn, p.Size))
	sum := sha256.Sum256(body)
	if err != nil || int64(len(body)) != p.Size || hex.EncodeToString(sum[:]) != p.SHA256 {
		return channel.WriteLine(conn, channel.SitesResult{Error: "传输中断或校验和不符"})
	}
	var files map[string]string
	if err := json.Unmarshal(body, &files); err != nil || len(files) > channel.MaxSiteFiles {
		return channel.WriteLine(conn, channel.SitesResult{Error: "站点配置无效"})
	}
	for name := range files {
		if !siteFile.MatchString(name) {
			return channel.WriteLine(conn, channel.SitesResult{Error: "文件名无效：" + name})
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := os.Stat(h.ConfDir); err != nil {
		return channel.WriteLine(conn, channel.SitesResult{Error: "这台机器没有 " + h.ConfDir + "：请先安装 nginx"})
	}
	previous, err := h.managed()
	if err != nil {
		return channel.WriteLine(conn, channel.SitesResult{Error: err.Error()})
	}
	restore := func() {
		for name := range files {
			if _, kept := previous[name]; !kept {
				os.Remove(filepath.Join(h.ConfDir, name))
			}
		}
		for name, raw := range previous {
			writeAtomic(filepath.Join(h.ConfDir, name), raw, 0644)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeAtomic(filepath.Join(h.ConfDir, name), []byte(files[name]), 0644); err != nil {
			restore()
			return channel.WriteLine(conn, channel.SitesResult{Error: err.Error()})
		}
	}
	for name := range previous {
		if _, keep := files[name]; !keep {
			os.Remove(filepath.Join(h.ConfDir, name))
		}
	}
	if out, err := h.Run(ctx, "nginx", "-t"); err != nil {
		restore()
		return channel.WriteLine(conn, channel.SitesResult{Error: "nginx 拒绝了新配置，已恢复原来的站点", Output: tailString(out, 4096)})
	}
	out, err := h.Run(ctx, "systemctl", "reload", "nginx")
	if err != nil {
		// Not running under systemd (or stopped): ask nginx itself.
		if out2, err2 := h.Run(ctx, "nginx", "-s", "reload"); err2 != nil {
			return channel.WriteLine(conn, channel.SitesResult{Error: "配置已写入并通过检查，但重载 nginx 失败", Output: tailString(append(out, out2...), 4096)})
		}
	}
	return channel.WriteLine(conn, channel.SitesResult{})
}
