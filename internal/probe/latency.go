package probe

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/config"
)

const (
	carrierPingManagedBy          = "tza-carrier-ping"
	carrierPingSelectionsKey      = "carrier_ping_selections"
	carrierPingIntervalKey        = "carrier_ping_interval_seconds"
	defaultPingIntervalSeconds    = 60
	minimumPingIntervalSeconds    = 10
	maximumPingIntervalSeconds    = 3600
	CarrierPingSelectionsKey      = carrierPingSelectionsKey
	CarrierPingIntervalSecondsKey = carrierPingIntervalKey
)

func DefaultPingIntervalSeconds() int {
	return defaultPingIntervalSeconds
}

func MinimumPingIntervalSeconds() int {
	return minimumPingIntervalSeconds
}

func NormalizePingIntervalSeconds(seconds int) int {
	if seconds < minimumPingIntervalSeconds {
		return minimumPingIntervalSeconds
	}
	if seconds > maximumPingIntervalSeconds {
		return maximumPingIntervalSeconds
	}
	return seconds
}

func CurrentPingSelections() []CarrierRouteSelection {
	raw, err := config.GetAs[any](carrierPingSelectionsKey, []any{})
	if err != nil {
		return []CarrierRouteSelection{}
	}
	selections, err := NormalizeSelections(raw)
	if err != nil {
		return []CarrierRouteSelection{}
	}
	return selections
}

// SyncManagedPingTasks materializes only the selected local catalogue entries.
// An empty selection removes TZA-managed tasks without touching manual tasks.
func SyncManagedPingTasks() error {
	seconds, _ := config.GetAs[int](carrierPingIntervalKey, defaultPingIntervalSeconds)
	return SyncManagedPingTasksWith(CurrentPingSelections(), seconds)
}

func SyncManagedPingTasksWith(selections []CarrierRouteSelection, intervalSeconds int) error {
	nodes, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return fmt.Errorf("load clients for carrier ping: %w", err)
	}
	clientIDs := make(models.StringArray, 0, len(nodes))
	for _, node := range nodes {
		if uuid := strings.TrimSpace(node.UUID); uuid != "" {
			clientIDs = append(clientIDs, uuid)
		}
	}
	sort.Strings(clientIDs)
	desired, err := managedPingTasks(selections, intervalSeconds, clientIDs)
	if err != nil {
		return err
	}
	return tasks.SyncManagedPingTasks(carrierPingManagedBy, desired)
}

func managedPingTasks(selections []CarrierRouteSelection, intervalSeconds int, clientIDs models.StringArray) ([]models.PingTask, error) {
	normalized, err := NormalizeSelections(selections)
	if err != nil {
		return nil, err
	}
	options := make(map[string]CatalogOption, len(CatalogOptions()))
	for _, option := range CatalogOptions() {
		options[option.ID] = option
	}
	intervalSeconds = NormalizePingIntervalSeconds(intervalSeconds)
	desired := make([]models.PingTask, 0, len(normalized))
	for _, selection := range normalized {
		key := selectionID(selection.Region, selection.Carrier, selection.Family)
		option, ok := options[key]
		if !ok {
			return nil, fmt.Errorf("carrier ping target is not in the local catalogue: %s", key)
		}
		port := option.Port
		if port <= 0 || port > 65535 {
			port = 80
		}
		desired = append(desired, models.PingTask{
			Name:       fmt.Sprintf("TZA · %s · %s · %s", option.Region, carrierDisplayName(option.Carrier), strings.ToUpper(option.Family)),
			Clients:    append(models.StringArray(nil), clientIDs...),
			DefaultOn:  true,
			Type:       "tcp",
			Target:     net.JoinHostPort(option.Host, strconv.Itoa(port)),
			Interval:   intervalSeconds,
			ManagedKey: key,
			Region:     option.Region,
			Carrier:    option.Carrier,
			Family:     option.Family,
		})
	}
	return desired, nil
}

func carrierDisplayName(carrier string) string {
	switch normalizeCarrier(carrier) {
	case "telecom":
		return "电信"
	case "unicom":
		return "联通"
	case "mobile":
		return "移动"
	default:
		return carrier
	}
}
