package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/channel"
)

// 真实 systemd 下的验收（只在带 systemd 的 Linux 容器里跑）：
//
//	TZA_PROXY_SYSTEMD=1 TZA_KERNEL=xray TZA_KERNEL_BIN=/w/xray TZA_KERNEL_CONFIG=/w/xray.json TZA_KERNEL_PORT=443 go test -run TestProxyHostSystemd
//
// 装内核、应用配置、服务以 tza-proxy 用户跑起来并监听端口，再移除。
func TestProxyHostSystemd(t *testing.T) {
	if os.Getenv("TZA_PROXY_SYSTEMD") != "1" {
		t.Skip("needs a systemd host")
	}
	kernel := os.Getenv("TZA_KERNEL")
	binary, _ := os.ReadFile(os.Getenv("TZA_KERNEL_BIN"))
	config, _ := os.ReadFile(os.Getenv("TZA_KERNEL_CONFIG"))
	h := NewProxyHost()
	handlers := h.Handlers()
	call := func(kind string, params any, body []byte) channel.ProxyResult {
		server, client := net.Pipe()
		raw, _ := json.Marshal(params)
		go func() { handlers[kind](context.Background(), raw, server); server.Close() }()
		if body != nil {
			go client.Write(body)
		}
		var r channel.ProxyResult
		if err := channel.ReadLine(client, &r); err != nil {
			t.Fatal(kind, err)
		}
		client.Close()
		return r
	}
	sum := sha256.Sum256(binary)
	if r := call(channel.KindProxyInstall, channel.ProxyInstallParams{Kernel: kernel, Version: "test", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(binary))}, binary); r.Error != "" {
		t.Fatal(r)
	}
	csum := sha256.Sum256(config)
	if r := call(channel.KindProxyApply, channel.ProxyApplyParams{Kernel: kernel, ConfigSHA256: hex.EncodeToString(csum[:]), Size: int64(len(config))}, config); r.Error != "" {
		t.Fatalf("%+v", r)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+os.Getenv("TZA_KERNEL_PORT"), time.Second)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			out, _ := h.Run(context.Background(), "journalctl", "-u", "tza-proxy-"+kernel, "--no-pager")
			t.Fatalf("端口没有在监听：%v\n%s", err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	out, _ := h.Run(context.Background(), "systemctl", "show", "-p", "User", "tza-proxy-"+kernel)
	if !strings.Contains(string(out), "User=tza-proxy") {
		t.Fatalf("%s", out)
	}
	if r := call(channel.KindProxyRemove, channel.ProxyRemoveParams{Kernel: kernel}, nil); r.Error != "" {
		t.Fatal(r)
	}
	if out, _ := h.Run(context.Background(), "systemctl", "is-active", "tza-proxy-"+kernel); strings.TrimSpace(string(out)) == "active" {
		t.Fatal("移除后服务还在跑")
	}
}
