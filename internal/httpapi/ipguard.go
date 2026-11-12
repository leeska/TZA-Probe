package httpapi

import (
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

func (a *API) ipguardRoutes(mux *http.ServeMux) {
	if a.IPGuard == nil {
		return
	}
	broker := a.IPGuard
	locator := ipguard.NewLocator()
	mux.HandleFunc("GET /api/v1/admin/ipguard/places", a.admin(func(w http.ResponseWriter, r *http.Request) {
		places, err := locator.Search(r.Context(), r.URL.Query().Get("q"))
		if err != nil {
			write(w, 502, map[string]string{"error": err.Error()})
			return
		}
		write(w, 200, places)
	}))
	mux.HandleFunc("GET /api/v1/admin/ipguard/location", a.admin(func(w http.ResponseWriter, r *http.Request) {
		status := broker.Status(r.URL.Query().Get("node"))
		family := r.URL.Query().Get("family")
		if family != "ipv4" && family != "ipv6" {
			write(w, 400, map[string]string{"error": "请选择 IPv4 或 IPv6"})
			return
		}
		if !status.Enabled || time.Now().Unix()-status.ReceivedAt >= 45 {
			write(w, 409, map[string]string{"error": "节点检测出口未连接"})
			return
		}
		for _, ip := range status.AllowedIPs {
			if requested := r.URL.Query().Get("ip"); requested != "" && requested != ip {
				continue
			}
			if strings.Contains(ip, ":") != (family == "ipv6") {
				continue
			}
			position, err := locator.Locate(r.Context(), ip, 0, 0)
			if err != nil {
				write(w, 502, map[string]string{"error": err.Error()})
				return
			}
			write(w, 200, map[string]any{"ip": ip, "position": position, "source": "ipwho.is", "approximate": true})
			return
		}
		write(w, 409, map[string]string{"error": "所选地址族没有已授权出口"})
	}))
	mux.HandleFunc("POST /api/v1/admin/ipguard/location", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Latitude == nil || input.Longitude == nil || math.Abs(*input.Latitude) > 90 || math.Abs(*input.Longitude) > 180 {
			write(w, 400, map[string]string{"error": "请填写有效经纬度"})
			return
		}
		position, err := locator.Locate(r.Context(), "", *input.Latitude, *input.Longitude)
		if err != nil {
			write(w, 502, map[string]string{"error": err.Error()})
			return
		}
		write(w, 200, position)
	}))
	mux.HandleFunc("GET /api/v1/admin/ipguard", a.admin(func(w http.ResponseWriter, r *http.Request) {
		policies, err := broker.Store.Policies(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		jobs, err := broker.Store.Jobs(r.Context(), r.URL.Query().Get("node"))
		if err != nil {
			fail(w, err)
			return
		}
		nodes, err := a.Store.Nodes(r.Context(), time.Now())
		if err != nil {
			fail(w, err)
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
		write(w, 200, map[string]any{"nodes": entries, "jobs": jobs, "environments": ipguard.Environments, "services": ipguard.Services, "worker_seen": broker.WorkerSeen(), "execution_location": "controller"})
	}))
	mux.HandleFunc("PUT /api/v1/admin/ipguard/nodes/{id}", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var settings ipguard.Settings
		if !decode(w, r, &settings) {
			return
		}
		nodes, err := a.Store.Nodes(r.Context(), time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		exists := false
		for _, node := range nodes {
			if node.ID == r.PathValue("id") {
				exists = true
			}
		}
		if !exists {
			write(w, 404, map[string]string{"error": "节点不存在"})
			return
		}
		for _, service := range settings.Services {
			if service != "google" && service != "meta" {
				write(w, 400, map[string]string{"error": "仅支持 Google 和 Meta 地区偏移检测"})
				return
			}
		}
		policies, err := broker.Store.Policies(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		policy, err := settings.Policy(policies[r.PathValue("id")], broker.Status(r.PathValue("id")))
		if err != nil {
			write(w, 409, map[string]string{"error": err.Error()})
			return
		}
		value, err := broker.Store.SavePolicy(r.Context(), r.PathValue("id"), policy)
		if err != nil {
			write(w, 409, map[string]string{"error": err.Error()})
			return
		}
		write(w, 200, value)
	}))
	mux.HandleFunc("POST /api/v1/admin/ipguard/jobs", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Node    string `json:"node_id"`
			Service string `json:"service"`
		}
		if !decode(w, r, &input) {
			return
		}
		status := broker.Status(input.Node)
		if !status.Enabled || time.Now().Unix()-status.ReceivedAt > 45 || time.Now().Unix()-broker.WorkerSeen() > 30 {
			write(w, 409, map[string]string{"error": "浏览器或出口 Agent 尚未就绪"})
			return
		}
		if input.Service != "google" && input.Service != "meta" {
			write(w, 400, map[string]string{"error": "服务无效"})
			return
		}
		job, err := broker.Store.Enqueue(r.Context(), input.Node, input.Service, "baseline", "")
		if err != nil {
			write(w, 409, map[string]string{"error": err.Error()})
			return
		}
		write(w, 202, job)
	}))
	mux.HandleFunc("DELETE /api/v1/admin/ipguard/jobs/{id}", a.admin(func(w http.ResponseWriter, r *http.Request) {
		respond(w, broker.Store.Cancel(r.Context(), r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/v1/agent/ipguard/status", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		var status ipguard.Status
		if !decode(w, r, &status) {
			return
		}
		broker.Report(node, status)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /api/v1/agent/ipguard/streams", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		offer, err := broker.Poll(r.Context(), node)
		if err != nil {
			return
		}
		write(w, 200, offer)
	}))
	mux.HandleFunc("GET /api/v1/agent/ipguard/streams/{id}", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		if err := broker.Accept(w, r, node, r.PathValue("id"), r.Header.Get("X-Stream-Secret")); err != nil {
			write(w, 403, map[string]string{"error": "通道未授权"})
		}
	}))
}
