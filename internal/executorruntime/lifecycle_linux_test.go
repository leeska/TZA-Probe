package executorruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestRuntimeLifecycleLinuxNonRootPeer(t *testing.T) {
	if os.Getenv("TZA_RUNTIME_LIFECYCLE_QA") != "1" {
		t.Skip("explicit disposable Linux lifecycle acceptance is disabled")
	}
	if os.Geteuid() != 0 {
		t.Fatal("disposable Linux peer acceptance requires root")
	}
	diagnostic := exec.Command("/usr/bin/id")
	diagnostic.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}}}
	if raw, err := diagnostic.CombinedOutput(); err != nil {
		t.Fatalf("isolated credential switch failed: %v: %s", err, raw)
	} else {
		t.Log(strings.TrimSpace(string(raw)))
	}
	directory := t.TempDir()
	for _, name := range []string{directory, filepath.Dir(directory)} {
		if err := os.Chmod(name, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(directory, "peer.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0o666); err != nil {
		t.Fatal(err)
	}
	var observed atomic.Int64
	server := &http.Server{
		ConnContext: func(ctx context.Context, connection net.Conn) context.Context {
			raw, err := connection.(*net.UnixConn).SyscallConn()
			if err == nil {
				_ = raw.Control(func(descriptor uintptr) {
					credential, err := syscall.GetsockoptUcred(int(descriptor), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
					if err == nil {
						observed.Store(int64(credential.Uid))
					}
				})
			}
			return ctx
		},
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if observed.Load() != 65534 {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			var envelope map[string]any
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			raw, err := os.ReadFile("../ipsentinel/testdata/contracts/ipsentinel-executor-v1/examples/hello.response.json")
			if err != nil {
				t.Error(err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			var response map[string]any
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Error(err)
				return
			}
			response["request_id"] = envelope["request_id"]
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(response)
		}),
	}
	go server.Serve(listener)
	defer server.Close()
	local := LocalLifecycle{Peer: RuntimePeer{SocketPath: socket, UID: 65534, GID: 65534}}
	local.Target.ControllerID = "22222222-2222-4222-8222-222222222222"
	local.Target.NodeID = "33333333-3333-4333-8333-333333333333"
	hello, err := local.Hello(t.Context())
	if err != nil || hello.ExecutorID != "ipsentinel" || observed.Load() != 65534 {
		t.Fatal("local control did not use the configured non-root OS peer", hello.ExecutorID, observed.Load(), err)
	}
	local.Peer.UID = 0
	if _, err := local.Hello(t.Context()); err == nil {
		t.Fatal("root was accepted as an executor peer")
	}
	t.Log("SO_PEERCRED confirms UID 65534; root peer rejected")
}

func TestRuntimeLifecycleLinuxServiceControl(t *testing.T) {
	unit := os.Getenv("TZA_RUNTIME_QA_UNIT")
	if unit == "" {
		t.Skip("explicit disposable systemd service acceptance is disabled")
	}
	if !strings.HasPrefix(unit, "ipsentinel-tza-lifecycle-qa-") {
		t.Fatal("acceptance requires a dedicated temporary service")
	}
	local := LocalLifecycle{}
	definition, err := local.Service(t.Context(), "definition", unit)
	if err != nil || !strings.Contains(definition, "TZA lifecycle QA") {
		t.Fatal("service definition was not the expected test unit", err)
	}
	defer local.Service(context.Background(), "stop", unit)
	for _, step := range []struct{ action, state string }{{"start", "active"}, {"stop", "inactive"}} {
		if _, err := local.Service(t.Context(), step.action, unit); err != nil {
			t.Fatal(step.action, err)
		}
		state, err := local.Service(t.Context(), "state", unit)
		if err != nil || state != step.state {
			t.Fatal(fmt.Sprintf("service %s: got %s", step.action, state), err)
		}
	}
	if _, err := local.Service(t.Context(), "restart", unit); err == nil {
		t.Fatal("unapproved service restart was accepted")
	}
}
