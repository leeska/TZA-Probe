package probe

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/internal/config"
	"github.com/komari-monitor/komari/internal/scheduler"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
)

const (
	defaultInterval    = time.Hour
	minimumInterval    = 15 * time.Minute
	maxStoredResultAge = 72 * time.Hour
	maxTargetsPerJob   = 12
	carrierRouteProbeTimeoutMs = 30_000
	carrierRouteProbeMaxHops   = 30
	enabledKey         = "carrier_route_enabled"
	intervalKey        = "carrier_route_interval_seconds"
	selectionsKey      = "carrier_route_selections"

	CarrierRouteEnabledKey    = enabledKey
	CarrierRouteIntervalKey   = intervalKey
	CarrierRouteSelectionsKey = selectionsKey
)

// CarrierRouteSelection identifies one independently monitored combination.
// The family is deliberately part of the selection so IPv4 and IPv6 can be
// enabled or disabled without affecting one another.
type CarrierRouteSelection struct {
	Region   string `json:"region"`
	Carrier  string `json:"carrier"`
	Family   string `json:"family"`
	TaskID   string `json:"task_id,omitempty"`
	TaskName string `json:"task_name,omitempty"`
}

type embeddedTarget struct {
	ID           string `json:"id"`
	Family       string `json:"family"`
	Region       string `json:"region"`
	Carrier      string `json:"carrier"`
	Host         string `json:"host"`
	IP           string `json:"ip"`
	Port         int    `json:"port"`
	Target       string `json:"target,omitempty"`
	BackupHost   string `json:"backup_host,omitempty"`
	BackupIP     string `json:"backup_ip,omitempty"`
	BackupPort   int    `json:"backup_port,omitempty"`
	BackupTarget string `json:"backup_target,omitempty"`
}

type embeddedCatalog struct {
	Source       string           `json:"source"`
	SourceFormat string           `json:"source_format"`
	SnapshotDate string           `json:"snapshot_date"`
	Targets      []embeddedTarget `json:"targets"`
}

//go:embed targets_embedded.json
var embeddedCatalogJSON []byte

type resultStore struct {
	mu      sync.RWMutex
	results map[string]map[string]v2.CarrierRouteEntry
}

var store = &resultStore{results: make(map[string]map[string]v2.CarrierRouteEntry)}

var (
	catalogOnce sync.Once
	catalog     embeddedCatalog
)

// CatalogOption is the stable option contract used by an administration UI.
// One option represents exactly one region/carrier/family combination.
type CatalogOption struct {
	ID         string `json:"id"`
	Region     string `json:"region"`
	Carrier    string `json:"carrier"`
	Family     string `json:"family"`
	Host       string `json:"host"`
	BackupHost string `json:"backup_host,omitempty"`
	IP         string `json:"ip"`
	Port       int    `json:"port"`
}

func loadEmbeddedCatalog() embeddedCatalog {
	catalogOnce.Do(func() {
		if err := json.Unmarshal(embeddedCatalogJSON, &catalog); err != nil {
			catalog = embeddedCatalog{}
		}
	})
	return catalog
}

// CatalogOptions returns a copy of the local target catalogue. It never makes
// a network request, so a settings page can render the choices offline.
func CatalogOptions() []CatalogOption {
	local := loadEmbeddedCatalog()
	options := make([]CatalogOption, 0, len(local.Targets))
	for _, target := range local.Targets {
		family := normalizeFamily(target.Family)
		carrier := normalizeCarrier(target.Carrier)
		if target.Region == "" || family == "" || carrier == "" || target.IP == "" {
			continue
		}
		host := strings.TrimSpace(target.Target)
		if host == "" {
			host = strings.TrimSpace(target.Host)
		}
		backupHost := strings.TrimSpace(target.BackupTarget)
		if backupHost == "" {
			backupHost = strings.TrimSpace(target.BackupHost)
		}
		options = append(options, CatalogOption{
			ID:         selectionID(target.Region, carrier, family),
			Region:     target.Region,
			Carrier:    carrier,
			Family:     family,
			Host:       host,
			BackupHost: backupHost,
			IP:         target.IP,
			Port:       target.Port,
		})
	}
	return options
}

func selectionID(region, carrier, family string) string {
	return strings.Join([]string{
		normalizeRegion(region),
		normalizeCarrier(carrier),
		normalizeFamily(family),
	}, ":")
}

func normalizeRegion(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func selectionSet(selections []CarrierRouteSelection) map[string]struct{} {
	set := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		set[selectionID(selection.Region, selection.Carrier, selection.Family)] = struct{}{}
	}
	return set
}

// NormalizeIntervalSeconds applies the safety bounds used by the scheduler.
// A zero or negative value therefore cannot accidentally create a tight loop.
func NormalizeIntervalSeconds(seconds int) int {
	if seconds < int(minimumInterval/time.Second) {
		return int(minimumInterval / time.Second)
	}
	if seconds > 24*60*60 {
		return 24 * 60 * 60
	}
	return seconds
}

func DefaultIntervalSeconds() int {
	return int(defaultInterval / time.Second)
}

func MinimumIntervalSeconds() int {
	return int(minimumInterval / time.Second)
}

// CurrentSelections returns the validated local configuration. Invalid saved
// values are treated as empty, preventing malformed settings from dispatching
// an unexpected target.
func CurrentSelections() []CarrierRouteSelection {
	raw, err := config.GetAs[any](selectionsKey, []any{})
	if err != nil {
		return []CarrierRouteSelection{}
	}
	selections, err := NormalizeSelections(raw)
	if err != nil {
		return []CarrierRouteSelection{}
	}
	return selections
}

// NormalizeSelections accepts the structured JSON used by the new UI and a
// compact region:carrier:family string for backwards-compatible manual edits.
// Every item must exist in the embedded catalogue.
func NormalizeSelections(value any) ([]CarrierRouteSelection, error) {
	items, err := flattenSelectionValues(value)
	if err != nil {
		return nil, err
	}
	available := make(map[string]struct{}, len(CatalogOptions()))
	for _, option := range CatalogOptions() {
		available[option.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(items))
	selections := make([]CarrierRouteSelection, 0, len(items))
	for _, item := range items {
		family := normalizeFamily(item.Family)
		carrier := normalizeCarrier(item.Carrier)
		region := strings.TrimSpace(item.Region)
		if region == "" || family == "" || carrier == "" {
			return nil, fmt.Errorf("carrier route selection requires region, carrier and family")
		}
		id := selectionID(region, carrier, family)
		if _, ok := available[id]; !ok {
			return nil, fmt.Errorf("carrier route target is not in the local catalogue: %s", id)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		selections = append(selections, CarrierRouteSelection{Region: region, Carrier: carrier, Family: family})
	}
	return sortSelections(selections), nil
}

func flattenSelectionValues(value any) ([]CarrierRouteSelection, error) {
	switch typed := value.(type) {
	case nil:
		return []CarrierRouteSelection{}, nil
	case []CarrierRouteSelection:
		return append([]CarrierRouteSelection(nil), typed...), nil
	case []any:
		items := make([]CarrierRouteSelection, 0, len(typed))
		for _, item := range typed {
			flattened, err := flattenSelectionValues(item)
			if err != nil {
				return nil, err
			}
			items = append(items, flattened...)
		}
		return items, nil
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return []CarrierRouteSelection{}, nil
		}
		if strings.HasPrefix(value, "[") {
			var decoded []any
			if err := json.Unmarshal([]byte(value), &decoded); err != nil {
				return nil, fmt.Errorf("invalid carrier route selections JSON: %w", err)
			}
			return flattenSelectionValues(decoded)
		}
		items := make([]CarrierRouteSelection, 0)
		for _, token := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
			parts := strings.FieldsFunc(strings.TrimSpace(token), func(r rune) bool { return r == ':' || r == '|' })
			if len(parts) != 3 {
				return nil, fmt.Errorf("carrier route selection must be region:carrier:family")
			}
			items = append(items, CarrierRouteSelection{Region: parts[0], Carrier: parts[1], Family: parts[2]})
		}
		return items, nil
	case map[string]any:
		region := stringValue(typed, "region", "province")
		carrier := stringValue(typed, "carrier", "isp", "operator")
		if families, ok := typed["families"]; ok {
			familyValues, err := flattenFamilyValues(families)
			if err != nil {
				return nil, err
			}
			items := make([]CarrierRouteSelection, 0, len(familyValues))
			for _, family := range familyValues {
				items = append(items, CarrierRouteSelection{Region: region, Carrier: carrier, Family: family})
			}
			return items, nil
		}
		return []CarrierRouteSelection{{
			Region:  region,
			Carrier: carrier,
			Family:  stringValue(typed, "family", "ip_version", "version"),
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported carrier route selections value %T", value)
	}
}

func flattenFamilyValues(value any) ([]string, error) {
	switch typed := value.(type) {
	case []any:
		families := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				families = append(families, text)
			}
		}
		return families, nil
	case string:
		return strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }), nil
	default:
		return nil, fmt.Errorf("carrier route families must be an array or string")
	}
}

func stringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sortSelections(selections []CarrierRouteSelection) []CarrierRouteSelection {
	order := make(map[string]int)
	for index, option := range CatalogOptions() {
		order[option.ID] = index
	}
	sort.SliceStable(selections, func(i, j int) bool {
		return order[selectionID(selections[i].Region, selections[i].Carrier, selections[i].Family)] < order[selectionID(selections[j].Region, selections[j].Carrier, selections[j].Family)]
	})
	return selections
}

// RegisterSchedule installs the Core-side dispatcher. Actual network probing
// remains on each Agent, so a slow or unavailable target cannot block Core.
func RegisterSchedule() error {
	scheduler.RemovePrefix("carrier-route:")
	for _, configured := range CurrentRouteTasks() {
		if !configured.Enabled || len(configured.Clients) == 0 {
			continue
		}
		task := configured
		if err := scheduler.AddContextFunc(
			"carrier-route:"+task.ID,
			scheduler.Every(time.Duration(task.IntervalSeconds)*time.Second),
			true,
			func(context.Context) { runRouteTask(task) },
		); err != nil {
			return err
		}
	}
	return nil
}

func runRouteTask(task CarrierRouteTask) {
	target := v2.CarrierRouteTarget{
		ID: task.ID, Region: task.Region, Carrier: task.Carrier,
		Host: task.Host, BackupHost: task.BackupHost, Port: task.Port,
	}
	for _, uuid := range task.Clients {
		dispatchRouteProbe(task, target, uuid)
	}
}

// TriggerRouteTasksForClient dispatches the tasks assigned to a client as
// soon as its V2 session is ready. This closes the startup/reconnect gap where
// the scheduler's immediate run precedes Agent connections by many minutes.
func TriggerRouteTasksForClient(uuid string) {
	if strings.TrimSpace(uuid) == "" {
		return
	}
	for _, configured := range CurrentRouteTasks() {
		if !configured.Enabled || !containsClient(configured.Clients, uuid) {
			continue
		}
		task := configured
		target := v2.CarrierRouteTarget{
			ID: task.ID, Region: task.Region, Carrier: task.Carrier,
			Host: task.Host, BackupHost: task.BackupHost, Port: task.Port,
		}
		dispatchRouteProbe(task, target, uuid)
	}
}

func dispatchRouteProbe(task CarrierRouteTask, target v2.CarrierRouteTarget, uuid string) {
	if uuid == "" || !agent_runtime.IsV2Client(uuid) {
		return
	}
	params := v2.CarrierRouteProbeParams{
		JobID: newJobID(), Family: task.Family,
		Targets: []v2.CarrierRouteTarget{target}, TimeoutMs: carrierRouteProbeTimeoutMs,
		MaxHops: carrierRouteProbeMaxHops, MaxConcurrency: 1,
	}
	_ = agent_runtime.DispatchV2Event(uuid, v2.MethodAgentCarrierRouteProbe, params)
}

func newJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "route-" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("route-%d", time.Now().UnixNano())
}

func Record(uuid string, result v2.CarrierRouteProbeResult) {
	if uuid == "" {
		return
	}
	now := time.Now().UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	items := store.results[uuid]
	if items == nil {
		items = make(map[string]v2.CarrierRouteEntry)
		store.results[uuid] = items
	}
	for _, entry := range result.Results {
		family := normalizeFamily(entry.Family)
		if family == "" {
			family = normalizeFamily(result.Family)
		}
		if family == "" || strings.TrimSpace(entry.Carrier) == "" {
			continue
		}
		entry.Family = family
		if entry.CheckedAt.IsZero() {
			entry.CheckedAt = now
		} else {
			entry.CheckedAt = entry.CheckedAt.UTC()
		}
		items[resultKey(entry)] = entry
	}
	// Bound memory if an Agent changes its target catalogue repeatedly.
	for key, entry := range items {
		if now.Sub(entry.CheckedAt) > maxStoredResultAge {
			delete(items, key)
		}
	}
}

func resultKey(entry v2.CarrierRouteEntry) string {
	return strings.Join([]string{normalizeFamily(entry.Family), normalizeCarrier(entry.Carrier), entry.Region, entry.TargetID, entry.Target}, "\x00")
}

func Query(uuid string, families []string, region string, maxAgeSeconds int) []v2.CarrierRouteEntry {
	if uuid == "" {
		return []v2.CarrierRouteEntry{}
	}
	allowed := make(map[string]struct{}, len(families))
	for _, family := range families {
		if normalized := normalizeFamily(family); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	cutoff := time.Now().UTC().Add(-maxStoredResultAge)
	if maxAgeSeconds > 0 {
		age := time.Duration(maxAgeSeconds) * time.Second
		if age < maxStoredResultAge {
			cutoff = time.Now().UTC().Add(-age)
		}
	}
	region = strings.TrimSpace(region)
	store.mu.RLock()
	defer store.mu.RUnlock()
	items := make([]v2.CarrierRouteEntry, 0)
	for _, entry := range store.results[uuid] {
		if len(allowed) > 0 {
			if _, ok := allowed[normalizeFamily(entry.Family)]; !ok {
				continue
			}
		}
		if entry.CheckedAt.Before(cutoff) {
			continue
		}
		if region != "" && entry.Region != "" && entry.Region != "全国" && !strings.Contains(strings.ToLower(entry.Region), strings.ToLower(region)) {
			continue
		}
		items = append(items, entry)
	}
	sort.SliceStable(items, func(i, j int) bool {
		fi, fj := familyOrder(items[i].Family), familyOrder(items[j].Family)
		if fi != fj {
			return fi < fj
		}
		ci, cj := carrierOrder(items[i].Carrier), carrierOrder(items[j].Carrier)
		if ci != cj {
			return ci < cj
		}
		return items[i].CheckedAt.After(items[j].CheckedAt)
	})
	return items
}

func normalizeFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "4", "v4", "ipv4", "tcp4":
		return "ipv4"
	case "6", "v6", "ipv6", "tcp6":
		return "ipv6"
	default:
		return ""
	}
}

func normalizeCarrier(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(lower, "电信"), strings.Contains(lower, "telecom"), strings.Contains(lower, "ctcc"), strings.Contains(lower, "chinatelecom"):
		return "telecom"
	case strings.Contains(lower, "联通"), strings.Contains(lower, "unicom"), strings.Contains(lower, "cucc"):
		return "unicom"
	case strings.Contains(lower, "移动"), strings.Contains(lower, "mobile"), strings.Contains(lower, "cmcc"), strings.Contains(lower, "cmi"):
		return "mobile"
	default:
		return lower
	}
}

func familyOrder(family string) int {
	if normalizeFamily(family) == "ipv4" {
		return 0
	}
	return 1
}

func carrierOrder(carrier string) int {
	switch normalizeCarrier(carrier) {
	case "telecom":
		return 0
	case "unicom":
		return 1
	case "mobile":
		return 2
	default:
		return 9
	}
}

func loadTargets(family string, selections []CarrierRouteSelection) []v2.CarrierRouteTarget {
	if len(selections) == 0 {
		return []v2.CarrierRouteTarget{}
	}
	return filterEmbeddedTargets(family, selectionSet(selections))
}

func filterEmbeddedTargets(family string, selected map[string]struct{}) []v2.CarrierRouteTarget {
	local := loadEmbeddedCatalog()
	targets := make([]v2.CarrierRouteTarget, 0)
	for _, target := range local.Targets {
		if normalizeFamily(target.Family) != normalizeFamily(family) {
			continue
		}
		if _, ok := selected[selectionID(target.Region, target.Carrier, family)]; !ok {
			continue
		}
		targets = append(targets, v2.CarrierRouteTarget{
			ID:         target.ID,
			Region:     target.Region,
			Carrier:    normalizeCarrier(target.Carrier),
			Host:       firstNonEmpty(target.Target, target.Host),
			BackupHost: firstNonEmpty(target.BackupTarget, target.BackupHost),
			Port:       target.Port,
		})
	}
	return targets
}

// MarshalTargets is useful for diagnostics and future admin settings without
// exposing internal map state.
func MarshalTargets(family string) ([]byte, error) {
	local := loadEmbeddedCatalog()
	targets := make([]v2.CarrierRouteTarget, 0)
	for _, target := range local.Targets {
		if normalizeFamily(target.Family) != normalizeFamily(family) {
			continue
		}
		targets = append(targets, v2.CarrierRouteTarget{
			ID:         target.ID,
			Region:     target.Region,
			Carrier:    normalizeCarrier(target.Carrier),
			Host:       firstNonEmpty(target.Target, target.Host),
			BackupHost: firstNonEmpty(target.BackupTarget, target.BackupHost),
			Port:       target.Port,
		})
	}
	return json.Marshal(targets)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
