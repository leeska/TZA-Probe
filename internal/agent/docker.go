package agent

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/channel"
)

// DockerHost serves the docker capability: Engine API calls on an allow list,
// logs, image pulls, compose projects and a shell in a container. The Agent
// runs as root on a managed host, so reaching the socket is all it needs.
type DockerHost struct {
	// Socket is the Engine API's unix socket.
	Socket string
	// CLI is the docker command (compose actions and the container shell).
	CLI string
	// Root holds the projects made from the panel (channel.ComposeRoot).
	Root   string
	client *http.Client
}

// NewDockerHost finds the socket (/var/run/docker.sock, else Docker
// Desktop's per-user one). TZA_COMPOSE_ROOT overrides the compose root for
// development on a machine where /opt is not writable.
func NewDockerHost() *DockerHost {
	socket := "/var/run/docker.sock"
	if _, err := os.Stat(socket); err != nil {
		if h, err := os.UserHomeDir(); err == nil {
			if alt := filepath.Join(h, ".docker/run/docker.sock"); fileExists(alt) {
				socket = alt
			}
		}
	}
	root := channel.ComposeRoot
	if r := os.Getenv("TZA_COMPOSE_ROOT"); r != "" {
		root = r
	}
	return &DockerHost{Socket: socket, CLI: "docker", Root: root}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func (h *DockerHost) Handlers() map[string]channel.Handler {
	return map[string]channel.Handler{
		channel.KindDockerAPI:    h.api,
		channel.KindDockerLogs:   h.logs,
		channel.KindDockerPull:   h.pull,
		channel.KindComposeList:  h.composeList,
		channel.KindComposeRead:  h.composeRead,
		channel.KindComposeWrite: h.composeWrite,
		channel.KindComposeRun:   h.composeRun,
		channel.KindDockerExec:   h.exec,
	}
}

func (h *DockerHost) http() *http.Client {
	if h.client == nil {
		socket := h.Socket
		h.client = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}}
	}
	return h.client
}

// engine calls the Engine API (unversioned paths: the daemon's own version).
func (h *DockerHost) engine(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	u := "http://docker" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := h.http().Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return nil, errors.New("连不上 Docker：这台机器没有安装 Docker 或服务没在运行")
		}
		return nil, err
	}
	return res, nil
}

var (
	containerRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	imageID      = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)
	imageRef     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,254}$`)
	projectName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// apiRule is one allowed Engine API call: the method, a path pattern and the
// query keys it may carry.
type apiRule struct {
	method string
	path   *regexp.Regexp
	query  []string
}

var apiRules = []apiRule{
	{"GET", regexp.MustCompile(`^/(version|info)$`), nil},
	{"GET", regexp.MustCompile(`^/containers/json$`), []string{"all"}},
	{"GET", regexp.MustCompile(`^/images/json$`), nil},
	{"GET", regexp.MustCompile(`^/containers/[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}/json$`), nil},
	{"POST", regexp.MustCompile(`^/containers/[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}/(start|stop|restart)$`), []string{"t"}},
	{"DELETE", regexp.MustCompile(`^/containers/[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`), []string{"force", "v"}},
	{"DELETE", regexp.MustCompile(`^/images/(sha256:)?[a-f0-9]{12,64}$`), []string{"force"}},
}

func allowed(p channel.DockerAPIParams) bool {
	for _, r := range apiRules {
		if r.method != p.Method || !r.path.MatchString(p.Path) {
			continue
		}
		for k, v := range p.Query {
			if !slices.Contains(r.query, k) || len(v) > 16 {
				return false
			}
		}
		return true
	}
	return false
}

func (h *DockerHost) api(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.DockerAPIParams
	if err := json.Unmarshal(raw, &p); err != nil || !allowed(p) {
		return channel.WriteLine(conn, channel.DockerAPIResult{Error: "不允许的 Docker 请求"})
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	q := url.Values{}
	for k, v := range p.Query {
		q.Set(k, v)
	}
	res, err := h.engine(ctx, p.Method, p.Path, q)
	if err != nil {
		return channel.WriteLine(conn, channel.DockerAPIResult{Error: err.Error()})
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, channel.MaxDockerBody+1))
	if err != nil {
		return channel.WriteLine(conn, channel.DockerAPIResult{Error: err.Error()})
	}
	if len(body) > channel.MaxDockerBody {
		return channel.WriteLine(conn, channel.DockerAPIResult{Error: "Docker 的回答太大"})
	}
	out := channel.DockerAPIResult{Status: res.StatusCode}
	if json.Valid(body) {
		out.Body = body
	}
	if res.StatusCode >= 400 {
		var e struct{ Message string }
		json.Unmarshal(body, &e)
		out.Error = e.Message
		if out.Error == "" {
			out.Error = strings.TrimSpace(string(body))
		}
	}
	return channel.WriteLine(conn, out)
}

// finish ends an output stream with an exit code (0 ok).
func finish(conn net.Conn, err error) error {
	code, msg := 0, ""
	if err != nil {
		code, msg = 1, err.Error()
	}
	return send(conn, channel.Message{Exit: &code, Error: msg})
}

// watchClose cancels ctx when the Server closes the stream.
func watchClose(ctx context.Context, conn net.Conn) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		io.Copy(io.Discard, conn)
		cancel()
	}()
	return ctx, cancel
}

// messageWriter turns written bytes into Output messages.
type messageWriter struct{ conn net.Conn }

func (w messageWriter) Write(p []byte) (int, error) {
	if err := send(w.conn, channel.Message{Output: append([]byte{}, p...)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (h *DockerHost) logs(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.DockerLogsParams
	if err := json.Unmarshal(raw, &p); err != nil || !containerRef.MatchString(p.Container) {
		return finish(conn, errors.New("容器无效"))
	}
	ctx, cancel := watchClose(ctx, conn)
	defer cancel()
	// A container with a TTY logs a raw stream; without one, frames.
	res, err := h.engine(ctx, "GET", "/containers/"+p.Container+"/json", nil)
	if err != nil {
		return finish(conn, err)
	}
	var info struct{ Config struct{ Tty bool } }
	json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if res.StatusCode != 200 {
		return finish(conn, errors.New("容器不存在"))
	}
	tail := "200"
	if p.Tail > 0 && p.Tail <= 10000 {
		tail = fmt.Sprint(p.Tail)
	} else if p.Tail < 0 {
		tail = "all"
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {tail}}
	if p.Follow {
		q.Set("follow", "1")
	}
	if p.Timestamps {
		q.Set("timestamps", "1")
	}
	res, err = h.engine(ctx, "GET", "/containers/"+p.Container+"/logs", q)
	if err != nil {
		return finish(conn, err)
	}
	defer res.Body.Close()
	out := messageWriter{conn}
	if info.Config.Tty {
		_, err = io.Copy(out, res.Body)
	} else {
		err = demux(res.Body, out)
	}
	if ctx.Err() != nil {
		return nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return finish(conn, err)
	}
	return finish(conn, nil)
}

// demux reads the Engine's multiplexed log stream: 8-byte headers (stream,
// 0, 0, 0, big-endian length) each followed by that many bytes. Lines go out
// with \r\n so a terminal shows them as written.
func demux(r io.Reader, w io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(header[4:])
		frame := make([]byte, n)
		if _, err := io.ReadFull(r, frame); err != nil {
			return err
		}
		if _, err := w.Write([]byte(strings.ReplaceAll(string(frame), "\n", "\r\n"))); err != nil {
			return err
		}
	}
}

// splitImage splits "name[:tag]" (or "name@digest") for /images/create.
func splitImage(ref string) (name, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref, ""
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func (h *DockerHost) pull(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.DockerPullParams
	if err := json.Unmarshal(raw, &p); err != nil || !imageRef.MatchString(p.Image) {
		return finish(conn, errors.New("镜像名无效"))
	}
	ctx, cancel := watchClose(ctx, conn)
	defer cancel()
	name, tag := splitImage(p.Image)
	q := url.Values{"fromImage": {name}}
	if tag != "" {
		q.Set("tag", tag)
	}
	res, err := h.engine(ctx, "POST", "/images/create", q)
	if err != nil {
		return finish(conn, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var e struct{ Message string }
		json.NewDecoder(res.Body).Decode(&e)
		return finish(conn, fmt.Errorf("拉取失败：%s", e.Message))
	}
	// Progress lines, one per layer and step; the byte counters in between
	// are left out.
	last := map[string]string{}
	decoder := json.NewDecoder(res.Body)
	for {
		var m struct {
			Status, ID, Error string
		}
		if err := decoder.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return finish(conn, nil)
			}
			return finish(conn, err)
		}
		if m.Error != "" {
			return finish(conn, errors.New(m.Error))
		}
		if last[m.ID] == m.Status || m.Status == "Downloading" || m.Status == "Extracting" {
			continue
		}
		last[m.ID] = m.Status
		line := m.Status
		if m.ID != "" {
			line = m.ID + "：" + m.Status
		}
		send(conn, channel.Message{Output: []byte(line + "\r\n")})
	}
}

// containerSummary is the part of /containers/json the compose views read.
type containerSummary struct {
	ID     string `json:"Id"`
	State  string
	Labels map[string]string
}

func (h *DockerHost) containers(ctx context.Context) ([]containerSummary, error) {
	res, err := h.engine(ctx, "GET", "/containers/json", url.Values{"all": {"1"}})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var list []containerSummary
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// projects finds compose projects: from container labels and from the
// directories under Root that hold a compose.yaml.
func (h *DockerHost) projects(ctx context.Context) ([]channel.ComposeProject, error) {
	list, err := h.containers(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*channel.ComposeProject{}
	for _, c := range list {
		name := c.Labels["com.docker.compose.project"]
		if !projectName.MatchString(name) {
			continue
		}
		p := byName[name]
		if p == nil {
			p = &channel.ComposeProject{Name: name, Dir: c.Labels["com.docker.compose.project.working_dir"]}
			for _, f := range strings.Split(c.Labels["com.docker.compose.project.config_files"], ",") {
				if f = strings.TrimSpace(f); filepath.IsAbs(f) {
					p.Files = append(p.Files, f)
				}
			}
			byName[name] = p
		}
		p.Containers++
		if c.State == "running" {
			p.Running++
		}
	}
	entries, _ := os.ReadDir(h.Root)
	for _, e := range entries {
		file := filepath.Join(h.Root, e.Name(), "compose.yaml")
		if !e.IsDir() || !projectName.MatchString(e.Name()) || !fileExists(file) {
			continue
		}
		p := byName[e.Name()]
		if p == nil {
			p = &channel.ComposeProject{Name: e.Name()}
			byName[e.Name()] = p
		}
		p.Dir, p.Files, p.Managed = filepath.Join(h.Root, e.Name()), []string{file}, true
	}
	out := []channel.ComposeProject{}
	for _, p := range byName {
		if p.Files == nil {
			p.Files = []string{}
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// project resolves a project by name from what the Agent itself sees, never
// from paths the Server sends.
func (h *DockerHost) project(ctx context.Context, name string) (channel.ComposeProject, error) {
	if !projectName.MatchString(name) {
		return channel.ComposeProject{}, errors.New("项目名只能用小写字母、数字、- 和 _")
	}
	list, err := h.projects(ctx)
	if err != nil {
		return channel.ComposeProject{}, err
	}
	for _, p := range list {
		if p.Name == name {
			if len(p.Files) == 0 || !filepath.IsAbs(p.Dir) {
				return p, errors.New("找不到这个项目的 compose 文件")
			}
			return p, nil
		}
	}
	return channel.ComposeProject{}, errNoProject
}

var errNoProject = errors.New("项目不存在")

func (h *DockerHost) cliEnv() []string {
	env := shellEnv()
	if h.Socket != "/var/run/docker.sock" {
		env = append(env, "DOCKER_HOST=unix://"+h.Socket)
	}
	return env
}

func (h *DockerHost) compose(ctx context.Context, p channel.ComposeProject, files []string, args ...string) *exec.Cmd {
	all := []string{"compose", "--project-name", p.Name, "--project-directory", p.Dir}
	for _, f := range files {
		all = append(all, "-f", f)
	}
	cmd := exec.CommandContext(ctx, h.CLI, append(all, args...)...)
	cmd.Env = h.cliEnv() // --project-directory sets the directory; no chdir
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	return cmd
}

func (h *DockerHost) composeList(ctx context.Context, _ json.RawMessage, conn net.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	list, err := h.projects(ctx)
	if err != nil {
		return channel.WriteLine(conn, channel.ComposeList{Projects: []channel.ComposeProject{}, Error: err.Error()})
	}
	out := channel.ComposeList{Projects: list}
	cmd := exec.CommandContext(ctx, h.CLI, "compose", "version", "--short")
	cmd.Env = h.cliEnv()
	if v, err := cmd.Output(); err == nil {
		out.Compose = strings.TrimSpace(string(v))
	}
	return channel.WriteLine(conn, out)
}

func (h *DockerHost) composeRead(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.ComposeParams
	json.Unmarshal(raw, &p)
	project, err := h.project(ctx, p.Project)
	if err != nil {
		return channel.WriteLine(conn, channel.ComposeFile{Error: err.Error()})
	}
	f, err := os.Open(project.Files[0])
	if err != nil {
		return channel.WriteLine(conn, channel.ComposeFile{Error: "读不到 compose 文件：" + err.Error()})
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, channel.MaxComposeFile+1))
	if err != nil || len(content) > channel.MaxComposeFile {
		return channel.WriteLine(conn, channel.ComposeFile{Error: "compose 文件太大或读不出"})
	}
	return channel.WriteLine(conn, channel.ComposeFile{Path: project.Files[0], Content: string(content)})
}

// composeWrite saves a compose file after `docker compose config` accepts
// it. A name the Agent does not know yet becomes a new project under Root;
// the previous file is kept next to it as .tza-bak.
func (h *DockerHost) composeWrite(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.ComposeWriteParams
	if err := json.Unmarshal(raw, &p); err != nil || len(p.Content) > channel.MaxComposeFile || strings.TrimSpace(p.Content) == "" {
		return channel.WriteLine(conn, channel.DockerResult{Error: "compose 内容为空或太大"})
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	project, err := h.project(ctx, p.Project)
	switch {
	case errors.Is(err, errNoProject): // new: make it under Root
		dir := filepath.Join(h.Root, p.Project)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return channel.WriteLine(conn, channel.DockerResult{Error: err.Error()})
		}
		project = channel.ComposeProject{Name: p.Project, Dir: dir, Files: []string{filepath.Join(dir, "compose.yaml")}, Managed: true}
	case err != nil:
		return channel.WriteLine(conn, channel.DockerResult{Error: err.Error()})
	}
	target := project.Files[0]
	var suffix [6]byte
	rand.Read(suffix[:])
	candidate := filepath.Join(filepath.Dir(target), ".tza-"+hex.EncodeToString(suffix[:])+".yaml")
	if err := os.WriteFile(candidate, []byte(p.Content), 0600); err != nil {
		return channel.WriteLine(conn, channel.DockerResult{Error: err.Error()})
	}
	files := append([]string{candidate}, project.Files[1:]...)
	if out, err := h.compose(ctx, project, files, "config", "--quiet").CombinedOutput(); err != nil {
		os.Remove(candidate)
		return channel.WriteLine(conn, channel.DockerResult{Error: "compose 文件没有通过检查", Output: tailString(out, 4096)})
	}
	if old, err := os.ReadFile(target); err == nil {
		os.WriteFile(target+".tza-bak", old, 0600)
	}
	if err := os.Rename(candidate, target); err != nil {
		os.Remove(candidate)
		return channel.WriteLine(conn, channel.DockerResult{Error: err.Error()})
	}
	return channel.WriteLine(conn, channel.DockerResult{})
}

func tailString(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

var composeActions = map[string][]string{
	"up":      {"up", "--detach", "--remove-orphans"},
	"down":    {"down"},
	"pull":    {"pull"},
	"restart": {"restart"},
	"delete":  {"down", "--remove-orphans"},
}

func (h *DockerHost) composeRun(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.ComposeRunParams
	json.Unmarshal(raw, &p)
	args, ok := composeActions[p.Action]
	if !ok {
		return finish(conn, errors.New("未知操作"))
	}
	ctx, cancel := watchClose(ctx, conn)
	defer cancel()
	project, err := h.project(ctx, p.Project)
	if err != nil {
		return finish(conn, err)
	}
	if p.Action == "delete" && p.Volumes {
		args = append(args, "--volumes")
	}
	cmd := h.compose(ctx, project, project.Files, args...)
	out := &crlfWriter{w: messageWriter{conn}}
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return send(conn, channel.Message{Exit: &code, Error: "docker compose " + args[0] + " 失败"})
	}
	if err != nil {
		return finish(conn, fmt.Errorf("运行 docker compose 失败（节点装了 compose 插件吗？）：%w", err))
	}
	if p.Action == "delete" {
		if !project.Managed {
			out.Write([]byte("这个项目不是在面板里建的，容器已移除，文件保留在 " + project.Dir + "\n"))
			return finish(conn, nil)
		}
		// Only a directory directly under Root is ever removed.
		if rel, err := filepath.Rel(h.Root, project.Dir); err != nil || rel != project.Name {
			return finish(conn, errors.New("项目目录不在 compose 根目录下，未删除"))
		}
		if err := os.RemoveAll(project.Dir); err != nil {
			return finish(conn, fmt.Errorf("删除项目目录失败：%w", err))
		}
		out.Write([]byte("已删除项目目录 " + project.Dir + "\n"))
	}
	return finish(conn, nil)
}

// crlfWriter makes \n into \r\n for the panel's terminal view.
type crlfWriter struct{ w io.Writer }

func (c *crlfWriter) Write(p []byte) (int, error) {
	if _, err := c.w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(string(p), "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// exec opens a shell in a running container: bash when it has one, else sh.
func (h *DockerHost) exec(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.DockerExecParams
	if err := json.Unmarshal(raw, &p); err != nil || !containerRef.MatchString(p.Container) {
		return send(conn, channel.Message{Error: "容器无效", Exit: intPtr(-1)})
	}
	return runTerminal(ctx, conn, p.Cols, p.Rows, h.cliEnv(), h.CLI, "exec", "-it", "-e", "TERM=xterm-256color", p.Container,
		"sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi")
}
