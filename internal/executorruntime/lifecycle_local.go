package executorruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

type LocalLifecycle struct {
	Peer   RuntimePeer
	Target ipsentinel.Target
}

type peerRequest struct {
	SocketPath string            `json:"socket_path"`
	Target     ipsentinel.Target `json:"target"`
	Method     string            `json:"method"`
	RequestID  string            `json:"request_id"`
	Fence      ipsentinel.Fence  `json:"fence"`
}

func (local LocalLifecycle) Verify(ctx context.Context, plan OperationPlan) error {
	plan.Options.Action = "verify"
	result, err := runReferenceOperation(ctx, referenceRequest{Plan: plan})
	if err != nil {
		return err
	}
	if result.State != "verified" || result.Release != plan.ArchiveSHA256 {
		return errors.New("installed runtime verification did not confirm the selected release")
	}
	return nil
}

func ServeRuntimePeer(ctx context.Context, source io.Reader, destination io.Writer) error {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		return errors.New("runtime peer requires a non-root Linux identity")
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) != 0 {
		return errors.New("runtime peer must not retain supplementary groups")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return errors.New("runtime peer cannot verify dropped capabilities")
	}
	verified := 0
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[0] == "CapEff:" || fields[0] == "CapPrm:" || fields[0] == "CapAmb:") {
			if strings.Trim(fields[1], "0") != "" {
				return errors.New("runtime peer must not retain capabilities")
			}
			verified++
		}
	}
	if verified != 3 {
		return errors.New("runtime peer cannot verify dropped capabilities")
	}
	raw, err := io.ReadAll(io.LimitReader(source, 8193))
	var request peerRequest
	if err != nil || len(raw) > 8192 || ipsentinel.DecodeStrict(raw, &request) != nil || !absoluteClean(request.SocketPath) ||
		!ipsentinel.ValidUUID(request.Target.ControllerID) || !ipsentinel.ValidUUID(request.Target.NodeID) {
		return errors.New("invalid runtime peer request")
	}
	client := ipsentinel.NewClient(request.SocketPath, request.Target)
	defer client.Close()
	var result any
	switch request.Method {
	case ipsentinel.MethodHello:
		result, err = client.Hello(ctx, "")
	case ipsentinel.MethodControlPause:
		result, err = client.ControlPause(ctx, request.RequestID, request.Fence)
	default:
		return errors.New("runtime peer supports only hello and pause")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(destination).Encode(result)
}

func (local LocalLifecycle) call(ctx context.Context, method, id string, fence ipsentinel.Fence, output any) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || local.Peer.UID == 0 || local.Peer.GID == 0 {
		return errors.New("runtime service control requires a dedicated root installer with a non-root peer")
	}
	executable := "/usr/local/libexec/tza/tza-runtime-installer"
	if err := trustedFile(executable); err != nil {
		return err
	}
	raw, err := json.Marshal(peerRequest{local.Peer.SocketPath, local.Target, method, id, fence})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--runtime-peer")
	command.Stdin = bytes.NewReader(raw)
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: local.Peer.UID, Gid: local.Peer.GID, Groups: []uint32{}}}
	var result boundedOutput
	command.Stdout, command.Stderr = &result, io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("non-root runtime peer could not verify or pause the executor: %w", err)
	}
	return ipsentinel.DecodeStrict(result.Bytes(), output)
}

func (local LocalLifecycle) Hello(ctx context.Context) (ipsentinel.Hello, error) {
	var hello ipsentinel.Hello
	err := local.call(ctx, ipsentinel.MethodHello, "", ipsentinel.Fence{}, &hello)
	return hello, err
}

func (local LocalLifecycle) Pause(ctx context.Context, id string, fence ipsentinel.Fence) error {
	var control ipsentinel.Control
	if err := local.call(ctx, ipsentinel.MethodControlPause, id, fence, &control); err != nil {
		return err
	}
	if !control.Paused || control.Epoch != fence.Epoch {
		return errors.New("executor did not confirm its fenced pause")
	}
	return nil
}

func (local LocalLifecycle) Service(ctx context.Context, action, unit string) (string, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || !executorUnit.MatchString(unit) {
		return "", errors.New("fixed local IPSentinel service required")
	}
	binary := "/usr/bin/systemctl"
	if err := trustedFile(binary); err != nil {
		return "", err
	}
	var arguments []string
	switch action {
	case "definition":
		arguments = []string{"cat", "--no-pager", "--", unit}
	case "state":
		arguments = []string{"show", "--property=ActiveState", "--value", "--", unit}
	case "start", "stop":
		arguments = []string{action, "--", unit}
	default:
		return "", errors.New("unsupported runtime service action")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	command.Dir = "/"
	var output boundedOutput
	command.Stdout, command.Stderr = &output, io.Discard
	if err := command.Run(); err != nil {
		return "", errors.New("fixed runtime service command failed")
	}
	return strings.TrimSpace(string(output.Bytes())), nil
}
