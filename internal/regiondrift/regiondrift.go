// Package regiondrift is the region drift module (地区偏移检测): policies per
// node, the browser worker's job queue and the egress relay through the
// node's own Agent. The checking itself lives in internal/ipguard and
// internal/browserworker; this package mounts it on the Server.
package regiondrift

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/httpjson"
	"github.com/leeska/TZA-Probe/internal/ipguard"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/storage"
)

// NodeLister is the part of the core store the module reads.
type NodeLister interface {
	Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error)
}

// Module serves region drift. Broker may be nil before the database is open
// (only Migrations is used then).
type Module struct {
	Broker *ipguard.Broker
	Nodes  NodeLister
}

func (*Module) ID() string { return "regiondrift" }

func (*Module) Migrations() []storage.Migration { return ipguard.Migrations() }

func (m *Module) Routes(r module.Routes) {
	if m.Broker == nil {
		return
	}
	broker := m.Broker
	locator := ipguard.NewLocator()
	r.Admin("GET /api/v1/admin/ipguard/places", func(w http.ResponseWriter, req *http.Request) {
		places, err := locator.Search(req.Context(), req.URL.Query().Get("q"))
		if err != nil {
			httpjson.Error(w, 502, err.Error())
			return
		}
		httpjson.Write(w, 200, places)
	})
	r.Admin("GET /api/v1/admin/ipguard/location", func(w http.ResponseWriter, req *http.Request) {
		status := broker.Status(req.URL.Query().Get("node"))
		family := req.URL.Query().Get("family")
		if family != "ipv4" && family != "ipv6" {
			httpjson.Error(w, 400, "请选择 IPv4 或 IPv6")
			return
		}
		if !status.Enabled || time.Now().Unix()-status.ReceivedAt >= 45 {
			httpjson.Error(w, 409, "节点检测出口未连接")
			return
		}
		for _, ip := range status.AllowedIPs {
			if requested := req.URL.Query().Get("ip"); requested != "" && requested != ip {
				continue
			}
			if strings.Contains(ip, ":") != (family == "ipv6") {
				continue
			}
			position, err := locator.Locate(req.Context(), ip, 0, 0)
			if err != nil {
				httpjson.Error(w, 502, err.Error())
				return
			}
			httpjson.Write(w, 200, map[string]any{"ip": ip, "position": position, "source": "ipwho.is", "approximate": true})
			return
		}
		httpjson.Error(w, 409, "所选地址族没有已授权出口")
	})
	r.Admin("POST /api/v1/admin/ipguard/location", func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
		}
		if !httpjson.Decode(w, req, &input) {
			return
		}
		if input.Latitude == nil || input.Longitude == nil || math.Abs(*input.Latitude) > 90 || math.Abs(*input.Longitude) > 180 {
			httpjson.Error(w, 400, "请填写有效经纬度")
			return
		}
		position, err := locator.Locate(req.Context(), "", *input.Latitude, *input.Longitude)
		if err != nil {
			httpjson.Error(w, 502, err.Error())
			return
		}
		httpjson.Write(w, 200, position)
	})
	r.Admin("GET /api/v1/admin/ipguard", func(w http.ResponseWriter, req *http.Request) {
		policies, err := broker.Store.Policies(req.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		jobs, err := broker.Store.Jobs(req.Context(), req.URL.Query().Get("node"))
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		type entry struct {
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Policy *ipguard.Policy `json:"policy"`
			Egress ipguard.Status  `json:"egress"`
		}
		entries := []entry{}
		for _, node := range nodes {
			value := entry{ID: node.ID, Name: node.Name, Egress: broker.Status(node.ID)}
			if policy, ok := policies[node.ID]; ok {
				value.Policy = &policy
			}
			entries = append(entries, value)
		}
		httpjson.Write(w, 200, map[string]any{"nodes": entries, "jobs": jobs, "environments": ipguard.Environments, "services": ipguard.Services, "worker_seen": broker.WorkerSeen(), "worker_configured": broker.WorkerConfigured, "execution_location": "controller"})
	})
	r.Admin("PUT /api/v1/admin/ipguard/nodes/{id}", func(w http.ResponseWriter, req *http.Request) {
		var settings ipguard.Settings
		if !httpjson.Decode(w, req, &settings) {
			return
		}
		nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		exists := false
		for _, node := range nodes {
			if node.ID == req.PathValue("id") {
				exists = true
			}
		}
		if !exists {
			httpjson.Error(w, 404, "节点不存在")
			return
		}
		for _, service := range settings.Services {
			if service != "google" && service != "meta" {
				httpjson.Error(w, 400, "仅支持 Google 和 Meta 地区偏移检测")
				return
			}
		}
		policies, err := broker.Store.Policies(req.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		policy, err := settings.Policy(policies[req.PathValue("id")], broker.Status(req.PathValue("id")))
		if err != nil {
			httpjson.Error(w, 409, err.Error())
			return
		}
		value, err := broker.Store.SavePolicy(req.Context(), req.PathValue("id"), policy)
		if err != nil {
			httpjson.Error(w, 409, err.Error())
			return
		}
		httpjson.Write(w, 200, value)
	})
	r.Admin("POST /api/v1/admin/ipguard/jobs", func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Node    string `json:"node_id"`
			Service string `json:"service"`
		}
		if !httpjson.Decode(w, req, &input) {
			return
		}
		status := broker.Status(input.Node)
		if !status.Enabled || time.Now().Unix()-status.ReceivedAt > 45 || time.Now().Unix()-broker.WorkerSeen() > 30 {
			httpjson.Error(w, 409, "浏览器或出口 Agent 尚未就绪")
			return
		}
		if input.Service != "google" && input.Service != "meta" {
			httpjson.Error(w, 400, "服务无效")
			return
		}
		job, err := broker.Store.Enqueue(req.Context(), input.Node, input.Service, "baseline", "")
		if err != nil {
			httpjson.Error(w, 409, err.Error())
			return
		}
		httpjson.Write(w, 202, job)
	})
	r.Admin("DELETE /api/v1/admin/ipguard/jobs/{id}", func(w http.ResponseWriter, req *http.Request) {
		httpjson.Respond(w, broker.Store.Cancel(req.Context(), req.PathValue("id")))
	})
	r.Agent("POST /api/v1/agent/ipguard/status", func(w http.ResponseWriter, req *http.Request, node string) {
		var status ipguard.Status
		if !httpjson.Decode(w, req, &status) {
			return
		}
		broker.Report(node, status)
		w.WriteHeader(204)
	})
	r.Agent("GET /api/v1/agent/ipguard/streams", func(w http.ResponseWriter, req *http.Request, node string) {
		offer, err := broker.Poll(req.Context(), node)
		if err != nil {
			return
		}
		httpjson.Write(w, 200, offer)
	})
	r.Agent("GET /api/v1/agent/ipguard/streams/{id}", func(w http.ResponseWriter, req *http.Request, node string) {
		if err := broker.Accept(w, req, node, req.PathValue("id"), req.Header.Get("X-Stream-Secret")); err != nil {
			httpjson.Error(w, 403, "通道未授权")
		}
	})
}
