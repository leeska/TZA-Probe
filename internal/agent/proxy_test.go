package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/channel"
)

// 代理内核：校验后安装、配置先过内核检查再写入并重启服务、状态读回、移除只删配置与服务。
func TestProxyHost(t *testing.T) {
	root := t.TempDir()
	var calls []string
	rejectConfig := false
	h := &ProxyHost{Root: root, Systemd: func() bool { return true }, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if strings.HasSuffix(name, "/xray") && rejectConfig {
			return []byte("bad port"), errors.New("exit 1")
		}
		if name == "systemctl" && args[0] == "is-active" {
			return []byte("active\n"), nil
		}
		if name == "id" {
			return []byte(fmt.Sprint(os.Getgid())), nil
		}
		return nil, nil
	}}
	handlers := h.Handlers()
	call := func(kind string, params any, body []byte, out any) {
		t.Helper()
		server, client := net.Pipe()
		raw, _ := json.Marshal(params)
		done := make(chan error, 1)
		go func() { done <- handlers[kind](context.Background(), raw, server); server.Close() }()
		if body != nil {
			go client.Write(body)
		}
		reflect.ValueOf(out).Elem().Set(reflect.Zero(reflect.TypeOf(out).Elem()))
		if err := channel.ReadLine(client, out); err != nil {
			t.Fatal(kind, err)
		}
		client.Close()
		<-done
	}
	binary := []byte("#!/bin/sh\necho fake xray\n")
	sum := sha256.Sum256(binary)
	var r channel.ProxyResult
	call(channel.KindProxyInstall, channel.ProxyInstallParams{Kernel: "xray", Version: "1.0", SHA256: strings.Repeat("0", 64), Size: int64(len(binary))}, binary, &r)
	if !strings.Contains(r.Error, "校验失败") {
		t.Fatalf("校验不符要拒绝：%+v", r)
	}
	if _, err := os.Stat(h.binaryPath("xray", "1.0")); err == nil {
		t.Fatal("校验失败不能留下文件")
	}
	call(channel.KindProxyInstall, channel.ProxyInstallParams{Kernel: "xray", Version: "1.0", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(binary))}, binary, &r)
	if r.Error != "" {
		t.Fatal(r)
	}
	if info, err := os.Stat(h.binaryPath("xray", "1.0")); err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("内核要可执行", err)
	}
	config := []byte(`{"inbounds":[]}`)
	csum := sha256.Sum256(config)
	apply := channel.ProxyApplyParams{Kernel: "xray", ConfigSHA256: hex.EncodeToString(csum[:]), Size: int64(len(config))}
	rejectConfig = true
	call(channel.KindProxyApply, apply, config, &r)
	if r.Error == "" || r.Output != "bad port" {
		t.Fatalf("内核拒绝的配置不能生效：%+v", r)
	}
	if _, err := os.Stat(h.configPath("xray")); err == nil {
		t.Fatal("被拒绝的配置不能写入")
	}
	rejectConfig = false
	calls = nil
	call(channel.KindProxyApply, apply, config, &r)
	if r.Error != "" {
		t.Fatal(r)
	}
	if info, err := os.Stat(h.configPath("xray")); err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("配置只给 root 和 tza-proxy 组读", err)
	}
	unitText, _ := os.ReadFile(h.unitPath("xray"))
	for _, want := range []string{"User=tza-proxy", h.binaryPath("xray", "1.0") + " run -config " + h.configPath("xray"), "CAP_NET_BIND_SERVICE", "ProtectSystem=strict"} {
		if !strings.Contains(string(unitText), want) {
			t.Fatalf("服务单元缺 %q\n%s", want, unitText)
		}
	}
	if !strings.Contains(strings.Join(calls, "\n"), "systemctl restart tza-proxy-xray") {
		t.Fatal(calls)
	}
	var status channel.ProxyStatus
	call(channel.KindProxyStatus, nil, nil, &status)
	if s := status.Kernels["xray"]; s.Version != "1.0" || s.ConfigSHA256 != apply.ConfigSHA256 || !s.Active || !status.Systemd {
		t.Fatalf("%+v", status)
	}
	call(channel.KindProxyRemove, channel.ProxyRemoveParams{Kernel: "xray"}, nil, &r)
	if _, err := os.Stat(h.configPath("xray")); err == nil || r.Error != "" {
		t.Fatal("移除要删配置", r)
	}
	if _, err := os.Stat(h.binaryPath("xray", "1.0")); err != nil {
		t.Fatal("移除保留内核")
	}
	call(channel.KindProxyStatus, nil, nil, &status)
	if s := status.Kernels["xray"]; s.ConfigSHA256 != "" || s.Version != "1.0" {
		t.Fatalf("%+v", s)
	}
	call(channel.KindProxyInstall, channel.ProxyInstallParams{Kernel: "../etc", Version: "1", SHA256: strings.Repeat("0", 64), Size: 1}, nil, &r)
	if r.Error == "" {
		t.Fatal("内核名不能带路径")
	}
}

// The agent keeps /etc/tza-probe at 0700, so the unprivileged service user
// must find its config somewhere else (v2.5.0 got this wrong).
func TestProxyConfigOutsideAgentDir(t *testing.T) {
	h := &ProxyHost{}
	if strings.HasPrefix(h.configPath("xray"), "/etc/tza-probe/") {
		t.Fatalf("config %s is unreadable for %s", h.configPath("xray"), ProxyUser)
	}
}
