package probe

import (
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	v2 "github.com/komari-monitor/komari/protocol/v2"
)

func TestManagedPingTasksUseIndependentFamiliesAndLocalAddresses(t *testing.T) {
	managed, err := managedPingTasks([]CarrierRouteSelection{
		{Region: "广东", Carrier: "telecom", Family: "ipv4"},
		{Region: "广东", Carrier: "telecom", Family: "ipv6"},
	}, 1, models.StringArray{"node-b", "node-a"})
	if err != nil {
		t.Fatalf("managedPingTasks() error = %v", err)
	}
	if len(managed) != 2 {
		t.Fatalf("managed tasks = %d, want 2", len(managed))
	}
	for _, task := range managed {
		if task.ManagedKey == "" || task.Region != "广东" || task.Carrier != "telecom" {
			t.Fatalf("unexpected managed metadata: %#v", task)
		}
		if task.Type != "tcp" || task.Interval != MinimumPingIntervalSeconds() || !task.DefaultOn {
			t.Fatalf("unexpected managed execution settings: %#v", task)
		}
		if len(task.Clients) != 2 || task.Clients[0] != "node-b" || !strings.Contains(task.Name, strings.ToUpper(task.Family)) {
			t.Fatalf("unexpected managed task: %#v", task)
		}
		if !strings.Contains(task.Target, ".ip.zstaticcdn.com:") {
			t.Fatalf("managed target does not use the embedded hostname: %q", task.Target)
		}
	}
	if managed[0].Family == managed[1].Family || managed[0].Target == managed[1].Target {
		t.Fatalf("families were not kept independent: %#v", managed)
	}
}

func TestCatalogContainsEveryRegionCarrierFamilyCombination(t *testing.T) {
	options := CatalogOptions()
	if len(options) != 196 {
		t.Fatalf("catalog options = %d, want 196", len(options))
	}
	seen := make(map[string]struct{}, len(options))
	regions := make(map[string]struct{})
	for _, option := range options {
		if _, ok := seen[option.ID]; ok {
			t.Fatalf("duplicate catalog option %q", option.ID)
		}
		seen[option.ID] = struct{}{}
		regions[option.Region] = struct{}{}
	}
	if len(regions) != 38 {
		t.Fatalf("catalog regions = %d, want 38", len(regions))
	}
}

func TestCatalogContainsInternationalBGPTargets(t *testing.T) {
	options := CatalogOptions()
	count := 0
	for _, option := range options {
		if option.Category == "international_bgp" {
			count++
			if option.Carrier != "international" || option.DisplayName == "" || !strings.Contains(option.Host, ".") {
				t.Fatalf("invalid international option: %#v", option)
			}
		}
	}
	if count != 10 {
		t.Fatalf("international options = %d, want 10", count)
	}
}

func TestNormalizeMonitorTasksDisablesInternationalRoute(t *testing.T) {
	tasks, err := NormalizeMonitorTasks([]CarrierMonitorTask{{ID: "intl", Name: "Tokyo", Clients: []string{"node"}, Enabled: true, RouteEnabled: true, Region: "日本东京", Carrier: "international", Family: "ipv4", Host: "speedtest.tokyo2.linode.com", PingIntervalSeconds: 60, RouteIntervalSeconds: 900}})
	if err != nil {
		t.Fatalf("NormalizeMonitorTasks() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].RouteEnabled || tasks[0].Category != "international_bgp" {
		t.Fatalf("normalized international task = %#v", tasks)
	}
}

func TestNormalizeProbeEndpointParsesLegacyPort(t *testing.T) {
	for _, test := range []struct {
		name     string
		input    string
		port     int
		wantHost string
		wantPort int
	}{
		{name: "hostname", input: "example.com:443", port: 80, wantHost: "example.com", wantPort: 443},
		{name: "ipv6", input: "[2001:db8::1]:8443", port: 80, wantHost: "2001:db8::1", wantPort: 8443},
		{name: "ipv6 without port", input: "2001:db8::1", port: 443, wantHost: "2001:db8::1", wantPort: 443},
		{name: "explicit structured port wins", input: "example.com:443", port: 8080, wantHost: "example.com", wantPort: 8080},
	} {
		t.Run(test.name, func(t *testing.T) {
			host, port := normalizeProbeEndpoint(test.input, test.port)
			if host != test.wantHost || port != test.wantPort {
				t.Fatalf("normalizeProbeEndpoint(%q, %d) = (%q, %d), want (%q, %d)", test.input, test.port, host, port, test.wantHost, test.wantPort)
			}
		})
	}
}

func TestNormalizeRouteTasksKeepsExplicitClientsAndHostname(t *testing.T) {
	tasks, err := NormalizeRouteTasks([]CarrierRouteTask{{
		ID: "route-1", Name: "湖南电信", Clients: []string{"node-b", "node-a", "node-a"},
		Enabled: true, Region: "湖南", Carrier: "电信", Family: "ipv4",
		Host: "hn-ct-v4.ip.zstaticcdn.com", BackupHost: "bak.hn-ct-v4.ip.zstaticcdn.com", IntervalSeconds: 60,
	}})
	if err != nil {
		t.Fatalf("NormalizeRouteTasks() error = %v", err)
	}
	if len(tasks) != 1 || len(tasks[0].Clients) != 2 || tasks[0].Clients[0] != "node-a" || tasks[0].Host != "hn-ct-v4.ip.zstaticcdn.com" {
		t.Fatalf("normalized route tasks = %#v", tasks)
	}
}

func TestNormalizeSelectionsValidatesAndDeduplicates(t *testing.T) {
	got, err := NormalizeSelections([]any{
		map[string]any{"region": "广东", "carrier": "电信", "families": []any{"ipv4"}},
		map[string]any{"region": "广东", "carrier": "电信", "family": "ipv4"},
		"浙江:unicom:ipv6",
	})
	if err != nil {
		t.Fatalf("NormalizeSelections() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("selections = %#v, want two deduplicated entries", got)
	}
	seen := make(map[string]bool, len(got))
	for _, selection := range got {
		seen[selectionID(selection.Region, selection.Carrier, selection.Family)] = true
	}
	if !seen[selectionID("广东", "telecom", "ipv4")] || !seen[selectionID("浙江", "unicom", "ipv6")] {
		t.Fatalf("normalized selections = %#v", got)
	}

	if _, err := NormalizeSelections("不存在:telecom:ipv4"); err == nil {
		t.Fatal("NormalizeSelections() accepted an unknown region")
	}
}

func TestQuerySortsIPv4ThenCarrierAndFiltersRegion(t *testing.T) {
	store.mu.Lock()
	store.results = make(map[string]map[string]v2.CarrierRouteEntry)
	store.mu.Unlock()
	now := time.Now().UTC()
	Record("node-1", v2.CarrierRouteProbeResult{Family: "ipv6", Results: []v2.CarrierRouteEntry{{Family: "ipv6", Carrier: "mobile", Region: "广东", TargetID: "m", CheckedAt: now}}})
	Record("node-1", v2.CarrierRouteProbeResult{Family: "ipv4", Results: []v2.CarrierRouteEntry{
		{Family: "ipv4", Carrier: "mobile", Region: "广东", TargetID: "cm", CheckedAt: now},
		{Family: "ipv4", Carrier: "telecom", Region: "广东", TargetID: "ct", CheckedAt: now},
		{Family: "ipv4", Carrier: "unicom", Region: "浙江", TargetID: "cu", CheckedAt: now},
	}})
	got := Query("node-1", nil, "广东", 3600)
	if len(got) != 3 {
		t.Fatalf("filtered results = %#v, want three", got)
	}
	if got[0].Family != "ipv4" || got[0].Carrier != "telecom" || got[1].Carrier != "mobile" || got[2].Family != "ipv6" {
		t.Fatalf("sort order = %#v", got)
	}
}
