package probe

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestRawTraceMasksAndUsesASN(t *testing.T) {
	data := []byte("1|192.0.2.1||1.2|64512\n2|*|||\n2|219.158.1.1||200.3|4837\n3|198.51.100.2||201.2|4134\n")
	hops, reached, latency := parse(data, "198.51.100.2")
	if reached != 1 || latency == nil || *latency != 201.2 || len(hops) != 3 {
		t.Fatal(hops, reached, latency)
	}
	// AS4134 on the target hop names 电信's access, not a second backbone: the
	// 4837 hops in front of it are the line this trace rode. See
	// model.RouteLabel — 163 is only named when it comes first.
	if hops[1].Address != "219.158.*.*" || model.RouteLabel(hops) != "4837" {
		t.Fatal(hops)
	}
	_, reached, _ = parse(data, "198.51.100.3")
	if reached != 0 {
		t.Fatal("partial trace marked reached")
	}
}
func TestOutputMemoryBound(t *testing.T) {
	b := limitedBuffer{remaining: 10}
	n, err := b.Write([]byte(strings.Repeat("x", 1000)))
	if err != nil || n != 1000 || len(b.data) != 10 {
		t.Fatal("unbounded output")
	}
}
func TestTCPSuccessAndMissingRouteTool(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	task := model.Probe{Name: "loopback", Kind: "tcp", Host: "127.0.0.1", Port: p, Family: "ipv4", Interval: 30}
	result := (Runner{}).Run(context.Background(), model.Job{ID: "j", Task: task})
	if result.Status != "ok" || result.Received != 3 || result.Latency == nil {
		t.Fatal(result)
	}
	task.Kind = "route"
	task.Region = "上海"
	task.Carrier = "电信"
	task.Interval = 900
	result = (Runner{}).Run(context.Background(), model.Job{Task: task})
	if result.Status != "unavailable" || result.Route != "未知" {
		t.Fatal(result)
	}
}

func TestFakeIPCannotBecomeSuccessfulMeasurement(t *testing.T) {
	for _, host := range []string{"198.18.0.1", "198.19.255.254"} {
		for _, kind := range []string{"route", "tcp"} {
			task := model.Probe{Name: "fake DNS", Kind: kind, Host: host, Port: 80, Family: "ipv4", Interval: 900, Region: "上海", Carrier: "电信"}
			r := (Runner{Nexttrace: "/does-not-exist"}).Run(context.Background(), model.Job{Task: task})
			if r.Status != "failed" || r.Reason != "synthetic_dns" || r.Received != 0 {
				t.Fatalf("proxy address became measurement: %+v", r)
			}
		}
	}
	if syntheticAddress(net.ParseIP("198.20.0.1")) || syntheticAddress(net.ParseIP("127.0.0.1")) {
		t.Fatal("overbroad synthetic range")
	}
}
