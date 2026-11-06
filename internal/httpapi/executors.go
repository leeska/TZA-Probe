package httpapi

import (
	"io"
	"net/http"
	"strconv"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/service"
)

func decodeExecutor(w http.ResponseWriter, r *http.Request, destination any) bool {
	if !jsonRequest(w, r) {
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		write(w, 413, map[string]string{"error": "执行器同步载荷过大"})
		return false
	}
	if err := ipsentinel.DecodeStrict(body, destination); err != nil {
		write(w, 400, map[string]string{"error": "执行器同步载荷无效"})
		return false
	}
	return true
}

func (a *API) executorRoutes(mux *http.ServeMux) {
	if runtimeStore, supported := a.Store.(service.RuntimeStore); supported {
		a.runtimeRoutes(mux, runtimeStore)
	}
	repository, supported := a.Store.(service.ExecutorStore)
	if !supported {
		return
	}
	mux.HandleFunc("GET /api/v1/agent/executors/ipsentinel", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		receipt, err := repository.ExecutorCursor(r.Context(), node)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, receipt)
	}))
	mux.HandleFunc("POST /api/v1/agent/executors/ipsentinel/records", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		var report ipsentinel.SyncReport
		if !decodeExecutor(w, r, &report) {
			return
		}
		receipt, err := repository.SaveExecutorRecords(r.Context(), node, report, time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, receipt)
	}))
	mux.HandleFunc("POST /api/v1/agent/executors/ipsentinel/actions", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		var hello ipsentinel.Hello
		if !decodeExecutor(w, r, &hello) {
			return
		}
		action, err := repository.LeaseExecutorAction(r.Context(), node, hello)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, action)
	}))
	mux.HandleFunc("POST /api/v1/agent/executors/ipsentinel/action-results", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		var result ipsentinel.ActionResult
		if !decodeExecutor(w, r, &result) {
			return
		}
		respond(w, repository.FinishExecutorAction(r.Context(), node, result))
	}))
	mux.HandleFunc("GET /api/v1/admin/executors", a.admin(func(w http.ResponseWriter, r *http.Request) {
		views, err := repository.Executors(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, views)
	}))
	mux.HandleFunc("PUT /api/v1/admin/nodes/{id}/executors/ipsentinel", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var binding ipsentinel.Binding
		if !decodeExecutor(w, r, &binding) {
			return
		}
		respond(w, repository.BindExecutor(r.Context(), r.PathValue("id"), binding))
	}))
	mux.HandleFunc("DELETE /api/v1/admin/nodes/{id}/executors/ipsentinel", a.admin(func(w http.ResponseWriter, r *http.Request) {
		respond(w, repository.UnbindExecutor(r.Context(), r.PathValue("id")))
	}))
	mux.HandleFunc("GET /api/v1/admin/nodes/{id}/executors/ipsentinel/records", a.admin(func(w http.ResponseWriter, r *http.Request) {
		after := int64(0)
		if value := r.URL.Query().Get("after_seq"); value != "" {
			var err error
			after, err = strconv.ParseInt(value, 10, 64)
			if err != nil || after < 0 || after > ipsentinel.MaxSafeInt {
				write(w, 400, map[string]string{"error": "游标无效"})
				return
			}
		}
		records, err := repository.ExecutorRecords(r.Context(), r.PathValue("id"), after)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, records)
	}))
	mux.HandleFunc("POST /api/v1/admin/nodes/{id}/executors/ipsentinel/actions", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var action ipsentinel.ExecutorAction
		if !decodeExecutor(w, r, &action) {
			return
		}
		action.ID = ipsentinel.NewUUID()
		if err := repository.CreateExecutorAction(r.Context(), r.PathValue("id"), action); err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusAccepted, map[string]string{"id": action.ID, "status": "pending"})
	}))
	mux.HandleFunc("GET /api/v1/admin/nodes/{id}/executors/ipsentinel/actions", a.admin(func(w http.ResponseWriter, r *http.Request) {
		actions, err := repository.ExecutorActions(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, actions)
	}))
	mux.HandleFunc("GET /api/v1/admin/nodes/{id}/executors/ipsentinel/schedule", a.admin(func(w http.ResponseWriter, r *http.Request) {
		view, err := repository.ExecutorSchedule(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, view)
	}))
	mux.HandleFunc("PUT /api/v1/admin/nodes/{id}/executors/ipsentinel/schedule", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var input ipsentinel.ScheduleInput
		if !decodeExecutor(w, r, &input) {
			return
		}
		respond(w, repository.SaveExecutorSchedule(r.Context(), r.PathValue("id"), input))
	}))
	mux.HandleFunc("DELETE /api/v1/admin/nodes/{id}/executors/ipsentinel/schedule", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if !decodeExecutor(w, r, &input) {
			return
		}
		respond(w, repository.DisableExecutorSchedule(r.Context(), r.PathValue("id"), input.ExpectedRevision))
	}))
}

func (a *API) runtimeRoutes(mux *http.ServeMux, store service.RuntimeStore) {
	mux.HandleFunc("POST /api/v1/agent/runtime", a.agent(func(w http.ResponseWriter, r *http.Request, node string) {
		var report runtimepkg.ChannelReport
		if !decodeExecutor(w, r, &report) {
			return
		}
		reply, err := store.RuntimeExchange(r.Context(), node, report)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, reply)
	}))
	mux.HandleFunc("GET /api/v1/admin/runtime", a.admin(func(w http.ResponseWriter, r *http.Request) {
		views, err := store.RuntimeChannels(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, views)
	}))
	mux.HandleFunc("POST /api/v1/admin/nodes/{id}/runtime/requests", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var input runtimepkg.ChannelInput
		if !decodeExecutor(w, r, &input) {
			return
		}
		request, err := store.CreateRuntimeRequest(r.Context(), r.PathValue("id"), input)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusAccepted, request)
	}))
	mux.HandleFunc("PUT /api/v1/admin/nodes/{id}/runtime/requests/{request}", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var approval runtimepkg.ChannelApproval
		if !decodeExecutor(w, r, &approval) {
			return
		}
		respond(w, store.ApproveRuntimeRequest(r.Context(), r.PathValue("id"), r.PathValue("request"), approval))
	}))
	mux.HandleFunc("DELETE /api/v1/admin/nodes/{id}/runtime/requests/{request}", a.admin(func(w http.ResponseWriter, r *http.Request) {
		respond(w, store.CancelRuntimeRequest(r.Context(), r.PathValue("id"), r.PathValue("request")))
	}))
}
