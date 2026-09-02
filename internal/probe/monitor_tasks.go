package probe

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/config"
	"gorm.io/gorm"
)

const (
	carrierMonitorTasksKey  = "carrier_monitor_tasks"
	carrierMonitorManagedBy = "tza-carrier-monitor"
	CarrierMonitorTasksKey  = carrierMonitorTasksKey
)

// CarrierMonitorTask is the unified monitoring contract. Latency is always
// represented by the task; RouteEnabled controls whether the same target is
// also sent through the bounded return-route probe.
type CarrierMonitorTask struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Clients              []string `json:"clients"`
	Enabled              bool     `json:"enabled"`
	RouteEnabled         bool     `json:"route_enabled"`
	Region               string   `json:"region,omitempty"`
	Carrier              string   `json:"carrier"`
	Family               string   `json:"family"`
	Host                 string   `json:"host"`
	BackupHost           string   `json:"backup_host,omitempty"`
	Port                 int      `json:"port,omitempty"`
	PingType             string   `json:"ping_type"`
	PingIntervalSeconds  int      `json:"ping_interval_seconds"`
	RouteIntervalSeconds int      `json:"route_interval_seconds,omitempty"`
	CatalogID            string   `json:"catalog_id,omitempty"`
	Category             string   `json:"category,omitempty"`
}

func CurrentMonitorTasks() []CarrierMonitorTask {
	items, err := config.GetAs[[]CarrierMonitorTask](carrierMonitorTasksKey, []CarrierMonitorTask{})
	if err != nil {
		return []CarrierMonitorTask{}
	}
	normalized, err := NormalizeMonitorTasks(items)
	if err != nil {
		return []CarrierMonitorTask{}
	}
	return normalized
}

func NormalizeMonitorTasks(items []CarrierMonitorTask) ([]CarrierMonitorTask, error) {
	seen := make(map[string]struct{}, len(items))
	out := make([]CarrierMonitorTask, 0, len(items))
	for index, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = fmt.Sprintf("monitor-%d", index+1)
		}
		if _, exists := seen[item.ID]; exists {
			return nil, fmt.Errorf("duplicate carrier monitor task id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		item.Name = strings.TrimSpace(item.Name)
		item.Clients = normalizeMonitorClients(item.Clients)
		item.Region = strings.TrimSpace(item.Region)
		item.Carrier = normalizeCarrier(item.Carrier)
		item.Family = normalizeFamily(item.Family)
		item.Host, item.Port = normalizeProbeEndpoint(item.Host, item.Port)
		item.BackupHost = normalizeProbeHost(item.BackupHost)
		item.CatalogID = strings.TrimSpace(item.CatalogID)
		item.Category = strings.TrimSpace(item.Category)
		if item.Category == "" && item.Carrier == "international" {
			item.Category = "international_bgp"
		}
		if item.Carrier == "international" {
			// International targets are for BGP latency only; route labels are
			// defined by the domestic carrier classifier.
			item.RouteEnabled = false
		}
		if item.Region == "" && item.Carrier == "international" {
			return nil, fmt.Errorf("international carrier monitor task requires region")
		}
		item.PingType = strings.ToLower(strings.TrimSpace(item.PingType))
		if item.PingType == "" {
			item.PingType = "tcp"
		}
		if item.PingType != "icmp" && item.PingType != "tcp" && item.PingType != "http" {
			return nil, fmt.Errorf("carrier monitor task has invalid ping type %q", item.PingType)
		}
		if item.Name == "" || item.Host == "" || item.Family == "" {
			return nil, fmt.Errorf("carrier monitor task requires name, host and family")
		}
		if item.Carrier != "telecom" && item.Carrier != "unicom" && item.Carrier != "mobile" && item.Carrier != "international" {
			return nil, fmt.Errorf("carrier monitor task has invalid carrier %q", item.Carrier)
		}
		if strings.ContainsAny(item.Host, " \t\r\n/") || strings.ContainsAny(item.BackupHost, " \t\r\n/") {
			return nil, fmt.Errorf("carrier monitor host must be a hostname or IP address")
		}
		if item.Port <= 0 || item.Port > 65535 {
			item.Port = 80
		}
		item.PingIntervalSeconds = NormalizePingIntervalSeconds(item.PingIntervalSeconds)
		if item.RouteEnabled {
			item.RouteIntervalSeconds = NormalizeIntervalSeconds(item.RouteIntervalSeconds)
		} else {
			item.RouteIntervalSeconds = 0
		}
		out = append(out, item)
	}
	return out, nil
}

func normalizeMonitorClients(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// SyncManagedMonitorTasks materializes one latency task and, when requested,
// one route task for every unified task. Existing user-created Ping tasks are
// untouched; only the two TZA-owned compatibility namespaces are replaced.
func SyncManagedMonitorTasks(items []CarrierMonitorTask) error {
	normalized, err := NormalizeMonitorTasks(items)
	if err != nil {
		return err
	}
	if err := config.Set(carrierMonitorTasksKey, normalized); err != nil {
		return err
	}

	desiredPing := make([]models.PingTask, 0, len(normalized))
	desiredRoutes := make([]CarrierRouteTask, 0, len(normalized))
	for _, item := range normalized {
		if item.Enabled && len(item.Clients) > 0 {
			desiredPing = append(desiredPing, models.PingTask{
				Name: item.Name, Clients: append(models.StringArray(nil), item.Clients...), DefaultOn: false,
				Type: item.PingType, Target: monitorTarget(item.Host, item.Port), Interval: item.PingIntervalSeconds,
				ManagedKey: "monitor:" + item.ID, Region: item.Region, Carrier: item.Carrier, Family: item.Family, Category: item.Category,
			})
		}
		if item.RouteEnabled {
			desiredRoutes = append(desiredRoutes, CarrierRouteTask{
				ID: "monitor-route-" + item.ID, Name: item.Name, Clients: append([]string(nil), item.Clients...),
				Enabled: item.Enabled, Region: item.Region, Carrier: item.Carrier, Family: item.Family,
				Host: item.Host, BackupHost: item.BackupHost, Port: item.Port,
				IntervalSeconds: item.RouteIntervalSeconds, CatalogID: item.CatalogID,
			})
		}
	}
	if err := tasks.SyncManagedPingTasks(carrierMonitorManagedBy, desiredPing); err != nil {
		return err
	}
	// Remove tasks created by the previous independent latency selector once a
	// unified configuration is saved. This does not touch hand-created tasks.
	if err := tasks.SyncManagedPingTasks(carrierPingManagedBy, nil); err != nil {
		return err
	}
	if err := config.Set(routeTasksKey, desiredRoutes); err != nil {
		return err
	}
	return RegisterSchedule()
}

func monitorTarget(host string, port int) string {
	if port <= 0 {
		port = 80
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + fmt.Sprint(port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// MigrateLegacyMonitorTasks creates the unified store once, preserving old
// independent selections and explicit route assignments. New nodes remain
// unassigned just as they did in the task model.
func MigrateLegacyMonitorTasks() error {
	if _, err := config.GetAs[[]CarrierMonitorTask](carrierMonitorTasksKey); err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// Ensure the older route selector has been materialized before pairing it
	// with latency tasks. This also makes the migration order safe for callers
	// other than the startup scheduler.
	if err := MigrateLegacyRouteTasks(); err != nil {
		return err
	}
	// Older latency selections were only materialized on settings writes. Create
	// their managed tasks now so they can be paired with the migrated routes.
	if len(CurrentPingSelections()) > 0 {
		if err := SyncManagedPingTasks(); err != nil {
			return err
		}
	}
	routes := CurrentRouteTasks()
	pingTasks, err := tasks.GetAllPingTasks()
	if err != nil {
		return err
	}
	items := make([]CarrierMonitorTask, 0, len(routes)+len(pingTasks))
	usedPing := make(map[uint]struct{})
	for index, route := range routes {
		id := strings.TrimPrefix(route.ID, "monitor-route-")
		if id == "" {
			id = fmt.Sprintf("legacy-route-%d", index+1)
		}
		pingIndex := -1
		for i := range pingTasks {
			ping := pingTasks[i]
			if _, used := usedPing[ping.Id]; used || ping.ManagedBy != carrierPingManagedBy {
				continue
			}
			if ping.Region == route.Region && ping.Carrier == route.Carrier && ping.Family == route.Family {
				pingIndex = i
				break
			}
		}
		item := CarrierMonitorTask{
			ID: id, Name: route.Name, Clients: route.Clients, Enabled: true, RouteEnabled: route.Enabled,
			Region: route.Region, Carrier: route.Carrier, Family: route.Family, Host: route.Host,
			BackupHost: route.BackupHost, Port: route.Port, PingType: "tcp",
			PingIntervalSeconds: DefaultPingIntervalSeconds(), RouteIntervalSeconds: route.IntervalSeconds,
			CatalogID: route.CatalogID,
		}
		if pingIndex >= 0 {
			ping := pingTasks[pingIndex]
			usedPing[ping.Id] = struct{}{}
			item.Name, item.Clients, item.Enabled = ping.Name, ping.Clients, len(ping.Clients) > 0
			item.PingType, item.PingIntervalSeconds = ping.Type, ping.Interval
		}
		items = append(items, item)
	}
	for _, ping := range pingTasks {
		if ping.ManagedBy != carrierPingManagedBy {
			continue
		}
		if _, used := usedPing[ping.Id]; used {
			continue
		}
		carrier := normalizeCarrier(ping.Carrier)
		if carrier == "" {
			carrier = "international"
		}
		family := normalizeFamily(ping.Family)
		if family == "" {
			family = "ipv4"
		}
		items = append(items, CarrierMonitorTask{
			ID: fmt.Sprintf("legacy-ping-%d", ping.Id), Name: ping.Name, Clients: ping.Clients,
			Enabled: len(ping.Clients) > 0, Region: ping.Region, Carrier: carrier, Family: family,
			Host: strings.TrimSuffix(strings.TrimSpace(ping.Target), ":80"), Port: 80, PingType: ping.Type,
			PingIntervalSeconds: ping.Interval,
		})
	}
	return SyncManagedMonitorTasks(items)
}
