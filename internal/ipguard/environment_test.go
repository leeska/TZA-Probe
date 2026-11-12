package ipguard

import (
	"testing"
	"time"
)

func TestRegionAndAuthorizedFamilyResolveOnServer(t *testing.T) {
	for country, environment := range Environments {
		settings := Settings{Family: "ipv6", Country: country, Enabled: true, Services: []string{"google"}}
		policy, err := settings.Policy(Policy{}, Status{Enabled: true, ReceivedAt: time.Now().Unix(), AllowedIPs: []string{"8.8.8.8", "2606:4700:4700::1111"}})
		if err != nil || policy.Locale != environment.Locale || policy.Timezone != environment.Timezone || policy.ExpectedIP != "2606:4700:4700::1111" {
			t.Fatal(country, policy, err)
		}
		if err := policy.Validate(); err != nil {
			t.Fatal(country, err)
		}
	}
	settings := Settings{Family: "ipv4", Country: "SG", Enabled: true, Services: []string{"google"}}
	for _, status := range []Status{
		{Enabled: true, ReceivedAt: time.Now().Unix(), AllowedIPs: []string{"2606:4700:4700::1111"}},
		{Enabled: true, ReceivedAt: time.Now().Add(-time.Minute).Unix(), AllowedIPs: []string{"8.8.8.8"}},
		{Enabled: false, ReceivedAt: time.Now().Unix(), AllowedIPs: []string{"8.8.8.8"}},
		{Enabled: true, ReceivedAt: time.Now().Unix(), AllowedIPs: []string{"127.0.0.1"}},
	} {
		if _, err := settings.Policy(testPolicy(), status); err == nil {
			t.Fatal("unavailable exit admitted", status)
		}
	}
	settings.Enabled = false
	policy, err := settings.Policy(testPolicy(), Status{})
	if err != nil || policy.ExpectedIP != "8.8.8.8" {
		t.Fatal("offline disable blocked", err)
	}
	policy, err = settings.Policy(testPolicy(), Status{Enabled: true, ReceivedAt: time.Now().Unix(), AllowedIPs: []string{"2606:4700:4700::1111"}})
	if err != nil || policy.Enabled || policy.ExpectedIP != "8.8.8.8" {
		t.Fatal("revoked exit prevented disabling detection", err)
	}
	settings.Country = "ZZ"
	if _, err := settings.Policy(testPolicy(), Status{}); err == nil {
		t.Fatal("unknown region accepted")
	}
}

func TestReportedAddressesArePrivateAndCanonical(t *testing.T) {
	broker := NewBroker(testStore(t))
	addresses := []string{"8.8.8.8", "127.0.0.1", "8.8.8.8", "2606:4700:4700::1111", "::ffff:8.8.8.8"}
	broker.Report("node", Status{Enabled: true, AllowedIPs: addresses})
	addresses[0] = "1.1.1.1"
	status := broker.Status("node")
	if len(status.AllowedIPs) != 2 || status.AllowedIPs[1] != "8.8.8.8" {
		t.Fatal(status)
	}
}
