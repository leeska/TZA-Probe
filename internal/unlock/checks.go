package unlock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/leeska/TZA-Probe/internal/model"
)

// check is one service's verdict function. It must never return a raw transport
// error as no/banned — use netErr for faults. Region and Detail are truncated
// by runOne, so a check may return long text safely.
type check func(ctx context.Context, c *Client) model.UnlockResult

// checks maps every catalogue id to its implementation. The catalogue lives in
// model; unlockTestCatalog pins the two together so a new service cannot ship
// without a check.
var checks = map[string]check{
	"netflix":         netflix,
	"netflix_cdn":     netflixCDN,
	"disneyplus":      disneyPlus,
	"youtube_premium": youtubePremium,
	"youtube_cdn":     youtubeCDN,
	"prime_video":     primeVideo,
	"tvb_anywhere":    tvbAnywhere,
	"iqiyi":           iqiyi,
	"dazn":            dazn,
	"openai":          openAI,
	"gemini":          gemini,
	"claude":          claude,
	"binance":         binance,
	"bybit":           bybit,
	"okx":             okx,
	"bing":            bing,
	"apple":           apple,
	"google_play":     googlePlay,
	"google_search":   googleSearch,
	"wikipedia":       wikipedia,
	"reddit":          reddit,
	"steam":           steam,
	"onetrust":        oneTrust,
	"sdggge":          sdggge,
}

// ---------------------------------------------------------------------------
// Streaming

// Netflix's licensed-title probes. Both are non-originals: a catalogue that
// carries them is a full catalogue, an egress Netflix only grants originals to
// renders the "not available in your country" notice in their place.
const (
	netflixTitleA = "https://www.netflix.com/title/81280792" // LEGO Ninjago
	netflixTitleB = "https://www.netflix.com/title/70143836" // Breaking Bad
)

// netflixNotice is the copy Netflix puts where a title it will not serve to
// this egress would be. Matching the phrase (rather than the leading "Oh no!")
// survives the apostrophe and wording of the headline.
const netflixNotice = "available to watch in your country"

func netflix(ctx context.Context, c *Client) model.UnlockResult {
	// Both URLs answer 200 whichever catalogue the egress gets, and the region
	// redirect only happens where a catalogue is served at all — so the status
	// code alone cannot tell a region page from the originals-only notice. The
	// body decides, exactly like the reference check (grep 'Oh no!').
	hdrs := map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	}
	type page struct {
		status int
		body   string
		path   string
	}
	fetch := func(raw string) (page, error) {
		r, err := c.do(ctx, true, "GET", raw, hdrs, nil)
		if err != nil {
			return page{}, err
		}
		body, rerr := read(r)
		if rerr != nil {
			return page{}, rerr
		}
		p := page{status: r.StatusCode, body: body}
		if r.Request != nil && r.Request.URL != nil {
			p.path = r.Request.URL.Path
		}
		return p, nil
	}
	p1, err := fetch(netflixTitleA)
	if err != nil {
		return netErr(err)
	}
	p2, err := fetch(netflixTitleB)
	if err != nil {
		return netErr(err)
	}
	switch {
	case p1.status == http.StatusForbidden && p2.status == http.StatusForbidden:
		return res(model.UnlockBanned, "", "HTTP 403")
	case p1.status == http.StatusNotFound && p2.status == http.StatusNotFound:
		// The older shape of the same answer: both licensed titles are gone
		// from this egress's catalogue.
		return res(model.UnlockOriginalsOnly, "", "Originals Only")
	case !okStatus(p1.status) && !okStatus(p2.status):
		return res(model.UnlockFailed, "", httpStatusDetail(p1.status))
	}
	// Originals only: Netflix withholds both licensed titles, which it renders
	// as the "not available in your country" notice in the title page.
	if netflixWithheld(p1.body) && netflixWithheld(p2.body) {
		return res(model.UnlockOriginalsOnly, "", "Originals Only")
	}
	region := netflixRegion(p1.body)
	if region == "" {
		region = netflixRegion(p2.body)
	}
	if region == "" {
		region = netflixRegionFromPath(p1.path)
	}
	if region == "" {
		region = netflixRegionFromPath(p2.path)
	}
	return res(model.UnlockYes, region, "")
}

// netflixWithheld reports whether the page is Netflix's "this title isn't
// available in your country" notice instead of the title itself.
func netflixWithheld(body string) bool {
	return strings.Contains(body, netflixNotice) || strings.Contains(body, "Oh no!")
}

// netflixRegion reads the region Netflix reports for the request itself, from
// the page's `"requestCountry":{…,"id":"DE","countryName":"Germany"}` block.
func netflixRegion(body string) string {
	i := strings.Index(body, `"requestCountry"`)
	if i < 0 {
		return ""
	}
	cc := strings.ToUpper(quotedAfter(body[i:], `"id":"`))
	if len(cc) != 2 || !isUpper(cc) {
		return ""
	}
	return cc
}

// netflixRegionFromPath reads the region off a region-prefixed path such as
// /de-en/title/81280792, the fallback when the page body carries no region.
func netflixRegionFromPath(path string) string {
	seg := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	if i := strings.IndexByte(seg, '-'); i < 0 {
		return ""
	} else {
		seg = seg[:i]
	}
	cc := strings.ToUpper(seg)
	if len(cc) != 2 || !isUpper(cc) {
		return ""
	}
	return cc
}

func netflixCDN(ctx context.Context, c *Client) model.UnlockResult {
	const target = "https://api.fast.com/netflix/speedtest/v2?https=true&token=YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm&urlCount=5"
	r, err := c.do(ctx, false, "GET", target, nil, nil)
	if err != nil {
		return netErr(err)
	}
	if r.StatusCode == http.StatusForbidden {
		drain(r)
		return res(model.UnlockBanned, "", "IP Banned By Netflix")
	}
	var v struct {
		Client struct {
			Location struct {
				City    string `json:"city"`
				Country string `json:"country"`
			} `json:"location"`
		} `json:"client"`
		Targets []struct {
			Location struct {
				City    string `json:"city"`
				Country string `json:"country"`
			} `json:"location"`
		} `json:"targets"`
	}
	rb, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	if err := json.Unmarshal([]byte(rb), &v); err != nil {
		return res(model.UnlockFailed, "", "bad speedtest payload")
	}
	// Prefer the egress's own location (what the panel wants: where this IP is
	// seen); fall back to the first chosen CDN node.
	loc := v.Client.Location
	if loc.City == "" && loc.Country == "" {
		if len(v.Targets) == 0 {
			return res(model.UnlockFailed, "", "no location")
		}
		loc = v.Targets[0].Location
	}
	region := loc.Country
	switch {
	case loc.City != "" && loc.Country != "":
		region = loc.City + ", " + loc.Country
	case loc.City != "":
		region = loc.City
	}
	if region == "" {
		return res(model.UnlockFailed, "", "no location")
	}
	return res(model.UnlockYes, region, "")
}

// Disney+ device-flow constants. The bearer is the public client credential the
// web app ships in its bundle (same for every client); the two bodies are the
// request templates the reference check keeps, with the per-round values
// (device assertion, refresh token) substituted in.
const (
	disneyDeviceBearer = "Bearer ZGlzbmV5JmJyb3dzZXImMS4wLjA.Cu56AgSfBTDag5NiRA81oLHkDZfu5L3CKadnefEAY84"

	disneyAssertionPlaceholder    = "DISNEYASSERTION"
	disneyRefreshTokenPlaceholder = "ILOVEDISNEY"

	disneyTokenBody = "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange&latitude=0&longitude=0&platform=browser&subject_token=DISNEYASSERTION&subject_token_type=urn%3Abamtech%3Aparams%3Aoauth%3Atoken-type%3Adevice"

	disneyRefreshBody = `{"query":"mutation refreshToken($input: RefreshTokenInput!) {\n            refreshToken(refreshToken: $input) {\n                activeSession {\n                    sessionId\n                }\n            }\n        }","variables":{"input":{"refreshToken":"ILOVEDISNEY"}}}`
)

func disneyPlus(ctx context.Context, c *Client) model.UnlockResult {
	// The marketing page answers 200 with a CDN's `region` header for every
	// egress (it read `us` for a Frankfurt box), so it cannot decide anything.
	// The verdict comes from Disney's own device flow, which is also what the
	// reference check drives: a device assertion, a token exchange that answers
	// `forbidden-location` when the IP is blocked, and a device session query
	// that names the catalogue this egress gets.
	auth := map[string]string{"Authorization": disneyDeviceBearer}
	post := func(follow bool, url, ctype, body string) (string, int, error) {
		h := map[string]string{"Content-Type": ctype}
		for k, v := range auth {
			h[k] = v
		}
		r, err := c.do(ctx, follow, "POST", url, h, strings.NewReader(body))
		if err != nil {
			return "", 0, err
		}
		rb, rerr := read(r)
		if rerr != nil {
			return "", r.StatusCode, rerr
		}
		return rb, r.StatusCode, nil
	}

	const devicesBody = `{"deviceFamily":"browser","applicationRuntime":"chrome","deviceProfile":"windows","attributes":{}}`
	dev, _, err := post(false, "https://disney.api.edge.bamgrid.com/devices", "application/json; charset=UTF-8", devicesBody)
	if err != nil {
		return netErr(err)
	}
	if strings.Contains(dev, "403 ERROR") {
		return res(model.UnlockBanned, "", "IP Banned By Disney+")
	}
	assertion := quotedAfter(dev, `"assertion":"`)
	if assertion == "" {
		return res(model.UnlockFailed, "", "no device assertion")
	}

	tok, _, err := post(false, "https://disney.api.edge.bamgrid.com/token",
		"application/x-www-form-urlencoded; charset=UTF-8",
		strings.Replace(disneyTokenBody, disneyAssertionPlaceholder, assertion, 1))
	if err != nil {
		return netErr(err)
	}
	if strings.Contains(tok, "forbidden-location") || strings.Contains(tok, "403 ERROR") {
		return res(model.UnlockBanned, "", "IP Banned By Disney+")
	}
	refresh := quotedAfter(tok, `"refresh_token":"`)
	if refresh == "" {
		return res(model.UnlockFailed, "", "no refresh token")
	}

	gql, _, err := post(true, "https://disney.api.edge.bamgrid.com/graph/v1/device/graphql", "application/json",
		strings.Replace(disneyRefreshBody, disneyRefreshTokenPlaceholder, refresh, 1))
	if err != nil {
		return netErr(err)
	}
	region := strings.ToUpper(quotedAfter(gql, `"countryCode":"`))
	supported := boolAfter(gql, `"inSupportedLocation":`)
	if region == "" {
		return res(model.UnlockNo, "", "")
	}
	if region == "JP" {
		return res(model.UnlockYes, "JP", "")
	}
	if supported == "false" {
		// The catalogue is announced for this region but not live yet.
		return res(model.UnlockNo, region, "not launched yet")
	}
	if supported != "true" {
		return res(model.UnlockFailed, "", "no location verdict")
	}
	// A preview/unavailable landing page overrides the region answer.
	r, err := c.do(ctx, true, "GET", "https://disneyplus.com", nil, nil)
	if err != nil {
		return netErr(err)
	}
	drain(r)
	if r.Request != nil && r.Request.URL != nil {
		final := r.Request.URL.String()
		if strings.Contains(final, "preview") || strings.Contains(final, "unavailable") {
			return res(model.UnlockNo, "", "")
		}
	}
	return res(model.UnlockYes, region, "")
}

func youtubePremium(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, true, "GET", "https://www.youtube.com/premium", nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	// GL is the catalogue this egress is served; countryCode elsewhere on the
	// page can name something else (a Hong Kong box reported US there).
	if cc := quotedAfter(page, `"GL":"`); len(cc) == 2 {
		return res(model.UnlockYes, strings.ToUpper(cc), "")
	}
	if cc := quotedAfter(page, `"countryCode":"`); len(cc) == 2 {
		return res(model.UnlockYes, strings.ToUpper(cc), "")
	}
	if strings.Contains(page, "premiumPurchaseButton") || strings.Contains(page, "manageSubscriptionButton") {
		return res(model.UnlockYes, "", "")
	}
	return res(model.UnlockNo, "", "")
}

func youtubeCDN(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, false, "GET", "https://redirector.googlevideo.com/report_mapping", nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	host := ""
	for _, line := range strings.Split(page, "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, "=> ")
		if i < 0 {
			continue
		}
		rest := line[i+3:]
		if j := strings.IndexAny(rest, " ("); j >= 0 {
			rest = rest[:j]
		}
		host = rest
		break
	}
	if host == "" {
		return res(model.UnlockFailed, "", "no mapping line")
	}
	// 机场码在主机名中间：实测东京那台回 `lcnrtb-bg`（nrt = 成田，-bg 只是
	// ISP 后缀），洛杉矶回 `lax31s13`。先按 "-" 切一刀会只看到后缀，把东京读成
	// 保加利亚 —— 所以整串里找已知机场码；认不出来就报失败，不猜一个地名。
	code := airCode(host)
	region := airCity(code)
	if region == "" {
		return res(model.UnlockFailed, "", "unknown aircode")
	}
	return res(model.UnlockYes, region, "")
}

func primeVideo(ctx context.Context, c *Client) model.UnlockResult {
	// Amazon hands a client that accepts only gzip a 143 KB document with no
	// territory marker, and the full 519 KB page (marker at 174 KB) to one that
	// accepts br or nothing: measured 0/3 marker with gzip against 3/3 with br
	// and 9/9 with identity, so ask for the uncompressed document.
	r, err := c.do(ctx, true, "GET", "https://www.primevideo.com", map[string]string{
		"Accept-Encoding": "identity",
	}, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	if cc := quotedAfter(page, `"currentTerritory":"`); len(cc) == 2 {
		return res(model.UnlockYes, strings.ToUpper(cc), "")
	}
	if cc := quotedAfter(page, `"countryCode":"`); len(cc) == 2 {
		return res(model.UnlockYes, strings.ToUpper(cc), "")
	}
	return res(model.UnlockNo, "", "")
}

func tvbAnywhere(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, false, "GET", "https://uapisfm.tvbanywhere.com.sg/geoip/check/platform/android", nil, nil)
	if err != nil {
		return netErr(err)
	}
	var v struct {
		Allow   bool   `json:"allow_in_this_country"`
		Country string `json:"country"`
	}
	rb, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	if err := json.Unmarshal([]byte(rb), &v); err != nil {
		return res(model.UnlockFailed, "", "bad geoip payload")
	}
	if v.Allow {
		return res(model.UnlockYes, strings.ToUpper(v.Country), "")
	}
	return res(model.UnlockNo, "", "")
}

func iqiyi(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, false, "GET", "https://www.iq.com", nil, nil)
	if err != nil {
		return netErr(err)
	}
	h := r.Header.Get("x-custom-client-ip")
	drain(r)
	if h == "" {
		return res(model.UnlockNo, "", "")
	}
	i := strings.Index(h, ":")
	if i < 0 {
		return res(model.UnlockNo, "", "")
	}
	region := h[i+1:]
	if region == "ntw" {
		region = "tw"
	}
	if region == "" {
		return res(model.UnlockNo, "", "")
	}
	return res(model.UnlockYes, strings.ToUpper(region), "")
}

func dazn(ctx context.Context, c *Client) model.UnlockResult {
	const payload = `{"LandingPageKey":"generic","Languages":"zh-CN,zh,en","Platform":"web","PlatformAttributes":{},"Manufacturer":"","PromoCode":"","Version":"2"}`
	hdrs := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json, text/plain, */*",
		"Origin":       "https://www.dazn.com",
		"Referer":      "https://www.dazn.com/",
	}
	r, err := c.do(ctx, true, "POST", "https://startup.core.indazn.com/misl/v5/Startup", hdrs, strings.NewReader(payload))
	if err != nil {
		return netErr(err)
	}
	body, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	var v struct {
		Region struct {
			IsAllowed         bool   `json:"IsAllowed"`
			DisallowedReason  string `json:"DisallowedReason"`
			GeolocatedCountry string `json:"GeolocatedCountry"`
		} `json:"Region"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		// A CDN block (CloudFront 403) is an outright refusal, not a fault.
		if r.StatusCode == http.StatusForbidden || r.StatusCode == http.StatusUnavailableForLegalReasons {
			return res(model.UnlockBanned, "", httpStatusDetail(r.StatusCode))
		}
		return res(model.UnlockFailed, "", httpStatusDetail(r.StatusCode))
	}
	if v.Region.IsAllowed {
		return res(model.UnlockYes, v.Region.GeolocatedCountry, "")
	}
	return res(model.UnlockBanned, "", v.Region.DisallowedReason)
}

// ---------------------------------------------------------------------------
// AI

func openAI(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, false, "GET", "https://chat.openai.com/cdn-cgi/trace", nil, nil)
	if err != nil {
		return netErr(err)
	}
	trace, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	loc := ""
	if i := strings.Index(trace, "loc="); i >= 0 {
		rest := trace[i+4:]
		if j := strings.IndexAny(rest, "\r\n"); j >= 0 {
			rest = rest[:j]
		}
		loc = strings.ToUpper(strings.TrimSpace(rest))
	}
	// No redirect following: a geo block answers 403 on the root itself.
	r2, err := c.do(ctx, false, "GET", "https://chat.openai.com", nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r2)
	if rerr != nil {
		return netErr(rerr)
	}
	if strings.Contains(page, "VPN") || r2.StatusCode == http.StatusForbidden {
		return res(model.UnlockBanned, "", "VPN Blocked")
	}
	if r2.StatusCode == http.StatusTooManyRequests {
		return res(model.UnlockOriginalsOnly, loc, "429 Rate limit")
	}
	if supportGPT(loc) {
		return res(model.UnlockYes, loc, "")
	}
	return res(model.UnlockNo, loc, "")
}

func gemini(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, true, "GET", "https://gemini.google.com", nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	// The app bundle carries the region as `,2,1,200,"XXX"`. Nothing else on
	// the page is an availability verdict we trust: the `45631641` flag the
	// reference check greps reads false on egresses the app still serves (a
	// browser in Frankfurt and in Hong Kong both render the full app), so it
	// is not a country gate and must not turn a working Gemini into "no".
	if cc := boundedAfter(page, `,2,1,200,"`, `"`); len(cc) == 3 && isUpper(cc) {
		return res(model.UnlockYes, cc, "")
	}
	if strings.Contains(page, "45631641,null,true") {
		return res(model.UnlockYes, "", "")
	}
	return res(model.UnlockNo, "", "")
}

// claude asks two of Anthropic's own gates. claude.ai sends a blocked region to
// anthropic.com/app-unavailable-in-region, but since 2026-10 Cloudflare answers
// every non-browser client there with a 403 challenge (all 14 production
// nodes, browser User-Agent or not), which says nothing about the IP. The API
// gate answers plainly: without a key, api.anthropic.com returns 401
// authentication_error where Claude is offered and 403 forbidden ("Request not
// allowed") where it is not. The region is Cloudflare's view of the egress
// (claude.ai/cdn-cgi/trace), the same geolocation Anthropic's edge uses.
func claude(ctx context.Context, c *Client) model.UnlockResult {
	loc := ""
	if t, err := c.do(ctx, false, "GET", "https://claude.ai/cdn-cgi/trace", nil, nil); err == nil {
		if trace, err := read(t); err == nil && t.StatusCode == http.StatusOK {
			loc = strings.ToUpper(traceValue(trace, "loc"))
		}
	}
	r, err := c.do(ctx, true, "GET", "https://claude.ai/", nil, nil)
	if err == nil {
		drain(r)
		final, path := "https://claude.ai/", "/"
		if r.Request != nil && r.Request.URL != nil {
			final, path = r.Request.URL.String(), r.Request.URL.Path
		}
		switch {
		case strings.Contains(final, "app-unavailable-in-region"):
			return res(model.UnlockNo, loc, "")
		case r.StatusCode == http.StatusOK && (path == "/" || strings.HasPrefix(path, "/login")):
			// The root or the sign-in page means the app answered.
			return res(model.UnlockYes, loc, "")
		}
	}
	// The app did not answer (Cloudflare's challenge, or a network error): the API gate decides.
	hdr := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	a, aerr := c.do(ctx, false, "POST", "https://api.anthropic.com/v1/messages", hdr, strings.NewReader("{}"))
	if aerr != nil {
		if err != nil {
			return netErr(err)
		}
		return netErr(aerr)
	}
	body, rerr := read(a)
	if rerr != nil {
		return netErr(rerr)
	}
	switch {
	case a.StatusCode == http.StatusUnauthorized || strings.Contains(body, "authentication_error"):
		return res(model.UnlockYes, loc, "")
	case a.StatusCode == http.StatusForbidden && (strings.Contains(body, "forbidden") || strings.Contains(body, "not allowed")):
		return res(model.UnlockNo, loc, "")
	case err != nil:
		return netErr(err)
	default:
		return res(model.UnlockFailed, loc, fmt.Sprintf("claude.ai HTTP %d / API HTTP %d", r.StatusCode, a.StatusCode))
	}
}

// ---------------------------------------------------------------------------
// Exchanges

func binance(ctx context.Context, c *Client) model.UnlockResult {
	// 网页站对受限地区也会回 202 挑战页，把「连得上」当解锁就全绿了；按地区真正回
	// 451 的是 API。实测（2026-10-02）：美国机器 api 451 / 网页 202，德国、日本两台
	// api 200 —— 与面板原来的口径一致。
	r, err := c.do(ctx, false, "GET", "https://api.binance.com/api/v3/ping", nil, nil)
	if err != nil {
		return netErr(err)
	}
	drain(r)
	api := r.StatusCode
	switch {
	case api == http.StatusUnavailableForLegalReasons:
		return res(model.UnlockNo, "", "HTTP 451")
	case api >= 200 && api < 300:
		return res(model.UnlockYes, "", "")
	}
	// API 因为别的原因不答时退回网页站：200/202 都说明这家站点没有按地区拦这台机器。
	page, err := c.do(ctx, false, "GET", "https://www.binance.com/", nil, nil)
	if err != nil {
		return netErr(err)
	}
	drain(page)
	if page.StatusCode == http.StatusUnavailableForLegalReasons {
		return res(model.UnlockNo, "", "HTTP 451")
	}
	if page.StatusCode >= 200 && page.StatusCode < 300 {
		return res(model.UnlockYes, "", "")
	}
	return res(model.UnlockFailed, "", httpStatusDetail(api))
}

func bybit(ctx context.Context, c *Client) model.UnlockResult {
	// 与币安同因：网页站哪都回 200，按国家拦的是 API 的 CDN（实测美国机器 403、
	// 德国 / 日本 200）。Detail 保留面板原来的说法。
	r, err := c.do(ctx, false, "GET", "https://api.bybit.com/v5/market/time", nil, nil)
	if err != nil {
		return netErr(err)
	}
	drain(r)
	api := r.StatusCode
	switch {
	case api == http.StatusForbidden:
		return res(model.UnlockNo, "", "CDN 按国家阻断")
	case api >= 200 && api < 300:
		return res(model.UnlockYes, "", "")
	}
	page, err := c.do(ctx, false, "GET", "https://www.bybit.com/", nil, nil)
	if err != nil {
		return netErr(err)
	}
	drain(page)
	if page.StatusCode == http.StatusForbidden {
		return res(model.UnlockNo, "", "CDN 按国家阻断")
	}
	if page.StatusCode >= 200 && page.StatusCode < 300 {
		return res(model.UnlockYes, "", "")
	}
	return res(model.UnlockFailed, "", httpStatusDetail(api))
}

func okx(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, true, "GET", "https://www.okx.com/", nil, nil)
	if err != nil {
		return netErr(err)
	}
	final := ""
	if r.Request != nil && r.Request.URL != nil {
		final = r.Request.URL.String()
	}
	drain(r)
	if r.StatusCode == http.StatusForbidden || r.StatusCode == http.StatusUnavailableForLegalReasons {
		return res(model.UnlockNo, "", httpStatusDetail(r.StatusCode))
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return res(model.UnlockFailed, "", httpStatusDetail(r.StatusCode))
	}
	// Region comes from the locale segment of the final URL (/en-us) or, when
	// that is absent, the Cloudflare trace.
	region := ""
	if i := strings.Index(final, "://"); i >= 0 {
		if k := strings.Index(final[i+3:], "/"); k >= 0 {
			seg := final[i+3+k+1:]
			if j := strings.IndexAny(seg, "/?"); j >= 0 {
				seg = seg[:j]
			}
			if parts := strings.Split(seg, "-"); len(parts) >= 2 {
				region = strings.ToUpper(parts[len(parts)-1])
			}
		}
	}
	if region == "" {
		if t, err := c.do(ctx, false, "GET", "https://www.okx.com/cdn-cgi/trace", nil, nil); err == nil {
			trace, rerr := read(t)
			if rerr == nil {
				if loc := traceValue(trace, "loc"); loc != "" {
					region = strings.ToUpper(loc)
				}
			}
		}
	}
	return res(model.UnlockYes, region, "")
}

// ---------------------------------------------------------------------------
// Misc / region lookups

func bing(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, true, "GET", "https://www.bing.com/search?q=curl", nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	if strings.Contains(page, "cn.bing.com") {
		return res(model.UnlockYes, "CN", "")
	}
	// 地区归地区、风险归 Detail：region 是地区类服务要显示的那段文字，把
	// "(Risky)" 拼进去会让面板上多出一个不存在的「地区」。
	region := boundedAfter(page, `Region:"`, `"`)
	if strings.Contains(page, `sj_cook.set("SRCHHPGUSR","HV"`) {
		return res(model.UnlockYes, region, "risky")
	}
	return res(model.UnlockYes, region, "")
}

func apple(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, true, "GET", "https://gspe1-ssl.ls.apple.com/pep/gcc", nil, nil)
	if err != nil {
		return netErr(err)
	}
	body, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	if len(body) >= 2 {
		body = body[:2]
	}
	if body == "" {
		return res(model.UnlockFailed, "", "empty region")
	}
	return res(model.UnlockYes, strings.ToUpper(body), "")
}

func googlePlay(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, true, "GET", "https://play.google.com/", nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	const marker = `<div class="yVZQTb">`
	i := strings.Index(page, marker)
	if i < 0 {
		return res(model.UnlockFailed, "", "page error")
	}
	seg := page[i+len(marker):]
	if j := strings.IndexAny(seg, "<("); j >= 0 {
		seg = seg[:j]
	}
	region := strings.TrimSpace(seg)
	if region == "" {
		return res(model.UnlockFailed, "", "page error")
	}
	return res(model.UnlockYes, region, "")
}

func googleSearch(ctx context.Context, c *Client) model.UnlockResult {
	const target = "https://www.google.com/search?q=curl&oq=curl&sourceid=chrome&ie=UTF-8"
	r, err := c.do(ctx, true, "GET", target, nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	blocked := strings.Contains(page, "unusual traffic from") ||
		strings.Contains(page, "is blocked") ||
		strings.Contains(page, "unaddressed abuse")
	hasResults := strings.Contains(page, "curl")
	switch {
	case blocked:
		return res(model.UnlockNo, "", "")
	case hasResults:
		return res(model.UnlockYes, "", "")
	default:
		return res(model.UnlockFailed, "", "page error")
	}
}

func wikipedia(ctx context.Context, c *Client) model.UnlockResult {
	const target = "https://zh.wikipedia.org/w/index.php?title=Wikipedia%3A%E6%B2%99%E7%9B%92&action=edit"
	r, err := c.do(ctx, true, "GET", target, nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	if strings.Contains(page, "banned") {
		return res(model.UnlockNo, "", "")
	}
	return res(model.UnlockYes, "", "")
}

func reddit(ctx context.Context, c *Client) model.UnlockResult {
	if c.family == "ipv6" {
		return res(model.UnlockFailed, "", "IPv6 unsupported")
	}
	// HTTP/1.1 on purpose (see doPlain): over h2 Reddit's edge answers this
	// client with its own 403 block page even from an IP it serves fine. The
	// Accept header asks for the document; the site sends its block page either
	// way.
	r, err := c.doPlain(ctx, "GET", "https://www.reddit.com/", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	}, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	switch r.StatusCode {
	case http.StatusOK:
		return res(model.UnlockYes, "", "")
	case http.StatusForbidden:
		// Only its own refusal copy makes this a verdict about the IP; a bare
		// 403 (the shape a non-browser client gets) is a failure to judge.
		if redditBlocked(page) {
			return res(model.UnlockNo, "", "")
		}
		return res(model.UnlockFailed, "", "HTTP 403")
	default:
		return res(model.UnlockFailed, "", httpStatusDetail(r.StatusCode))
	}
}

// redditBlocked recognises Reddit's own refusal pages: the network-security
// block (the www shell says "You've been blocked by network security."; the
// 1.5 KB one is titled Blocked) and the rate-limit notice.
func redditBlocked(page string) bool {
	l := strings.ToLower(page)
	return strings.Contains(l, "blocked by network security") ||
		strings.Contains(l, "whoa there") ||
		strings.Contains(l, "blocked due to a network policy")
}

func steam(ctx context.Context, c *Client) model.UnlockResult {
	r, err := c.do(ctx, true, "GET", "https://store.steampowered.com/app/761830", nil, nil)
	if err != nil {
		return netErr(err)
	}
	page, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	i := strings.Index(page, "priceCurrency")
	if i < 0 {
		return res(model.UnlockFailed, "", "page error")
	}
	seg := page[i:]
	j := strings.Index(seg, `content="`)
	if j < 0 {
		return res(model.UnlockFailed, "", "page error")
	}
	seg = seg[j+len(`content="`):]
	k := strings.IndexByte(seg, '"')
	if k < 0 {
		return res(model.UnlockFailed, "", "page error")
	}
	region := seg[:k]
	if region == "" {
		return res(model.UnlockFailed, "", "page error")
	}
	return res(model.UnlockYes, region, "")
}

func oneTrust(ctx context.Context, c *Client) model.UnlockResult {
	const target = "https://geolocation.onetrust.com/cookieconsentpub/v1/geo/location"
	r, err := c.do(ctx, false, "GET", target, nil, nil)
	if err != nil {
		return netErr(err)
	}
	body, rerr := read(r)
	if rerr != nil {
		return netErr(rerr)
	}
	// The body is a JSONP call: jsonFeed({...});
	if i := strings.IndexByte(body, '{'); i >= 0 {
		if j := strings.LastIndexByte(body, '}'); j > i {
			body = body[i : j+1]
		}
	}
	var v struct {
		Country   string `json:"country"`
		StateName string `json:"stateName"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.Country == "" {
		return res(model.UnlockFailed, "", "page error")
	}
	region := v.Country
	if v.StateName != "" {
		region = v.Country + " [" + v.StateName + "]"
	}
	return res(model.UnlockYes, region, "")
}

// sdGGGEPayload is the fixed protobuf consent probe: a base64 body replayed to
// the game's consent endpoint. Only a literal 200 is a pass and 483 the game's
// own "region refused"; anything else is a failure, never a guess.
const sdGGGEPayload = "1CR6PntuLeI3yaCYAZdOPxn18bOFYJxUiYtcavqqAHDCjc3C/wozplHYwfhykUStp7Bb/LAhV8aWQkS9sLliHCIgXBvDsWe4pwXvV3cSXkoaBfL23/zytEHlAatOi/32UVYLJhyUsegCRMMGREr2fXqyx970imQ35hqWVj/MRTHS9Bi8iqo9nIqSDTcQqVn3BbuyhJcz52nhfSda2may3QVHkH9QDdFjW9S/2re2cxE3iaE/DUbjB9H8KUpihQB1Emf88I0241ea7CAI1jHel6aZ5Ul4XjTf8ug3Rl/T80A="

func sdggge(ctx context.Context, c *Client) model.UnlockResult {
	raw, err := base64.StdEncoding.DecodeString(sdGGGEPayload)
	if err != nil {
		return res(model.UnlockFailed, "", "bad payload")
	}
	hdrs := map[string]string{
		"Content-Type":           "application/protobuf",
		"Accept":                 "application/protobuf",
		"X-Content-Is-Encrypted": "True",
		"X-Language":             "hk",
		"X-Unity-Version":        "2022.3.45f1",
		"X-Master-Url":           "https://clientdata.gl.eternal.channel.or.jp/prd-gl/catalogs/hr0phpfWDVahMJGQIk2OSd6hy35YpQZVKYAo6lKeld-9scMGJw2KTnBDGbS04Gw-i25avFTH55K-yU9TCX2OkQ.json",
		"X-Language-Master-Url":  "https://clientdata.gl.eternal.channel.or.jp/prd-gl/language_catalogs/hk/F-HORjFKHLai8nLXUdPyQRqzexZNPKIn2O36Hgd2Bxm2RysBNS0-PQHQwfHXEOONog0w5yULtewBaVk-Ndf6nQ.json",
		"X-App-Version-Hash":     "20928",
		"X-Token":                "e5df59f1-8588-4477-a887-5fe854895493Mj0jmtfbgIhQOUmHQE1W7sLq7G5eSBqcFWqldSPjy6s=",
		"Accept-Language":        "zh-CN,zh-Hans;q=0.9",
		"User-Agent":             "GETERNAL/25041500 CFNetwork/3826.400.120 Darwin/24.3.0",
	}
	const target = "https://api.gl.eternal.channel.or.jp/api/pvt/consent/view?user_id=649635267711712178"
	r, err := c.do(ctx, false, "POST", target, hdrs, bytes.NewReader(raw))
	if err != nil {
		return netErr(err)
	}
	drain(r)
	switch r.StatusCode {
	case http.StatusOK:
		return res(model.UnlockYes, "", "")
	case 483:
		return res(model.UnlockNo, "", "")
	default:
		return res(model.UnlockFailed, "", httpStatusDetail(r.StatusCode))
	}
}

// ---------------------------------------------------------------------------
// Small helpers shared by the checks above.

func httpStatusDetail(code int) string {
	return "HTTP " + itoa(code)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// boundedAfter returns the text between start and end markers, or "".
func boundedAfter(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// traceValue reads a `key=value` line from a cdn-cgi/trace body.
func traceValue(trace, key string) string {
	for _, line := range strings.Split(trace, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(line[len(key)+1:])
		}
	}
	return ""
}

func cookieValue(r *http.Response, name string) string {
	for _, ck := range r.Cookies() {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

func isUpper(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return s != ""
}

// supportGPT is the set of countries ChatGPT serves, used only after a 200 root
// response (a 403 or 429 is decided before this is consulted).
func supportGPT(loc string) bool {
	for _, s := range gptSupported {
		if loc == s {
			return true
		}
	}
	return false
}

var gptSupported = []string{
	"AL", "DZ", "AD", "AO", "AG", "AR", "AM", "AU", "AT", "AZ", "BS", "BD", "BB", "BE", "BZ", "BJ", "BT", "BA", "BW", "BR", "BG", "BF", "CV", "CA", "CL", "CO", "KM", "CR", "HR", "CY", "DK", "DJ", "DM", "DO", "EC", "SV", "EE", "FJ", "FI", "FR", "GA", "GM", "GE", "DE", "GH", "GR", "GD", "GT", "GN", "GW", "GY", "HT", "HN", "HU", "IS", "IN", "ID", "IQ", "IE", "IL", "IT", "JM", "JP", "JO", "KZ", "KE", "KI", "KW", "KG", "LV", "LB", "LS", "LR", "LI", "LT", "LU", "MG", "MW", "MY", "MV", "ML", "MT", "MH", "MR", "MU", "MX", "MC", "MN", "ME", "MA", "MZ", "MM", "NA", "NR", "NP", "NL", "NZ", "NI", "NE", "NG", "MK", "NO", "OM", "PK", "PW", "PA", "PG", "PE", "PH", "PL", "PT", "QA", "RO", "RW", "KN", "LC", "VC", "WS", "SM", "ST", "SN", "RS", "SC", "SL", "SG", "SK", "SI", "SB", "ZA", "ES", "LK", "SR", "SE", "CH", "TH", "TG", "TO", "TT", "TN", "TR", "TV", "UG", "AE", "US", "UY", "VU", "ZM", "BO", "BN", "CG", "CZ", "VA", "FM", "MD", "PS", "KR", "TW", "TZ", "TL", "GB",
}

// airCode mimics RegionRestrictionCheck's findAirCode: take the alphabetic
// prefix before the first digit, upper-case it, and keep the first known IATA
// code it contains.
func airCode(code string) string {
	i := 0
	for ; i < len(code); i++ {
		if code[i] >= '0' && code[i] <= '9' {
			break
		}
	}
	upper := strings.ToUpper(code[:i])
	for _, want := range airCodes {
		if strings.Contains(upper, want) {
			return want
		}
	}
	return upper
}

// airCity maps the codes airCode can return to a human "City, Country". An
// unknown code falls back to the raw code so the panel still shows something.
func airCity(code string) string {
	return airCities[code]
}

var airCodes = []string{
	"AMS", "ATL", "AUS", "BCN", "BER", "BNA", "BOM", "BOS", "BRU", "BUE", "CDG", "CLT", "CPH",
	"DEL", "DEN", "DFW", "DTM", "DTW", "DUB", "DUS", "EDI", "EWR", "FRA", "FUK", "GMP", "GRU",
	"HAM", "HKG", "HND", "IAD", "IAH", "ICN", "JFK", "KIX", "LAS", "LAX", "LHR", "MAD", "MAN",
	"MCO", "MEL", "MEX", "MIA", "MSP", "MUC", "NGO", "NRT", "ORD", "ORY", "PDX", "PHL", "PHX",
	"RDU", "SAN", "SEA", "SFO", "SIN", "SJC", "SLC", "STL", "STR", "SYD", "TPA", "TPE", "YUL",
	"YVR", "YYZ", "ZRH",
}

var airCities = map[string]string{
	"AMS": "Amsterdam, Netherlands", "ATL": "Atlanta, United States",
	"AUS": "Austin, United States", "BCN": "Barcelona, Spain",
	"BER": "Berlin, Germany", "BNA": "Nashville, United States",
	"BOM": "Mumbai, India", "BOS": "Boston, United States",
	"BRU": "Brussels, Belgium", "BUE": "Buenos Aires, Argentina",
	"CDG": "Paris, France", "CLT": "Charlotte, United States",
	"CPH": "Copenhagen, Denmark", "DEL": "Delhi, India",
	"DEN": "Denver, United States", "DFW": "Dallas, United States",
	"DTM": "Dortmund, Germany", "DTW": "Detroit, United States",
	"DUB": "Dublin, Ireland", "DUS": "Dusseldorf, Germany",
	"EDI": "Edinburgh, United Kingdom", "EWR": "Newark, United States",
	"FRA": "Frankfurt, Germany", "FUK": "Fukuoka, Japan",
	"GMP": "Seoul, South Korea", "GRU": "Sao Paulo, Brazil",
	"HAM": "Hamburg, Germany", "HKG": "Hong Kong, China",
	"HND": "Tokyo, Japan", "IAD": "Washington, United States",
	"IAH": "Houston, United States", "ICN": "Seoul, South Korea",
	"JFK": "New York, United States", "KIX": "Osaka, Japan",
	"LAS": "Las Vegas, United States", "LAX": "Los Angeles, United States",
	"LHR": "London, United Kingdom", "MAD": "Madrid, Spain",
	"MAN": "Manchester, United Kingdom", "MCO": "Orlando, United States",
	"MEL": "Melbourne, Australia", "MEX": "Mexico City, Mexico",
	"MIA": "Miami, United States", "MSP": "Minneapolis, United States",
	"MUC": "Munich, Germany", "NGO": "Nagoya, Japan",
	"NRT": "Tokyo, Japan", "ORD": "Chicago, United States",
	"ORY": "Paris, France", "PDX": "Portland, United States",
	"PHL": "Philadelphia, United States", "PHX": "Phoenix, United States",
	"RDU": "Raleigh, United States", "SAN": "San Diego, United States",
	"SEA": "Seattle, United States", "SFO": "San Francisco, United States",
	"SIN": "Singapore, Singapore", "SJC": "San Jose, United States",
	"SLC": "Salt Lake City, United States", "STL": "St. Louis, United States",
	"STR": "Stuttgart, Germany", "SYD": "Sydney, Australia",
	"TPA": "Tampa, United States", "TPE": "Taipei, Taiwan",
	"YUL": "Montreal, Canada", "YVR": "Vancouver, Canada",
	"YYZ": "Toronto, Canada", "ZRH": "Zurich, Switzerland",
}
