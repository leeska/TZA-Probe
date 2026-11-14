package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-ControlCenter/internal/channel"
)

func applySites(t *testing.T, h *NginxHost, files map[string]string) channel.SitesResult {
	t.Helper()
	body, _ := json.Marshal(files)
	sum := sha256.Sum256(body)
	raw, _ := json.Marshal(channel.SitesApplyParams{Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])})
	server, client := net.Pipe()
	go func() {
		h.apply(context.Background(), raw, server)
		server.Close()
	}()
	client.Write(body)
	var res channel.SitesResult
	if err := channel.ReadLine(client, &res); err != nil {
		t.Fatal(err)
	}
	client.Close()
	return res
}

// 网站：整组替换面板管理的文件；nginx -t 不通过就恢复原样；不碰别的文件。
func TestNginxApply(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "other.conf"), []byte("server { server_name keep.example; }"), 0644)
	var commands []string
	broken := false
	h := &NginxHost{ConfDir: dir, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "nginx" && args[0] == "-t" && broken {
			return []byte("nginx: [emerg] unknown directive \"oops\""), &os.PathError{Op: "nginx", Err: os.ErrInvalid}
		}
		return nil, nil
	}}
	a := map[string]string{"tza-site-a1.conf": "server { server_name a.example; }", "tza-site-b2.conf": "server { server_name b.example; }"}
	if res := applySites(t, h, a); res.Error != "" {
		t.Fatal(res)
	}
	if !strings.Contains(strings.Join(commands, "|"), "systemctl reload nginx") {
		t.Fatal(commands)
	}
	broken = true
	res := applySites(t, h, map[string]string{"tza-site-a1.conf": "oops;", "tza-site-c3.conf": "server {}"})
	if res.Error == "" || !strings.Contains(res.Output, "unknown directive") {
		t.Fatal("nginx -t 失败要报错并带回输出", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "tza-site-a1.conf"))
	if string(got) != a["tza-site-a1.conf"] {
		t.Fatal("失败要恢复原来的文件", string(got))
	}
	if _, err := os.Stat(filepath.Join(dir, "tza-site-c3.conf")); err == nil {
		t.Fatal("失败时新文件要删掉")
	}
	if _, err := os.Stat(filepath.Join(dir, "tza-site-b2.conf")); err != nil {
		t.Fatal("失败时被删的文件要恢复")
	}
	broken = false
	if res := applySites(t, h, map[string]string{}); res.Error != "" {
		t.Fatal(res)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 || left[0].Name() != "other.conf" {
		t.Fatalf("清空后只留下别的文件：%v", left)
	}
	if res := applySites(t, h, map[string]string{"../evil.conf": "x"}); res.Error == "" {
		t.Fatal("文件名要校验")
	}
}

func TestParseNginxDump(t *testing.T) {
	dump := `nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
# configuration file /etc/nginx/nginx.conf:
http { include /etc/nginx/conf.d/*.conf; }
# configuration file /etc/nginx/sites-enabled/blog.conf:
server {
    listen 80;
    server_name blog.example.com www.blog.example.com;
}
server {
    server_name blog.example.com;
}
# configuration file /etc/nginx/conf.d/tza-site-ab12.conf:
server {
    server_name app.example.com;
}
# configuration file /etc/nginx/sites-enabled/default:
server {
    server_name _;
}
`
	sites := parseNginxDump(dump, "/etc/nginx/conf.d")
	if len(sites) != 3 || sites[0].File != "/etc/nginx/sites-enabled/blog.conf" || strings.Join(sites[0].Names, ",") != "blog.example.com,www.blog.example.com" || sites[0].Managed || !sites[1].Managed || len(sites[2].Names) != 0 {
		t.Fatalf("%+v", sites)
	}
}
