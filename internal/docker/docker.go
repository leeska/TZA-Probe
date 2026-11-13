// Package docker is the Docker module: containers, images and compose
// projects on nodes with the docker capability (managed Agents talk to the
// local Docker Engine). Every change and every stream (logs, pulls, compose
// actions, a shell in a container) is written to the audit log.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/httpjson"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/remote"
)

// NodeLister is the part of the core store the module reads.
type NodeLister interface {
	Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error)
}

// Module serves Docker. Remote relays the streams (its Bridge); its Hub
// holds the channels.
type Module struct {
	Remote *remote.Module
	Nodes  NodeLister
}

func (*Module) ID() string { return "docker" }

var (
	containerRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	imageID      = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)
	imageRef     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,254}$`)
	projectName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// NodeEntry is a node as the Docker page lists it.
type NodeEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Capable   bool   `json:"capable"`
}

// Overview is one node's Docker state. Containers and Images are the
// Engine's own JSON (/containers/json?all=1, /images/json).
type Overview struct {
	Version    json.RawMessage     `json:"version,omitempty"`
	Containers json.RawMessage     `json:"containers,omitempty"`
	Images     json.RawMessage     `json:"images,omitempty"`
	Compose    channel.ComposeList `json:"compose"`
	Error      string              `json:"error,omitempty"`
}

func (m *Module) Routes(r module.Routes) {
	if m.Remote == nil || m.Remote.Hub == nil {
		return
	}
	r.Admin("GET /api/v1/admin/docker", func(w http.ResponseWriter, req *http.Request) {
		nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		out := []NodeEntry{}
		for _, n := range nodes {
			st := m.Remote.Hub.Status(n.ID)
			out = append(out, NodeEntry{ID: n.ID, Name: n.Name, Connected: st.Connected, Capable: slices.Contains(st.Capabilities, channel.CapabilityDocker)})
		}
		httpjson.Write(w, 200, out)
	})
	r.Admin("GET /api/v1/admin/docker/nodes/{node}", func(w http.ResponseWriter, req *http.Request) {
		node, ok := m.node(w, req)
		if !ok {
			return
		}
		httpjson.Write(w, 200, m.overview(req.Context(), node))
	})
	r.Admin("POST /api/v1/admin/docker/nodes/{node}/containers/{id}/{action}", func(w http.ResponseWriter, req *http.Request) {
		node, ok := m.node(w, req)
		if !ok {
			return
		}
		id, action := req.PathValue("id"), req.PathValue("action")
		if !containerRef.MatchString(id) || !slices.Contains([]string{"start", "stop", "restart"}, action) {
			httpjson.Error(w, 400, "容器或操作无效")
			return
		}
		m.apiCall(w, req, r, node, channel.DockerAPIParams{Method: "POST", Path: "/containers/" + id + "/" + action}, "docker.container."+action, id)
	})
	r.Admin("DELETE /api/v1/admin/docker/nodes/{node}/containers/{id}", func(w http.ResponseWriter, req *http.Request) {
		node, ok := m.node(w, req)
		if !ok {
			return
		}
		id := req.PathValue("id")
		if !containerRef.MatchString(id) {
			httpjson.Error(w, 400, "容器无效")
			return
		}
		m.apiCall(w, req, r, node, channel.DockerAPIParams{Method: "DELETE", Path: "/containers/" + id, Query: map[string]string{"force": "1"}}, "docker.container.remove", id)
	})
	r.Admin("DELETE /api/v1/admin/docker/nodes/{node}/images/{id}", func(w http.ResponseWriter, req *http.Request) {
		node, ok := m.node(w, req)
		if !ok {
			return
		}
		id := req.PathValue("id")
		if !imageID.MatchString(id) {
			httpjson.Error(w, 400, "镜像无效")
			return
		}
		q := map[string]string{}
		if req.URL.Query().Get("force") == "1" {
			q["force"] = "1"
		}
		m.apiCall(w, req, r, node, channel.DockerAPIParams{Method: "DELETE", Path: "/images/" + id, Query: q}, "docker.image.remove", id)
	})
	r.Admin("GET /api/v1/admin/docker/nodes/{node}/compose/{project}", func(w http.ResponseWriter, req *http.Request) {
		node, ok := m.node(w, req)
		if !ok {
			return
		}
		var file channel.ComposeFile
		if err := m.call(req.Context(), node, channel.KindComposeRead, channel.ComposeParams{Project: req.PathValue("project")}, &file); err != nil {
			httpjson.Error(w, 502, err.Error())
			return
		}
		if file.Error != "" {
			httpjson.Error(w, 400, file.Error)
			return
		}
		httpjson.Write(w, 200, file)
	})
	r.Admin("PUT /api/v1/admin/docker/nodes/{node}/compose/{project}", func(w http.ResponseWriter, req *http.Request) {
		node, ok := m.node(w, req)
		if !ok {
			return
		}
		var in struct {
			Content string `json:"content"`
		}
		if !httpjson.Decode(w, req, &in) {
			return
		}
		project := req.PathValue("project")
		if !projectName.MatchString(project) {
			httpjson.Error(w, 400, "项目名只能用小写字母、数字、- 和 _，以字母或数字开头")
			return
		}
		if strings.TrimSpace(in.Content) == "" || len(in.Content) > channel.MaxComposeFile {
			httpjson.Error(w, 400, "compose 内容为空或超过 256 KB")
			return
		}
		var res channel.DockerResult
		err := m.call(req.Context(), node, channel.KindComposeWrite, channel.ComposeWriteParams{Project: project, Content: in.Content}, &res)
		entry := model.AuditEntry{Action: "docker.compose.write", NodeID: node, Detail: fmt.Sprintf("%s（%d 字节）", project, len(in.Content))}
		switch {
		case err != nil:
			entry.Status = 502
			r.Audit(req, entry)
			httpjson.Error(w, 502, err.Error())
		case res.Error != "":
			entry.Status = 400
			r.Audit(req, entry)
			httpjson.Write(w, 400, map[string]string{"error": res.Error, "output": res.Output})
		default:
			entry.Status = 200
			r.Audit(req, entry)
			httpjson.Write(w, 200, map[string]bool{"ok": true})
		}
	})
	// 流：日志、拉镜像、Compose 操作和容器终端，都经远程管理的 WebSocket 转接。
	r.Admin("GET /api/v1/admin/docker/stream", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		node := q.Get("node")
		if !m.capable(node) {
			httpjson.Error(w, 409, "这台节点没有开启 Docker 管理：在节点上重跑接入命令并加 --manage")
			return
		}
		cols, _ := strconv.Atoi(q.Get("cols"))
		rows, _ := strconv.Atoi(q.Get("rows"))
		switch q.Get("kind") {
		case "logs":
			c := q.Get("container")
			if !containerRef.MatchString(c) {
				httpjson.Error(w, 400, "容器无效")
				return
			}
			tail, _ := strconv.Atoi(q.Get("tail"))
			m.Remote.Bridge(w, req, r, node, channel.KindDockerLogs, channel.DockerLogsParams{Container: c, Tail: tail, Follow: q.Get("follow") == "1", Timestamps: q.Get("timestamps") == "1"}, "docker.logs", c)
		case "pull":
			image := q.Get("image")
			if !imageRef.MatchString(image) {
				httpjson.Error(w, 400, "镜像名无效")
				return
			}
			m.Remote.Bridge(w, req, r, node, channel.KindDockerPull, channel.DockerPullParams{Image: image}, "docker.image.pull", image)
		case "compose":
			project, action := q.Get("project"), q.Get("action")
			if !projectName.MatchString(project) || !slices.Contains([]string{"up", "down", "pull", "restart", "delete"}, action) {
				httpjson.Error(w, 400, "项目或操作无效")
				return
			}
			volumes := action == "delete" && q.Get("volumes") == "1"
			detail := project
			if volumes {
				detail += "（同时删除数据卷）"
			}
			m.Remote.Bridge(w, req, r, node, channel.KindComposeRun, channel.ComposeRunParams{Project: project, Action: action, Volumes: volumes}, "docker.compose."+action, detail)
		case "exec":
			c := q.Get("container")
			if !containerRef.MatchString(c) {
				httpjson.Error(w, 400, "容器无效")
				return
			}
			m.Remote.Bridge(w, req, r, node, channel.KindDockerExec, channel.DockerExecParams{Container: c, Cols: cols, Rows: rows}, "docker.exec", c)
		default:
			httpjson.Error(w, 400, "未知的流")
		}
	})
}

func (m *Module) capable(node string) bool {
	st := m.Remote.Hub.Status(node)
	return st.Connected && slices.Contains(st.Capabilities, channel.CapabilityDocker)
}

// node checks the path's node exists and has the docker capability.
func (m *Module) node(w http.ResponseWriter, req *http.Request) (string, bool) {
	node := req.PathValue("node")
	nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
	if err != nil {
		httpjson.Fail(w, err)
		return "", false
	}
	if !slices.ContainsFunc(nodes, func(n model.NodeView) bool { return n.ID == node }) {
		httpjson.Error(w, 404, "节点不存在")
		return "", false
	}
	if !m.capable(node) {
		httpjson.Error(w, 409, "这台节点没有开启 Docker 管理：在节点上重跑接入命令并加 --manage")
		return "", false
	}
	return node, true
}

// call opens a request and reads its one-line answer.
func (m *Module) call(ctx context.Context, node, kind string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	conn, err := m.Remote.Hub.Open(ctx, node, kind, params)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Minute))
	return channel.ReadLine(conn, out)
}

func (m *Module) engine(ctx context.Context, node string, p channel.DockerAPIParams) (channel.DockerAPIResult, error) {
	var res channel.DockerAPIResult
	if err := m.call(ctx, node, channel.KindDockerAPI, p, &res); err != nil {
		return res, err
	}
	if res.Error != "" && res.Status == 0 {
		return res, errors.New(res.Error)
	}
	return res, nil
}

// apiCall performs a write and answers with the Engine's status.
func (m *Module) apiCall(w http.ResponseWriter, req *http.Request, r module.Routes, node string, p channel.DockerAPIParams, action, detail string) {
	res, err := m.engine(req.Context(), node, p)
	entry := model.AuditEntry{Action: action, NodeID: node, Detail: detail}
	if err != nil {
		entry.Status = 502
		r.Audit(req, entry)
		httpjson.Error(w, 502, err.Error())
		return
	}
	entry.Status = res.Status
	r.Audit(req, entry)
	switch {
	case res.Status == 304: // already started / stopped
		httpjson.Write(w, 200, map[string]bool{"ok": true})
	case res.Status >= 400:
		httpjson.Error(w, res.Status, res.Error)
	default:
		httpjson.Write(w, 200, map[string]bool{"ok": true})
	}
}

func (m *Module) overview(ctx context.Context, node string) Overview {
	var out Overview
	version, err := m.engine(ctx, node, channel.DockerAPIParams{Method: "GET", Path: "/version"})
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Version = version.Body
	if c, err := m.engine(ctx, node, channel.DockerAPIParams{Method: "GET", Path: "/containers/json", Query: map[string]string{"all": "1"}}); err == nil {
		out.Containers = c.Body
	} else {
		out.Error = err.Error()
	}
	if i, err := m.engine(ctx, node, channel.DockerAPIParams{Method: "GET", Path: "/images/json"}); err == nil {
		out.Images = i.Body
	} else if out.Error == "" {
		out.Error = err.Error()
	}
	if err := m.call(ctx, node, channel.KindComposeList, nil, &out.Compose); err != nil && out.Error == "" {
		out.Error = err.Error()
	}
	if out.Compose.Projects == nil {
		out.Compose.Projects = []channel.ComposeProject{}
	}
	return out
}
