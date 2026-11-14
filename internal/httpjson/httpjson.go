// Package httpjson is the JSON request / response plumbing shared by the
// HTTP API and the modules that mount their own routes on it.
package httpjson

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/leeska/TZA-ControlCenter/internal/model"
)

// MaxBody bounds a JSON request body.
const MaxBody = 256 << 10

// Write sends v as JSON with the given status.
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("response write failed")
	}
}

// Error sends {"error": message}.
func Error(w http.ResponseWriter, status int, message string) {
	Write(w, status, map[string]string{"error": message})
}

// Decode reads exactly one JSON value with no unknown fields into v. On a bad
// request it has already answered, and returns false.
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		Error(w, 415, "请求须为 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		Error(w, 400, "请求体不是有效的 JSON")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		Error(w, 400, "请求体只能包含一个 JSON 值")
		return false
	}
	return true
}

// Fail answers with the error's kind as the status and its message as the
// body. The bare sentinels get a generic Chinese line; a message built with
// model.Invalid / NotFound / Conflict is shown as is. Anything else is a 500.
func Fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrInvalid):
		Error(w, 400, message(err, model.ErrInvalid, "请求参数无效"))
	case errors.Is(err, model.ErrNotFound):
		Error(w, 404, message(err, model.ErrNotFound, "对象不存在"))
	case errors.Is(err, model.ErrConflict):
		Error(w, 409, message(err, model.ErrConflict, "操作冲突，请刷新后重试"))
	default:
		slog.Error("request failed", "error", err)
		Error(w, 500, "服务器处理失败")
	}
}

// Respond answers 204 on success and Fail otherwise.
func Respond(w http.ResponseWriter, err error) {
	if err != nil {
		Fail(w, err)
		return
	}
	w.WriteHeader(204)
}

func message(err, sentinel error, fallback string) string {
	if err == sentinel {
		return fallback
	}
	return err.Error()
}
