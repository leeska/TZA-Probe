package ipguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type Position struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Country   string  `json:"country"`
	City      string  `json:"city,omitempty"`
	Timezone  string  `json:"timezone"`
}

func (position Position) Validate(country string) error {
	if math.IsNaN(position.Latitude) || math.IsInf(position.Latitude, 0) || math.Abs(position.Latitude) > 90 || math.IsNaN(position.Longitude) || math.IsInf(position.Longitude, 0) || math.Abs(position.Longitude) > 180 {
		return errors.New("经纬度超出范围")
	}
	if position.Country != country || len(position.City) > 200 || strings.ContainsAny(position.City, "\r\n") {
		return errors.New("定位与目标地区不一致，请重新选点")
	}
	if position.Timezone == "" || position.Timezone == "Local" {
		return errors.New("定位缺少时区")
	}
	if _, err := time.LoadLocation(position.Timezone); err != nil {
		return errors.New("定位时区无效")
	}
	return nil
}

type locationCache struct {
	position Position
	expires  time.Time
}

type Locator struct {
	Client      *http.Client
	mu          sync.Mutex
	cache       map[string]locationCache
	places      map[string]placeCache
	nextReverse time.Time
	nextSearch  time.Time
}

func NewLocator() *Locator {
	return &Locator{Client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("定位服务重定向被拒绝") }}, cache: map[string]locationCache{}}
}

func (locator *Locator) get(ctx context.Context, destination string, result any) error {
	request, err := http.NewRequestWithContext(ctx, "GET", destination, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "TZA-Probe-region-location/1.0 (https://github.com/leeska/TZA-Probe)")
	response, err := locator.Client.Do(request)
	if err != nil {
		return errors.New("定位服务暂不可用，请稍后重试或手动选择地区")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("定位服务暂不可用，请稍后重试或手动选择地区")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(result); err != nil {
		return errors.New("定位服务返回无效数据")
	}
	return nil
}

type Place struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (locator *Locator) Search(ctx context.Context, query string) ([]Place, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 || len(query) > 200 {
		return nil, errors.New("请输入 2～200 字符的地点名称")
	}
	locator.mu.Lock()
	defer locator.mu.Unlock()
	if time.Now().Before(locator.nextSearch) {
		return nil, errors.New("搜索过快，请稍后再试")
	}
	locator.nextSearch = time.Now().Add(time.Second)
	var result struct {
		Features []struct {
			Properties struct {
				Name    string `json:"name"`
				City    string `json:"city"`
				Country string `json:"country"`
			} `json:"properties"`
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := locator.get(ctx, "https://photon.komoot.io/api/?"+url.Values{"q": {query}, "limit": {"5"}}.Encode(), &result); err != nil {
		return nil, err
	}
	places := []Place{}
	for _, feature := range result.Features {
		coordinates := feature.Geometry.Coordinates
		if len(coordinates) != 2 || math.Abs(coordinates[0]) > 180 || math.Abs(coordinates[1]) > 90 {
			continue
		}
		parts := []string{}
		for _, part := range []string{feature.Properties.Name, feature.Properties.City, feature.Properties.Country} {
			if part != "" && !slices.Contains(parts, part) {
				parts = append(parts, part)
			}
		}
		places = append(places, Place{Name: strings.Join(parts, " · "), Latitude: coordinates[1], Longitude: coordinates[0]})
		if len(places) == 5 {
			break
		}
	}
	return places, nil
}

func (locator *Locator) Locate(ctx context.Context, ip string, latitude, longitude float64) (Position, error) {
	key := ip
	if ip == "" {
		key = fmt.Sprintf("%.5f,%.5f", latitude, longitude)
	}
	locator.mu.Lock()
	defer locator.mu.Unlock()
	if cached, ok := locator.cache[key]; ok && time.Now().Before(cached.expires) {
		return cached.position, nil
	}
	var position Position
	if ip != "" {
		var result struct {
			Success   bool    `json:"success"`
			IP        string  `json:"ip"`
			Country   string  `json:"country_code"`
			City      string  `json:"city"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Timezone  struct {
				ID string `json:"id"`
			} `json:"timezone"`
		}
		if err := locator.get(ctx, "https://ipwho.is/"+url.PathEscape(ip), &result); err != nil {
			return position, err
		}
		if !result.Success || result.IP != ip {
			return position, errors.New("未取得此出口 IP 的定位，请手动选择地区")
		}
		position = Position{Latitude: result.Latitude, Longitude: result.Longitude, Country: result.Country, City: result.City, Timezone: result.Timezone.ID}
	} else {
		if time.Now().Before(locator.nextReverse) {
			return position, errors.New("选点查询过快，请稍后再试")
		}
		locator.nextReverse = time.Now().Add(time.Second)
		query := url.Values{"lat": {fmt.Sprint(latitude)}, "lon": {fmt.Sprint(longitude)}, "format": {"jsonv2"}, "zoom": {"10"}, "addressdetails": {"1"}}
		var address struct {
			Address struct {
				Country string `json:"country_code"`
				City    string `json:"city"`
				Town    string `json:"town"`
			} `json:"address"`
		}
		if err := locator.get(ctx, "https://nominatim.openstreetmap.org/reverse?"+query.Encode(), &address); err != nil {
			return position, err
		}
		var zone struct {
			Timezone string `json:"timeZone"`
		}
		query = url.Values{"latitude": {fmt.Sprint(latitude)}, "longitude": {fmt.Sprint(longitude)}}
		if err := locator.get(ctx, "https://timeapi.io/api/timezone/coordinate?"+query.Encode(), &zone); err != nil {
			return position, err
		}
		city := address.Address.City
		if city == "" {
			city = address.Address.Town
		}
		position = Position{Latitude: latitude, Longitude: longitude, Country: strings.ToUpper(address.Address.Country), City: city, Timezone: zone.Timezone}
	}
	if _, ok := Environments[position.Country]; !ok {
		return Position{}, errors.New("定位地区暂不支持，请手动选择目标地区")
	}
	if err := position.Validate(position.Country); err != nil {
		return Position{}, err
	}
	if len(locator.cache) >= 512 {
		clear(locator.cache)
	}
	locator.cache[key] = locationCache{position: position, expires: time.Now().Add(24 * time.Hour)}
	return position, nil
}

// IPPlace is where an IP is, for the node form's 自动检测: names in Chinese,
// and Region is the value 节点所在地区 uses (a city for the mainland, the
// short name of the country or area elsewhere).
type IPPlace struct {
	IP          string  `json:"ip"`
	CountryCode string  `json:"country_code"`
	Country     string  `json:"country"`
	Province    string  `json:"province"`
	City        string  `json:"city"`
	Region      string  `json:"region"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Timezone    string  `json:"timezone"`
	// Supported reports whether region drift can target this country.
	Supported bool `json:"supported"`
}

// shortAreas names the areas whose Chinese country name is long or political
// in the geolocation answer; 节点所在地区 uses the short form.
var shortAreas = map[string]string{"HK": "香港", "MO": "澳门", "TW": "台湾", "CN": "中国"}

// Describe looks ip up (ipwho.is, Chinese names) for any country, unlike
// Locate which only answers region drift's supported targets. Cached a day.
func (locator *Locator) Describe(ctx context.Context, ip string) (IPPlace, error) {
	key := "describe:" + ip
	locator.mu.Lock()
	defer locator.mu.Unlock()
	if cached, ok := locator.places[key]; ok && time.Now().Before(cached.expires) {
		return cached.place, nil
	}
	var result struct {
		Success     bool    `json:"success"`
		IP          string  `json:"ip"`
		CountryCode string  `json:"country_code"`
		Country     string  `json:"country"`
		Region      string  `json:"region"`
		City        string  `json:"city"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		Timezone    struct {
			ID string `json:"id"`
		} `json:"timezone"`
	}
	if err := locator.get(ctx, "https://ipwho.is/"+url.PathEscape(ip)+"?lang=zh-CN", &result); err != nil {
		return IPPlace{}, err
	}
	if !result.Success || result.IP != ip || len(result.CountryCode) != 2 {
		return IPPlace{}, errors.New("未取得这个 IP 的定位")
	}
	place := IPPlace{IP: ip, CountryCode: strings.ToUpper(result.CountryCode), Country: result.Country, Province: result.Region, City: result.City, Latitude: result.Latitude, Longitude: result.Longitude, Timezone: result.Timezone.ID}
	place.Region = RegionName(place)
	_, place.Supported = Environments[place.CountryCode]
	if locator.places == nil || len(locator.places) >= 512 {
		locator.places = map[string]placeCache{}
	}
	locator.places[key] = placeCache{place: place, expires: time.Now().Add(24 * time.Hour)}
	return place, nil
}

// RegionName is the 节点所在地区 value for a place: the city (without 市) on
// the mainland, falling back to the province; the short name for Hong Kong,
// Macao and Taiwan; the country's Chinese name elsewhere.
func RegionName(p IPPlace) string {
	trim := func(s string) string {
		for _, suffix := range []string{"特别行政区", "自治区", "市", "省"} {
			s = strings.TrimSuffix(s, suffix)
		}
		return strings.TrimSpace(s)
	}
	if p.CountryCode == "CN" {
		if city := trim(p.City); city != "" {
			return city
		}
		if province := trim(p.Province); province != "" {
			return province
		}
	}
	if short, ok := shortAreas[p.CountryCode]; ok {
		return short
	}
	if p.Country != "" {
		return p.Country
	}
	return p.CountryCode
}

type placeCache struct {
	place   IPPlace
	expires time.Time
}
