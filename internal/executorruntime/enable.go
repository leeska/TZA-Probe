package executorruntime

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

type EnableSettings struct {
	ExpectedIP string `json:"expected_ip"`
	Country    string `json:"country"`
}

func (settings EnableSettings) Validate() error {
	address, err := netip.ParseAddr(settings.ExpectedIP)
	if err != nil || address.String() != settings.ExpectedIP || !address.IsGlobalUnicast() || address.IsPrivate() ||
		address.Is4In6() || address.IsLinkLocalUnicast() {
		return errors.New("请填写 Agent 实际使用的公网出口 IP")
	}
	for _, reserved := range []string{"192.0.0.0/24", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "198.18.0.0/15", "100.64.0.0/10", "2001:db8::/32"} {
		if netip.MustParsePrefix(reserved).Contains(address) {
			return errors.New("出口 IP 不能使用保留或测试地址")
		}
	}
	if !strings.Contains(" US CA GB DE FR NL SE NO FI CH AT IT ES PT IE PL CZ AU NZ JP KR SG HK TW IN BR MX ZA AE ", " "+settings.Country+" ") {
		return errors.New("请选择支持的目标地区")
	}
	return nil
}

func EnableConfig(settings EnableSettings) ipsentinel.Config {
	family := "ipv4"
	if address, _ := netip.ParseAddr(settings.ExpectedIP); address.Is6() {
		family = "ipv6"
	}
	environment := ipsentinel.Environment{Locale: "en-US", Timezone: "UTC"}
	services := []ipsentinel.ServicePlan{}
	for _, entry := range []struct {
		id, adapter, observe, experiment string
		origins                          []string
	}{
		{"google", "google_home", "google-home-neutral-v1", "google-home-ncr-v1", []string{"https://www.google.com", "https://www.google.com.hk", "https://www.gstatic.com"}},
		{"youtube", "youtube_region", "youtube-premium-v1", "", []string{"https://www.youtube.com", "https://www.gstatic.com", "https://i.ytimg.com", "https://www.google.com"}},
		{"meta", "meta_ads_library", "meta-ads-neutral-v1", "meta-ads-country-v1", []string{"https://www.facebook.com", "https://static.xx.fbcdn.net", "https://scontent.xx.fbcdn.net"}},
	} {
		service := ipsentinel.ServicePlan{
			ServiceID: entry.id, Enabled: true, AdapterID: entry.adapter, ObservationProfileID: entry.observe,
			ObservationEnvironment: environment, AllowedOrigins: append(entry.origins, "https://api64.ipify.org"), TargetCountry: settings.Country,
		}
		if entry.experiment != "" {
			service.Experiment = &ipsentinel.ExperimentPlan{ProfileID: entry.experiment, Environment: environment, DwellMinMS: 4000, DwellMaxMS: 12000}
		}
		services = append(services, service)
	}
	return ipsentinel.Config{
		ConfigVersion: 1, Egress: ipsentinel.EgressConfig{ExpectedIP: settings.ExpectedIP, Family: family, CheckProfileID: "ipify-public-v1"},
		Services: services, Limits: ipsentinel.Limits{TimeoutMS: 120000, MemoryBytes: 461373440, CPUMillicores: 850, PIDs: 256, NetworkBytes: 20971520, HTTPRequests: 150},
		DailyBudget: ipsentinel.DailyLimits{Runs: 8, Navigations: 32, NetworkBytes: 167772160},
	}
}

func validateEnableReview(request ChannelRequest, result ChannelResult) error {
	if result.Operation == nil || result.Review != nil || request.Enable == nil {
		return errors.New("first enablement requires a reviewed plugin configuration")
	}
	review := result.Operation
	plan := review.Plan
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil || digest != result.PlanSHA256 || review.PlanSHA256 != digest ||
		review.ApprovalTemplate != (OperationApproval{FormatVersion: 1, Action: "enable", PlanSHA256: digest}) ||
		plan.FormatVersion != 1 || plan.Target != request.Enrollment.Target || plan.Version != request.Version ||
		plan.Options.Action != "enable" || plan.Options.InstallRoot != DefaultInstallRoot ||
		!absoluteClean(plan.Options.ReferencePath) || plan.Options.StateDir != "" || plan.Options.ConfigPath != "" || plan.Options.PolicyPath != "" || plan.Options.Unit != "" ||
		plan.StagePlanSHA256 != request.StagePlanSHA256 || !validDigest(plan.ArchiveSHA256) ||
		plan.InstallerSHA256 != ReferenceInstallerSHA256 || !validDigest(plan.SigningKeySHA256) ||
		plan.ExpectedCurrent != "" || plan.SelectionSHA256 != "" ||
		plan.Enable == nil || *plan.Enable != *request.Enable || plan.Enable.Validate() != nil {
		return errors.New("first enablement differs from the approved node, installation or settings")
	}
	return nil
}

func validateEnableReceipt(request ChannelRequest, result ChannelResult) error {
	receipt := result.OperationReceipt
	if receipt == nil || result.Receipt != nil || request.Operation == nil || request.Enable == nil || receipt.Hello == nil ||
		result.State != "enabled" || receipt.State != "enabled" || receipt.FormatVersion != 1 || !receipt.Selected || !receipt.Activated ||
		receipt.PlanSHA256 != request.PlanSHA256 || receipt.Release != receipt.Plan.ArchiveSHA256 {
		return errors.New("first enablement has no verified startup receipt")
	}
	digest, err := ipsentinel.CanonicalSHA256(receipt.Plan)
	if err != nil || digest != request.PlanSHA256 || result.PlanSHA256 != request.PlanSHA256 {
		return errors.New("first enablement receipt differs from approval")
	}
	observed, err := ipsentinel.ParseContractTime(receipt.Hello.Status.ObservedAt)
	if err != nil {
		return err
	}
	if request.ApprovedAt <= 0 || observed.Before(time.Unix(request.ApprovedAt-5, 0)) || observed.After(time.Now().Add(5*time.Second)) {
		return errors.New("plugin startup observation predates its approval")
	}
	return ValidateEnabledHello(*receipt.Hello, request.Version, *request.Enable, observed)
}

func ValidateEnabledHello(hello ipsentinel.Hello, version string, settings EnableSettings, now time.Time) error {
	raw, err := json.Marshal(hello)
	if err != nil || ipsentinel.ValidateDefinition("hello", raw, nil) != nil {
		return errors.New("plugin returned an invalid startup observation")
	}
	status := hello.Status
	observed, err := ipsentinel.ParseContractTime(status.ObservedAt)
	configDigest, digestErr := ipsentinel.CanonicalSHA256(EnableConfig(settings))
	if err != nil || digestErr != nil || observed.Before(now.Add(-30*time.Second)) || observed.After(now.Add(5*time.Second)) ||
		hello.RuntimeVersion != version || status.Control.Mode != ipsentinel.ModeManaged ||
		status.Health != "ready" || status.Control.Paused || status.Control.Draining || status.Control.ActiveRunID != nil ||
		!hello.LocalPolicy.HardLimitsAvailable || status.Config == nil || status.Config.SHA256 != configDigest {
		return errors.New("plugin has not confirmed a ready, isolated instance with the approved configuration")
	}
	for _, feature := range []string{"run", "config", "journal", "pause", "assessment"} {
		found := false
		for _, available := range hello.Features {
			found = found || feature == available
		}
		if !found {
			return errors.New("plugin startup lacks a required capability")
		}
	}
	return nil
}
