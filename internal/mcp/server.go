// Package mcp exposes the probe over the Model Context Protocol so an AI client
// can do what the administrator panel does without driving the browser: read
// nodes, latency history and route labels, and manage nodes, tasks and
// notification settings with the panel's own validation.
//
// The endpoint implements both protocol eras on one URL: the stateless
// 2026-07-28 revision, where every request declares its version in _meta and
// mirrors it into headers, and the older initialize handshake (2025-06-18 /
// 2025-11-25). The specification calls that a dual-era server. There is no
// session state: every request is answered on its own.
//
// This package knows nothing about HTTP routes or authentication; cmd/server
// injects the persistence port and httpapi mounts the handler behind the
// administrator key.
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/service"
)

const (
	// Modern is the per-request-metadata revision: no handshake, no session.
	Modern = "2026-07-28"
	// Legacy revisions keep the initialize handshake; both are answered as-is.
	legacyLatest = "2025-11-25"
	legacyEarly  = "2025-06-18"
	// maxBody bounds one JSON-RPC message, as the HTTP contract does.
	maxBody = 256 << 10

	versionKey    = "io.modelcontextprotocol/protocolVersion"
	serverInfoKey = "io.modelcontextprotocol/serverInfo"
	// encodedPrefix marks a header value that could not be sent as plain ASCII.
	encodedPrefix = "=?base64?"
)

// versions is what this server implements, in the order it advertises them.
var versions = []string{Modern, legacyLatest, legacyEarly}

// DefaultInstructions tells a client's model what this endpoint is for when the
// composition root does not override it.
const DefaultInstructions = "TZA Probe 探针面板的管理通道：节点状态、主机历史、TCP 延迟与回程线路标签。回程标签按实际经过的骨干命名（163 / CN2GIA / CN2GT / CTG / 4837 / 9929 / 10099 / CMIN2 / CMI），跳地址已脱敏，标签未知时不猜。本通道持有管理密钥，除只读查询外还能新增、修改、删除节点与任务，轮换节点密钥，修改通知设置；这些写操作立即生效，删除不可撤销，执行前请向用户确认。"

// JSON-RPC codes plus the two the 2026-07-28 revision allocates inside the
// implementation-defined range for header validation and version negotiation.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeHeaderMismatch = -32020
	codeVersionUnknown = -32022
)

// rpcError is a JSON-RPC error object; data is only sent when it carries the
// information a client needs (the supported versions, for example).
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Source is the persistence port: the same one the management service uses,
// so every tool is either a read or the same write the panel performs.
type Source = service.Store

// Server answers MCP requests. Name and Version identify it in serverInfo;
// Instructions is the guidance clients show to their model. Notifier sends the
// explicit notification test; a nil Notifier only disables that one tool.
type Server struct {
	Source       Source
	Notifier     notify.Sender
	Name         string
	Version      string
	Instructions string
}

// service is the management surface the tools call; it is cheap to build, and
// building it per call keeps Server a plain struct literal.
func (s *Server) service() *service.Service {
	return &service.Service{Store: s.Source, Notifier: s.Notifier}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// call is the part of params every method needs: the tool name, its arguments
// and the per-request metadata the modern revision carries.
type call struct {
	Name      string
	Arguments json.RawMessage
	Meta      map[string]json.RawMessage
	// Version is the handshake-era declaration: initialize carries its protocol
	// version as a plain param, while the modern revision puts it in _meta.
	Version string
}

func parseCall(raw json.RawMessage) call {
	var v struct {
		Name          string                     `json:"name"`
		Arguments     json.RawMessage            `json:"arguments"`
		Meta          map[string]json.RawMessage `json:"_meta"`
		LegacyVersion string                     `json:"protocolVersion"`
	}
	if len(raw) > 0 {
		// A params value that is not an object leaves every field empty; method
		// handlers report the missing field as invalid params.
		_ = json.Unmarshal(raw, &v)
	}
	return call{Name: v.Name, Arguments: v.Arguments, Meta: v.Meta, Version: v.LegacyVersion}
}

// protocolVersion reads the version the request declares, wherever the revision
// it speaks puts it.
func (c call) protocolVersion() string {
	var version string
	if raw, ok := c.Meta[versionKey]; ok {
		_ = json.Unmarshal(raw, &version)
	}
	if version == "" {
		version = c.Version
	}
	return version
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// The modern revision dropped the standalone SSE stream (GET) and
		// session termination (DELETE): both answer 405 here.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "MCP endpoint accepts POST only", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil || req.Method == "" {
		s.writeError(w, r, http.StatusBadRequest, nil, &rpcError{Code: codeParseError, Message: "invalid JSON-RPC message"})
		return
	}
	params := parseCall(req.Params)
	mode, rpcErr, status := s.negotiate(r, req, params)
	if rpcErr != nil {
		s.writeError(w, r, status, req.ID, rpcErr)
		return
	}
	if notification(req) {
		// Notifications ask for nothing back; the transport acknowledges them.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	ctx := context.WithValue(r.Context(), baseURLKey{}, BaseURL(r))
	result, rpcErr, status := s.dispatch(ctx, mode, req.Method, params)
	if rpcErr != nil {
		s.writeError(w, r, status, req.ID, rpcErr)
		return
	}
	s.writeResult(w, r, mode, req.ID, result)
}

// negotiate picks the protocol era for this request. It returns the version the
// request is served in (Modern or one of the handshake revisions), or the error
// the specification requires for a malformed version/header combination.
func (s *Server) negotiate(r *http.Request, req request, params call) (string, *rpcError, int) {
	header := r.Header.Get("MCP-Protocol-Version")
	body := params.protocolVersion()
	if req.Method == "initialize" {
		// The handshake era never sends a version header; answer in the version
		// the client asked for so its later requests keep that revision.
		if body != "" && !supported(body) {
			return "", unknownVersion(body), http.StatusBadRequest
		}
		if body == "" {
			body = legacyLatest
		}
		return body, nil, 0
	}
	// An unsupported version is rejected before anything else, even when the
	// body claims a version this server does speak: the answer tells the client
	// which versions it should retry with.
	if header != "" && !supported(header) {
		return "", unknownVersion(header), http.StatusBadRequest
	}
	if header == Modern || body == Modern {
		// A modern request declares its version in both places and mirrors the
		// method plus the tool name into headers so intermediaries can route it
		// without parsing the body. Every mismatch is rejected.
		if header == "" {
			return "", mismatch("MCP-Protocol-Version header is missing"), http.StatusBadRequest
		}
		if header != Modern || body != Modern {
			return "", mismatch("MCP-Protocol-Version header value '" + header + "' does not match body value '" + body + "'"), http.StatusBadRequest
		}
		if got := r.Header.Get("Mcp-Method"); got != req.Method {
			return "", mismatch("Mcp-Method header value '" + got + "' does not match body value '" + req.Method + "'"), http.StatusBadRequest
		}
		if req.Method == "tools/call" {
			got := r.Header.Get("Mcp-Name")
			if got == "" {
				return "", mismatch("Mcp-Name header is missing"), http.StatusBadRequest
			}
			if decodeHeader(got) != params.Name {
				return "", mismatch("Mcp-Name header value '" + got + "' does not match body value '" + params.Name + "'"), http.StatusBadRequest
			}
		}
		return Modern, nil, 0
	}
	if header == "" {
		// Revisions before 2025-06-18 never sent the header. Treat such a bare
		// request as the newest handshake revision instead of guessing modern.
		header = legacyLatest
	}
	return header, nil, 0
}

// dispatch routes one JSON-RPC method. status is the HTTP status for the legacy
// fallback: the modern revision rejects an unknown method with 404 so a client
// can tell "modern server, no such method" from a legacy server's 200.
func (s *Server) dispatch(ctx context.Context, mode, method string, params call) (map[string]any, *rpcError, int) {
	modern := mode == Modern
	unknown := func() (map[string]any, *rpcError, int) {
		status := http.StatusOK
		if modern {
			status = http.StatusNotFound
		}
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}, status
	}
	switch method {
	case "server/discover":
		if !modern {
			// Only the modern revision defines discovery; a handshake client
			// gets the same answer as a plain tools/list caller.
			return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}, http.StatusOK
		}
		return s.discover(), nil, 0
	case "initialize":
		return s.initialize(mode, params), nil, 0
	case "ping":
		return map[string]any{}, nil, 0
	case "tools/list":
		return s.toolList(modern), nil, 0
	case "tools/call":
		return s.callTool(ctx, params)
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil, 0
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil, 0
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil, 0
	case "logging/setLevel":
		return map[string]any{}, nil, 0
	default:
		return unknown()
	}
}

func (s *Server) initialize(mode string, params call) map[string]any {
	version := params.protocolVersion()
	if !supported(version) {
		version = mode
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      s.info(),
		"instructions":    s.instructions(),
	}
}

func (s *Server) discover() map[string]any {
	return map[string]any{
		"supportedVersions": append([]string{}, versions...),
		"capabilities":      map[string]any{"tools": map[string]any{"listChanged": false}},
		"instructions":      s.instructions(),
		"ttlMs":             3600000,
		"cacheScope":        "public",
	}
}

func (s *Server) instructions() string {
	if s.Instructions != "" {
		return s.Instructions
	}
	return DefaultInstructions
}

func (s *Server) info() map[string]any {
	name, version := s.Name, s.Version
	if name == "" {
		name = "tza-probe"
	}
	if version == "" {
		version = model.Version
	}
	return map[string]any{"name": name, "version": version}
}

// writeResult adds what the modern revision requires of every result: the
// resultType discriminator and the server's identity in _meta. Tool arguments
// and payloads stay identical across eras.
func (s *Server) writeResult(w http.ResponseWriter, r *http.Request, mode string, id json.RawMessage, result map[string]any) {
	if mode == Modern {
		result["resultType"] = "complete"
		result["_meta"] = map[string]any{serverInfoKey: s.info()}
	}
	s.send(w, r, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": idValue(id), "result": result})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, id json.RawMessage, e *rpcError) {
	s.send(w, r, status, map[string]any{"jsonrpc": "2.0", "id": idValue(id), "error": e})
}

// send writes the JSON-RPC message. A client that accepts only text/event-stream
// gets the same message as one SSE event, which is what the transport allows.
func (s *Server) send(w http.ResponseWriter, r *http.Request, status int, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("mcp response encode failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if wantsSSE(r) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("event: message\ndata: " + string(data) + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func wantsSSE(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return accept != "" && !strings.Contains(accept, "application/json") && strings.Contains(accept, "text/event-stream")
}

// idValue keeps the client's identifier byte-for-byte; a notification has none,
// so the key is dropped instead of being sent as null.
func idValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func notification(req request) bool {
	id := strings.TrimSpace(string(req.ID))
	return id == "" || id == "null"
}

func supported(version string) bool {
	for _, v := range versions {
		if v == version {
			return true
		}
	}
	return false
}

func mismatch(message string) *rpcError {
	return &rpcError{Code: codeHeaderMismatch, Message: "Header mismatch: " + message}
}

func unknownVersion(requested string) *rpcError {
	return &rpcError{Code: codeVersionUnknown, Message: "Unsupported protocol version", Data: map[string]any{"supported": versions, "requested": requested}}
}

// decodeHeader undoes the Base64 sentinel a client must use when a name cannot
// travel as plain ASCII.
func decodeHeader(value string) string {
	if strings.HasPrefix(value, encodedPrefix) && strings.HasSuffix(value, "?=") {
		if raw, err := base64.StdEncoding.DecodeString(value[len(encodedPrefix) : len(value)-2]); err == nil {
			return string(raw)
		}
	}
	return value
}
