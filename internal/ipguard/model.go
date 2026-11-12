package ipguard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"
)

const Protocol = "tza.ipguard-egress/1"

type Service struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Adapter    string   `json:"adapter"`
	Observe    string   `json:"observe"`
	Experiment string   `json:"experiment,omitempty"`
	Origins    []string `json:"origins"`
}

var Services = []Service{
	{ID: "google", Name: "Google", Adapter: "google_home", Observe: "https://www.google.com/", Experiment: "https://www.google.com/ncr", Origins: []string{"www.google.com", "www.google.com.hk", "www.gstatic.com", "fonts.gstatic.com", "policies.google.com"}},
	{ID: "meta", Name: "Meta", Adapter: "meta_ads_library", Observe: "https://www.facebook.com/ads/library/", Experiment: "https://www.facebook.com/ads/library/?country={country}&ad_type=all&active_status=active", Origins: []string{"www.facebook.com", "static.xx.fbcdn.net", "scontent.xx.fbcdn.net"}},
}

func Lookup(id string) (Service, bool) {
	for _, service := range Services {
		if service.ID == id {
			return service, true
		}
	}
	return Service{}, false
}

func PermittedHost(serviceID, host string) bool {
	service, ok := Lookup(serviceID)
	if !ok {
		return false
	}
	if host == "api64.ipify.org" {
		return true
	}
	if slices.Contains(service.Origins, host) {
		return true
	}
	if serviceID == "meta" && strings.HasPrefix(host, "scontent-") && strings.HasSuffix(host, ".xx.fbcdn.net") {
		prefix := strings.TrimSuffix(strings.TrimPrefix(host, "scontent-"), ".xx.fbcdn.net")
		return prefix != "" && strings.IndexFunc(prefix, func(character rune) bool {
			return character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9')
		}) < 0
	}
	return false
}

func PermittedPageOrigin(serviceID, origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || origin != "https://"+parsed.Hostname() {
		return false
	}
	service, ok := Lookup(serviceID)
	if !ok {
		return false
	}
	for _, destination := range []string{service.Observe, service.Experiment} {
		page, err := url.Parse(destination)
		if err == nil && page.Hostname() == parsed.Hostname() {
			return true
		}
	}
	return serviceID == "google" && parsed.Hostname() == "www.google.com.hk"
}

func PublicAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.Is4In6() {
		return false
	}
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, reserved := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/23", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(reserved).Contains(address) {
			return false
		}
	}
	return true
}

type Policy struct {
	Revision        int64     `json:"revision"`
	Enabled         bool      `json:"enabled"`
	AutoRecover     bool      `json:"auto_recover"`
	ExpectedIP      string    `json:"expected_ip"`
	Country         string    `json:"country"`
	Locale          string    `json:"locale"`
	Timezone        string    `json:"timezone"`
	Position        *Position `json:"position,omitempty"`
	Services        []string  `json:"services"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	NetworkBytes    int64     `json:"network_bytes"`
	IntervalMinutes int       `json:"interval_minutes"`
	DwellSeconds    int       `json:"dwell_seconds"`
}

type Settings struct {
	Revision    int64     `json:"revision"`
	Enabled     bool      `json:"enabled"`
	AutoRecover bool      `json:"auto_recover"`
	Family      string    `json:"family"`
	Country     string    `json:"country"`
	Services    []string  `json:"services"`
	Position    *Position `json:"position,omitempty"`
}

func (settings Settings) Policy(previous Policy, status Status) (Policy, error) {
	environment, ok := Environments[settings.Country]
	if !ok {
		return Policy{}, errors.New("目标地区暂不支持")
	}
	if settings.Family != "ipv4" && settings.Family != "ipv6" {
		return Policy{}, errors.New("请选择 IPv4 或 IPv6")
	}
	address, err := netip.ParseAddr(previous.ExpectedIP)
	retain := err == nil && (settings.Family == "ipv4") == address.Is4()
	if settings.Enabled || !retain {
		previous.ExpectedIP = ""
		if status.Enabled && time.Now().Unix()-status.ReceivedAt < 45 {
			for _, candidate := range status.AllowedIPs {
				parsed, err := netip.ParseAddr(candidate)
				if err == nil && PublicAddress(parsed) && parsed.String() == candidate && (settings.Family == "ipv4") == parsed.Is4() {
					previous.ExpectedIP = candidate
					if candidate == address.String() {
						break
					}
				}
			}
		}
		if previous.ExpectedIP == "" {
			if !status.Enabled || time.Now().Unix()-status.ReceivedAt >= 45 {
				return Policy{}, errors.New("节点检测出口未连接，请稍后再试")
			}
			return Policy{}, errors.New("所选出口尚未开通，请选择可用的 IPv4 或 IPv6")
		}
	}
	previous.Revision = settings.Revision
	previous.Enabled = settings.Enabled
	previous.AutoRecover = settings.AutoRecover
	previous.Country = settings.Country
	previous.Locale = environment.Locale
	previous.Timezone = environment.Timezone
	previous.Position = settings.Position
	if settings.Position != nil {
		if err := settings.Position.Validate(settings.Country); err != nil {
			return Policy{}, err
		}
		previous.Timezone = settings.Position.Timezone
	}
	previous.Services = settings.Services
	previous.IntervalMinutes = 720
	if previous.TimeoutSeconds == 0 {
		previous.TimeoutSeconds = 120
		previous.NetworkBytes = 20 << 20
		previous.DwellSeconds = 8
	}
	return previous, nil
}

func SameEnvironment(first, second Policy) bool {
	return first.ExpectedIP == second.ExpectedIP && first.Country == second.Country && first.Locale == second.Locale && first.Timezone == second.Timezone && samePosition(first.Position, second.Position)
}

func samePosition(first, second *Position) bool {
	if first == nil || second == nil {
		return first == second
	}
	return first.Latitude == second.Latitude && first.Longitude == second.Longitude && first.Country == second.Country && first.Timezone == second.Timezone
}

func (policy Policy) Validate() error {
	if policy.Position != nil {
		if err := policy.Position.Validate(policy.Country); err != nil {
			return err
		}
		if policy.Timezone != policy.Position.Timezone {
			return errors.New("时区与定位不一致")
		}
	}
	address, err := netip.ParseAddr(policy.ExpectedIP)
	if err != nil || !PublicAddress(address) || address.String() != policy.ExpectedIP {
		return errors.New("需要规范的公网出口 IP")
	}
	if len(policy.Country) != 2 || strings.IndexFunc(policy.Country, func(character rune) bool { return character < 'A' || character > 'Z' }) >= 0 {
		return errors.New("目标地区需要两位大写代码")
	}
	if policy.Locale == "" || len(policy.Locale) > 40 || strings.IndexFunc(policy.Locale, func(character rune) bool {
		return character != '-' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9')
	}) >= 0 {
		return errors.New("语言代码无效")
	}
	if policy.Timezone == "" || policy.Timezone == "Local" {
		return errors.New("需要 IANA 时区")
	}
	if _, err := time.LoadLocation(policy.Timezone); err != nil {
		return errors.New("时区无效")
	}
	if len(policy.Services) < 1 || len(policy.Services) > len(Services) {
		return errors.New("请选择服务")
	}
	seen := map[string]bool{}
	for _, id := range policy.Services {
		if _, ok := Lookup(id); !ok || seen[id] {
			return errors.New("服务无效或重复")
		}
		seen[id] = true
	}
	if policy.TimeoutSeconds < 10 || policy.TimeoutSeconds > 180 || policy.NetworkBytes < 1<<20 || policy.NetworkBytes > 100<<20 || policy.IntervalMinutes != 0 && policy.IntervalMinutes < 60 || policy.IntervalMinutes > 10080 || policy.DwellSeconds < 1 || policy.DwellSeconds > 30 {
		return errors.New("超出单次检测或排期范围")
	}
	return nil
}

type Job struct {
	ID         string  `json:"id"`
	NodeID     string  `json:"node_id"`
	Service    string  `json:"service"`
	Phase      string  `json:"phase"`
	Policy     Policy  `json:"policy"`
	State      string  `json:"state"`
	CreatedAt  int64   `json:"created_at"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at"`
	Lease      string  `json:"lease,omitempty"`
	Result     *Result `json:"result,omitempty"`
}

type Page struct {
	Outcome     string `json:"outcome"`
	FinalOrigin string `json:"final_origin"`
	Signal      string `json:"signal"`
	Value       string `json:"value"`
}

func CountrySignal(service string) string {
	switch service {
	case "google":
		return "google_page_country"
	case "meta":
		return "ads_library_country"
	}
	return ""
}

func (page Page) Country(service string) string {
	signal := CountrySignal(service)
	if signal == "" || page.Signal != signal || len(page.Value) != 2 || strings.IndexFunc(page.Value, func(character rune) bool { return character < 'A' || character > 'Z' }) >= 0 {
		return ""
	}
	return page.Value
}

func (job Job) CurrentSignal() bool {
	return job.Result != nil && job.Result.Page.Signal == CountrySignal(job.Service)
}

type Result struct {
	Execution           string             `json:"execution"`
	Reason              string             `json:"reason"`
	EgressBefore        string             `json:"egress_before"`
	EgressAfter         string             `json:"egress_after"`
	Sandbox             bool               `json:"sandbox"`
	NetworkIsolated     bool               `json:"network_isolated"`
	BrowserVersion      string             `json:"browser_version"`
	Page                Page               `json:"page"`
	DeniedOrigins       []string           `json:"denied_origins"`
	NetworkBytes        int64              `json:"network_bytes"`
	Assessment          string             `json:"assessment"`
	GeolocationOverride bool               `json:"geolocation_override"`
	GoogleTerms         *RegionObservation `json:"google_terms,omitempty"`
}

type RegionObservation struct {
	Page       Page   `json:"page"`
	Reason     string `json:"reason"`
	Assessment string `json:"assessment"`
}

type Store interface {
	Policies(context.Context) (map[string]Policy, error)
	SavePolicy(context.Context, string, Policy) (Policy, error)
	Enqueue(context.Context, string, string, string, string) (Job, error)
	Jobs(context.Context, string) ([]Job, error)
	Claim(context.Context) (*Job, error)
	Running(context.Context, string, string) (*Job, error)
	Complete(context.Context, string, string, Result) error
	Cancel(context.Context, string) error
	Recover(context.Context) error
}

func ID() string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

func Active(state string) bool { return state == "queued" || state == "running" }
