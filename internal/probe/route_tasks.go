package probe

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/internal/config"
	"gorm.io/gorm"
)

const (
	routeTasksKey         = "carrier_route_tasks"
	CarrierRouteTasksKey  = routeTasksKey
	routeTasksMigratedKey = "carrier_route_tasks_migrated"
)

// CarrierRouteTask is an independently scheduled return-route check. Targets
// are assigned to explicit clients; an empty client list never means all.
type CarrierRouteTask struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Clients         []string `json:"clients"`
	Enabled         bool     `json:"enabled"`
	Region          string   `json:"region,omitempty"`
	Carrier         string   `json:"carrier"`
	Family          string   `json:"family"`
	Host            string   `json:"host"`
	BackupHost      string   `json:"backup_host,omitempty"`
	Port            int      `json:"port,omitempty"`
	IntervalSeconds int      `json:"interval_seconds"`
	CatalogID       string   `json:"catalog_id,omitempty"`
}

// MigrateLegacyRouteTasks performs a single, loss-averse migration from the
// pre-task carrier_route_selections settings. Legacy selections applied to all
// nodes, so the migration snapshots the currently known node UUIDs into each
// new task. An empty client list remains intentionally disabled after this
// point; newly added nodes must be assigned explicitly by an administrator.
func MigrateLegacyRouteTasks() error {
	migrated, err := config.GetAs[bool](routeTasksMigratedKey, false)
	if err != nil {
		return fmt.Errorf("read carrier route migration marker: %w", err)
	}
	if migrated {
		return nil
	}
	// A present task key, including an intentionally empty list, is authoritative
	// and must never be overwritten by compatibility data.
	if _, err := config.GetAs[[]CarrierRouteTask](routeTasksKey); err == nil {
		return config.Set(routeTasksMigratedKey, true)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("read carrier route tasks: %w", err)
	}

	raw, err := config.GetAs[any](selectionsKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return config.Set(routeTasksMigratedKey, true)
	}
	if err != nil {
		return fmt.Errorf("read legacy carrier route selections: %w", err)
	}
	selections, err := NormalizeSelections(raw)
	if err != nil {
		// Invalid legacy data should not prevent startup. Mark it migrated and
		// leave the new model empty so an administrator can configure it again.
		if setErr := config.Set(routeTasksKey, []CarrierRouteTask{}); setErr != nil {
			return setErr
		}
		return config.Set(routeTasksMigratedKey, true)
	}

	enabled, _ := config.GetAs[bool](enabledKey, false)
	interval, _ := config.GetAs[int](intervalKey, DefaultIntervalSeconds())
	interval = NormalizeIntervalSeconds(interval)
	nodes, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return fmt.Errorf("load clients for carrier route migration: %w", err)
	}
	clientIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if uuid := strings.TrimSpace(node.UUID); uuid != "" {
			clientIDs = append(clientIDs, uuid)
		}
	}
	sort.Strings(clientIDs)
	options := make(map[string]CatalogOption, len(CatalogOptions()))
	for _, option := range CatalogOptions() {
		options[option.ID] = option
	}
	tasks := make([]CarrierRouteTask, 0, len(selections))
	for index, selection := range selections {
		option, ok := options[selectionID(selection.Region, selection.Carrier, selection.Family)]
		if !ok {
			continue
		}
		tasks = append(tasks, CarrierRouteTask{
			ID:      fmt.Sprintf("route-legacy-%d", index+1),
			Name:    fmt.Sprintf("%s %s %s", option.Region, carrierDisplayName(option.Carrier), strings.ToUpper(option.Family)),
			Clients: append([]string(nil), clientIDs...), Enabled: enabled,
			Region: option.Region, Carrier: option.Carrier, Family: option.Family,
			Host: option.Host, BackupHost: option.BackupHost, Port: option.Port,
			IntervalSeconds: interval, CatalogID: option.ID,
		})
	}
	if err := config.Set(routeTasksKey, tasks); err != nil {
		return fmt.Errorf("save migrated carrier route tasks: %w", err)
	}
	return config.Set(routeTasksMigratedKey, true)
}

func CurrentRouteTasks() []CarrierRouteTask {
	tasks, err := config.GetAs[[]CarrierRouteTask](routeTasksKey, []CarrierRouteTask{})
	if err != nil {
		return []CarrierRouteTask{}
	}
	normalized, err := NormalizeRouteTasks(tasks)
	if err != nil {
		return []CarrierRouteTask{}
	}
	return normalized
}

func NormalizeRouteTasks(items []CarrierRouteTask) ([]CarrierRouteTask, error) {
	seen := make(map[string]struct{}, len(items))
	out := make([]CarrierRouteTask, 0, len(items))
	for index, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = fmt.Sprintf("route-%d", index+1)
		}
		if _, exists := seen[item.ID]; exists {
			return nil, fmt.Errorf("duplicate carrier route task id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		item.Name = strings.TrimSpace(item.Name)
		item.Region = strings.TrimSpace(item.Region)
		item.Carrier = normalizeCarrier(item.Carrier)
		item.Family = normalizeFamily(item.Family)
		item.Host = normalizeProbeHost(item.Host)
		item.BackupHost = normalizeProbeHost(item.BackupHost)
		item.CatalogID = strings.TrimSpace(item.CatalogID)
		if item.Name == "" || item.Host == "" || item.Family == "" {
			return nil, fmt.Errorf("carrier route task requires name, host and family")
		}
		if item.Carrier != "telecom" && item.Carrier != "unicom" && item.Carrier != "mobile" {
			return nil, fmt.Errorf("carrier route task has invalid carrier %q", item.Carrier)
		}
		if strings.ContainsAny(item.Host, " \t\r\n/") || strings.ContainsAny(item.BackupHost, " \t\r\n/") {
			return nil, fmt.Errorf("carrier route host must be a hostname or IP address")
		}
		if item.Port <= 0 || item.Port > 65535 {
			item.Port = 80
		}
		item.IntervalSeconds = NormalizeIntervalSeconds(item.IntervalSeconds)
		item.Clients = normalizeTaskClients(item.Clients)
		out = append(out, item)
	}
	return out, nil
}

func normalizeProbeHost(value string) string {
	return strings.Trim(strings.TrimSpace(value), "[]")
}

func normalizeTaskClients(values []string) []string {
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

func RouteSelectionsForClient(uuid string) []CarrierRouteSelection {
	selections := make([]CarrierRouteSelection, 0)
	seen := make(map[string]struct{})
	for _, task := range CurrentRouteTasks() {
		if !task.Enabled || !containsClient(task.Clients, uuid) || task.Region == "" {
			continue
		}
		key := task.ID
		if key == "" {
			key = selectionID(task.Region, task.Carrier, task.Family)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		selections = append(selections, CarrierRouteSelection{
			Region: task.Region, Carrier: task.Carrier, Family: task.Family,
			TaskID: task.ID, TaskName: task.Name,
		})
	}
	return sortSelections(selections)
}

func RouteIntervalForClient(uuid string) int {
	interval := 0
	for _, task := range CurrentRouteTasks() {
		if !task.Enabled || !containsClient(task.Clients, uuid) {
			continue
		}
		if interval == 0 || task.IntervalSeconds < interval {
			interval = task.IntervalSeconds
		}
	}
	if interval == 0 {
		return DefaultIntervalSeconds()
	}
	return interval
}

func containsClient(values []string, uuid string) bool {
	for _, value := range values {
		if value == uuid {
			return true
		}
	}
	return false
}
