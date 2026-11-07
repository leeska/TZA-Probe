package executorruntime

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

func TestPluginEnableSettingsAndDefaultConfiguration(t *testing.T) {
	for _, ip := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		settings := EnableSettings{ExpectedIP: ip, Country: "JP"}
		if err := settings.Validate(); err != nil {
			t.Fatal(err)
		}
		config := EnableConfig(settings)
		raw, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := ipsentinel.ValidateDefinition("config", raw, nil); err != nil {
			t.Fatal(err)
		}
		if len(config.Services) != 3 || config.DailyBudget.Runs != 8 {
			t.Fatal("default service coverage or node budget changed")
		}
	}
	for _, ip := range []string{"", "127.0.0.1", "10.0.0.1", "192.0.2.1", "100.64.0.1", "::1", "::ffff:8.8.8.8", "2001:db8::1", "example.org"} {
		if (EnableSettings{ExpectedIP: ip, Country: "JP"}).Validate() == nil {
			t.Fatal("accepted non-public egress", ip)
		}
	}
	if (EnableSettings{ExpectedIP: "8.8.8.8", Country: "jp"}).Validate() == nil {
		t.Fatal("accepted invalid region")
	}
}

func TestEnabledHelloRequiresReadyApprovedConfiguration(t *testing.T) {
	raw, err := os.ReadFile("../ipsentinel/testdata/contracts/ipsentinel-executor-v1/examples/hello.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result ipsentinel.Hello `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	settings := EnableSettings{ExpectedIP: "8.8.8.8", Country: "JP"}
	hello := envelope.Result
	hello.Status.Control.Mode = ipsentinel.ModeManaged
	hello.Status.Control.Paused = false
	hello.Status.Control.PauseReason = nil
	hello.Status.Control.ActiveRunID = nil
	hello.Status.Health = "ready"
	hello.Features = []string{"run", "config", "journal", "pause", "assessment"}
	hello.LocalPolicy.HardLimitsAvailable = true
	digest, _ := ipsentinel.ConfigSHA256(EnableConfig(settings))
	hello.Status.Config = &ipsentinel.ConfigRef{Revision: 1, SHA256: digest}
	now, _ := ipsentinel.ParseContractTime(hello.Status.ObservedAt)
	if err := ValidateEnabledHello(hello, hello.RuntimeVersion, settings, now); err != nil {
		t.Fatal(err)
	}
	hello.LocalPolicy.HardLimitsAvailable = false
	if ValidateEnabledHello(hello, hello.RuntimeVersion, settings, now) == nil {
		t.Fatal("accepted missing kernel isolation")
	}
	hello.LocalPolicy.HardLimitsAvailable = true
	settings.Country = "US"
	if ValidateEnabledHello(hello, hello.RuntimeVersion, settings, now) == nil {
		t.Fatal("accepted a different approved configuration")
	}
}
