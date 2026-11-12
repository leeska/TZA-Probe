package ipguard

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

type locationTransport func(*http.Request) (*http.Response, error)

func (transport locationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestPositionValidationAndEnvironment(t *testing.T) {
	position := Position{Latitude: 37.77, Longitude: -122.42, Country: "US", Timezone: "America/Los_Angeles"}
	settings := Settings{Country: "US", Family: "ipv4", Enabled: true, Services: []string{"google"}, Position: &position}
	policy, err := settings.Policy(testPolicy(), Status{Enabled: true, ReceivedAt: time.Now().Unix(), AllowedIPs: []string{"8.8.8.8"}})
	if err != nil || policy.Timezone != "America/Los_Angeles" || policy.Locale != "en-US" || policy.Validate() != nil {
		t.Fatal(policy, err)
	}
	if SameEnvironment(policy, testPolicy()) {
		t.Fatal("position reused old evidence")
	}
	for _, change := range []func(*Position){func(value *Position) { value.Latitude = math.NaN() }, func(value *Position) { value.Latitude = 91 }, func(value *Position) { value.Longitude = math.Inf(1) }, func(value *Position) { value.Longitude = -181 }, func(value *Position) { value.Country = "JP" }, func(value *Position) { value.Timezone = "Local" }} {
		invalid := position
		change(&invalid)
		if invalid.Validate("US") == nil {
			t.Fatal("invalid position accepted", invalid)
		}
	}
}

func TestLocationServicesFixedHostsCacheAndSearch(t *testing.T) {
	locator := NewLocator()
	requests := 0
	locator.Client.Transport = locationTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		var body string
		switch request.URL.Host {
		case "ipwho.is":
			body = `{"success":true,"ip":"8.8.8.8","country_code":"US","latitude":37.77,"longitude":-122.42,"timezone":{"id":"America/Los_Angeles"}}`
		case "photon.komoot.io":
			body = `{"features":[{"properties":{"name":"Tokyo","country":"Japan"},"geometry":{"coordinates":[139.69,35.68]}}]}`
		case "nominatim.openstreetmap.org":
			body = `{"address":{"country_code":"jp","city":"Tokyo"}}`
		case "timeapi.io":
			body = `{"timeZone":"Asia/Tokyo"}`
		default:
			t.Fatal("unexpected lookup destination", request.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ctx := context.Background()
	for range 2 {
		if position, err := locator.Locate(ctx, "8.8.8.8", 0, 0); err != nil || position.Country != "US" {
			t.Fatal(position, err)
		}
	}
	if requests != 1 {
		t.Fatal("IP lookup not cached")
	}
	if places, err := locator.Search(ctx, "Tokyo"); err != nil || len(places) != 1 || places[0].Latitude != 35.68 {
		t.Fatal(places, err)
	}
	if _, err := locator.Search(ctx, "Tokyo"); err == nil {
		t.Fatal("unbounded search")
	}
	position, err := locator.Locate(ctx, "", 35.68, 139.69)
	if err != nil || position.Country != "JP" || position.Timezone != "Asia/Tokyo" {
		t.Fatal(position, err)
	}
	if _, err := locator.Locate(ctx, "", 35.69, 139.70); err == nil {
		t.Fatal("unbounded reverse lookup")
	}
}
