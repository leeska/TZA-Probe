package agent

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leeska/TZA-Probe/internal/channel"
)

// fakeEngine is a Docker Engine API on a unix socket that records requests.
type fakeEngine struct {
	mu       sync.Mutex
	requests []string
}

func (f *fakeEngine) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.requests, "\n")
}

func startEngine(t *testing.T, root string) (*DockerHost, *fakeEngine) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tzad") // unix socket paths are short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeEngine{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/version":
			w.Write([]byte(`{"Version":"27.3.1"}`))
		case r.URL.Path == "/containers/json":
			w.Write([]byte(`[{"Id":"aaa","State":"running","Labels":{"com.docker.compose.project":"web","com.docker.compose.project.working_dir":"/srv/web","com.docker.compose.project.config_files":"/srv/web/docker-compose.yml"}},{"Id":"bbb","State":"exited","Labels":{"com.docker.compose.project":"web"}},{"Id":"ccc","State":"running","Labels":{}}]`))
		case r.URL.Path == "/containers/plain/json":
			w.Write([]byte(`{"Config":{"Tty":false}}`))
		case r.URL.Path == "/containers/plain/logs":
			for _, line := range []string{"hello\n", "world\n"} {
				header := make([]byte, 8)
				header[0] = 1
				binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
				w.Write(append(header, line...))
			}
		case r.URL.Path == "/containers/aaa/stop":
			w.WriteHeader(204)
		case r.URL.Path == "/images/create":
			for _, s := range []string{`{"status":"Pulling from library/nginx","id":"latest"}`, `{"status":"Downloading","id":"l1","progress":"[=>]"}`, `{"status":"Pull complete","id":"l1"}`, `{"status":"Pull complete","id":"l1"}`, `{"status":"Status: Downloaded newer image for nginx:latest"}`} {
				w.Write([]byte(s + "\n"))
			}
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"no such thing"}`))
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	// A fake docker CLI: `compose … config` checks the file (refuses one
	// containing "bad"), everything else echoes its arguments.
	cli := filepath.Join(root, "docker")
	os.WriteFile(cli, []byte(`#!/bin/sh
for a in "$@"; do last="$a"; done
case "$*" in
  *"config --quiet"*) for f in "$@"; do case "$f" in *.yaml) if grep -q bad "$f"; then echo "invalid compose" >&2; exit 15; fi;; esac; done; exit 0;;
  *"version --short"*) echo 2.29.7; exit 0;;
esac
echo "ran: $*"
`), 0755)
	return &DockerHost{Socket: socket, CLI: cli, Root: filepath.Join(root, "compose")}, f
}

// call runs a handler over a pipe and returns what it wrote.
func callHandler(t *testing.T, h func(context.Context, json.RawMessage, net.Conn) error, params any) []string {
	t.Helper()
	server, client := net.Pipe()
	raw, _ := json.Marshal(params)
	go func() {
		h(context.Background(), raw, server)
		server.Close()
	}()
	lines := []string{}
	scanner := bufio.NewScanner(client)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	client.Close()
	return lines
}

func output(t *testing.T, lines []string) (string, channel.Message) {
	var out strings.Builder
	var last channel.Message
	for _, l := range lines {
		var m channel.Message
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(l)
		}
		out.Write(m.Output)
		last = m
	}
	return out.String(), last
}

func TestDockerAPIAllowList(t *testing.T) {
	h, engine := startEngine(t, t.TempDir())
	lines := callHandler(t, h.api, channel.DockerAPIParams{Method: "POST", Path: "/containers/aaa/stop"})
	var res channel.DockerAPIResult
	json.Unmarshal([]byte(lines[0]), &res)
	if res.Status != 204 || res.Error != "" {
		t.Fatalf("%+v", res)
	}
	for _, bad := range []channel.DockerAPIParams{
		{Method: "POST", Path: "/containers/create"},
		{Method: "POST", Path: "/containers/aaa/exec"},
		{Method: "GET", Path: "/containers/../../etc/passwd"},
		{Method: "DELETE", Path: "/images/nginx:latest"},
		{Method: "GET", Path: "/containers/json", Query: map[string]string{"filters": "x"}},
		{Method: "POST", Path: "/build"},
	} {
		lines := callHandler(t, h.api, bad)
		json.Unmarshal([]byte(lines[0]), &res)
		if res.Error != "不允许的 Docker 请求" {
			t.Errorf("%+v 要拒绝：%+v", bad, res)
		}
	}
	if strings.Contains(engine.seen(), "/build") || strings.Contains(engine.seen(), "create") {
		t.Fatal("被拒绝的请求不能到达 Docker", engine.seen())
	}
	lines = callHandler(t, h.api, channel.DockerAPIParams{Method: "GET", Path: "/containers/zzz/json"})
	json.Unmarshal([]byte(lines[0]), &res)
	if res.Status != 404 || res.Error != "no such thing" {
		t.Fatalf("Docker 的错误要带回来：%+v", res)
	}
}

func TestDockerLogsAndPull(t *testing.T) {
	h, engine := startEngine(t, t.TempDir())
	out, last := output(t, callHandler(t, h.logs, channel.DockerLogsParams{Container: "plain", Tail: 50}))
	if out != "hello\r\nworld\r\n" || last.Exit == nil || *last.Exit != 0 {
		t.Fatalf("日志解帧：%q %+v", out, last)
	}
	if !strings.Contains(engine.seen(), "GET /containers/plain/logs?stderr=1&stdout=1&tail=50") {
		t.Fatal(engine.seen())
	}
	out, last = output(t, callHandler(t, h.pull, channel.DockerPullParams{Image: "nginx"}))
	if strings.Count(out, "Pull complete") != 1 || strings.Contains(out, "Downloading") || *last.Exit != 0 {
		t.Fatalf("拉取进度：%q", out)
	}
	if !strings.Contains(engine.seen(), "POST /images/create?fromImage=nginx&tag=latest") {
		t.Fatal(engine.seen())
	}
	_, last = output(t, callHandler(t, h.logs, channel.DockerLogsParams{Container: "a;rm -rf /"}))
	if last.Error == "" {
		t.Fatal("非法容器名要拒绝")
	}
	for ref, want := range map[string][2]string{"nginx": {"nginx", "latest"}, "ghcr.io/a/b:1.2": {"ghcr.io/a/b", "1.2"}, "localhost:5000/x": {"localhost:5000/x", "latest"}, "a@sha256:00": {"a@sha256:00", ""}} {
		if n, tag := splitImage(ref); n != want[0] || tag != want[1] {
			t.Errorf("splitImage(%s) = %s %s", ref, n, tag)
		}
	}
}

func TestComposeProjects(t *testing.T) {
	root := t.TempDir()
	h, _ := startEngine(t, root)
	// 面板新建：写到 Root 下，先过 compose config 检查。
	var res channel.DockerResult
	json.Unmarshal([]byte(callHandler(t, h.composeWrite, channel.ComposeWriteParams{Project: "blog", Content: "services:\n  app:\n    image: nginx\n"})[0]), &res)
	if res.Error != "" {
		t.Fatal(res)
	}
	json.Unmarshal([]byte(callHandler(t, h.composeWrite, channel.ComposeWriteParams{Project: "blog", Content: "bad: yes\n"})[0]), &res)
	if res.Error == "" || !strings.Contains(res.Output, "invalid compose") {
		t.Fatalf("没通过检查的不能保存：%+v", res)
	}
	saved, _ := os.ReadFile(filepath.Join(root, "compose/blog/compose.yaml"))
	if !strings.Contains(string(saved), "image: nginx") {
		t.Fatalf("原文件要保留：%s", saved)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "compose/blog"))
	if len(entries) != 1 {
		t.Fatalf("检查失败的临时文件要删掉：%v", entries)
	}
	json.Unmarshal([]byte(callHandler(t, h.composeWrite, channel.ComposeWriteParams{Project: "Bad/../x", Content: "a: 1"})[0]), &res)
	if res.Error == "" {
		t.Fatal("项目名要校验")
	}

	var list channel.ComposeList
	json.Unmarshal([]byte(callHandler(t, h.composeList, nil)[0]), &list)
	if list.Compose != "2.29.7" || len(list.Projects) != 2 {
		t.Fatalf("%+v", list)
	}
	blog, web := list.Projects[0], list.Projects[1]
	if blog.Name != "blog" || !blog.Managed || web.Name != "web" || web.Managed || web.Containers != 2 || web.Running != 1 || web.Dir != "/srv/web" || web.Files[0] != "/srv/web/docker-compose.yml" {
		t.Fatalf("项目：%+v %+v", blog, web)
	}
	var file channel.ComposeFile
	json.Unmarshal([]byte(callHandler(t, h.composeRead, channel.ComposeParams{Project: "blog"})[0]), &file)
	if !strings.Contains(file.Content, "image: nginx") {
		t.Fatal(file)
	}
	out, last := output(t, callHandler(t, h.composeRun, channel.ComposeRunParams{Project: "blog", Action: "up"}))
	if !strings.Contains(out, "compose --project-name blog --project-directory "+filepath.Join(root, "compose/blog")+" -f "+filepath.Join(root, "compose/blog/compose.yaml")+" up --detach --remove-orphans") || *last.Exit != 0 {
		t.Fatalf("up：%q", out)
	}
	_, last = output(t, callHandler(t, h.composeRun, channel.ComposeRunParams{Project: "blog", Action: "exec"}))
	if last.Error == "" {
		t.Fatal("只允许 up/down/pull/restart")
	}
	_, last = output(t, callHandler(t, h.composeRun, channel.ComposeRunParams{Project: "nope", Action: "down"}))
	if last.Error != "项目不存在" {
		t.Fatal(last)
	}
}
