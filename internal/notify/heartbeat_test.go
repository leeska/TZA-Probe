package notify

import (
	"github.com/leeska/TZA-Probe/internal/model"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticHeartbeatDoesNotPretendResourceRecovery(t *testing.T) {
	n := model.NodeView{Online: true, MetricsStale: true, Metrics: &model.Metrics{CPU: 99, MemoryUsed: 99, MemoryTotal: 100}, AgentStatus: &model.AgentStatus{State: "collect_failed", Reason: "disk"}}
	for _, kind := range []string{model.AlertCPU, model.AlertMemory} {
		r := model.AlertRule{Kind: kind, Threshold: 80}
		active, _ := evaluate(n, r, time.Now())
		if active {
			t.Fatal("stale usage triggered", kind)
		}
		text := recoveryMessage(n, r, true, time.Now())
		if !strings.Contains(text, "不能确认使用率已恢复正常") || !strings.Contains(text, "停止评估") {
			t.Fatalf("misleading recovery: %s", text)
		}
	}
	active, _ := evaluate(n, model.AlertRule{Kind: model.AlertOffline}, time.Now())
	if active {
		t.Fatal("collector failure became host offline")
	}
}
