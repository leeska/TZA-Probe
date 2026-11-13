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

// 节点所在地区的自动检测：中文名、大陆取城市（去掉「市」）、港澳台用简称、其他国家用国名；
// 同一个 IP 一天内只查一次；是否能作为地区偏移的目标地区写在 supported 里。
func TestDescribe(t *testing.T) {
	locator := NewLocator()
	answers := map[string]string{
		"1.2.3.4": `{"success":true,"ip":"1.2.3.4","country_code":"CN","country":"中国","region":"广东","city":"深圳市","latitude":22.5,"longitude":114.1,"timezone":{"id":"Asia/Shanghai"}}`,
		"5.6.7.8": `{"success":true,"ip":"5.6.7.8","country_code":"HK","country":"中国香港","region":"","city":"","latitude":22.3,"longitude":114.2,"timezone":{"id":"Asia/Hong_Kong"}}`,
		"8.8.8.8": `{"success":true,"ip":"8.8.8.8","country_code":"US","country":"美国","region":"加利福尼亚","city":"山景城","latitude":37.4,"longitude":-122.1,"timezone":{"id":"America/Los_Angeles"}}`,
	}
	requests := 0
	locator.Client.Transport = locationTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Host != "ipwho.is" || request.URL.Query().Get("lang") != "zh-CN" {
			t.Fatal("unexpected lookup", request.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(answers[strings.TrimPrefix(request.URL.Path, "/")]))}, nil
	})
	ctx := context.Background()
	for ip, want := range map[string]string{"1.2.3.4": "深圳", "5.6.7.8": "香港", "8.8.8.8": "美国"} {
		place, err := locator.Describe(ctx, ip)
		if err != nil || place.Region != want {
			t.Fatalf("%s → %+v %v, want %s", ip, place, err, want)
		}
	}
	if place, _ := locator.Describe(ctx, "1.2.3.4"); place.CountryCode != "CN" || place.Supported != (Environments["CN"] != Environment{}) {
		t.Fatalf("supported 跟着地区偏移支持的目标地区走：%+v", place)
	}
	if place, _ := locator.Describe(ctx, "8.8.8.8"); !place.Supported || place.Timezone != "America/Los_Angeles" {
		t.Fatalf("%+v", place)
	}
	if requests != 3 {
		t.Fatalf("同一个 IP 要缓存：%d 次请求", requests)
	}
	if RegionName(IPPlace{CountryCode: "CN", Province: "上海市"}) != "上海" {
		t.Fatal("没有城市时用省级名称")
	}
}
