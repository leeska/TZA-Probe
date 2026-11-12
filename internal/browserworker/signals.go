package browserworker

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Region is what region.js reports: the region codes whose localized names
// exactly match a visible label of the given source, and the labels matched.
type Region struct {
	Source     string   `json:"source"`
	Candidates []string `json:"candidates"`
	Labels     []string `json:"labels"`
}

// Signal is one page-region reading. Status is observed, conflict or unknown.
type Signal struct {
	Metric       string
	Value        string
	Status       string
	NeutralEntry bool
}

// Extraction is the reading of one rendered page, the Go form of the Python
// worker's adapters.extract_signals for the three adapters the Server uses.
type Extraction struct {
	Recognized bool
	Challenge  bool
	Signal     Signal
}

var (
	searchForm   = regexp.MustCompile(`<form\b[^>]*action=["'][^"']*/search`)
	sorryForm    = regexp.MustCompile(`(?i)<form\b[^>]*action=["'][^"']*/sorry/`)
	regionCode   = regexp.MustCompile(`^[A-Z]{2}$`)
	metaDefault  = regexp.MustCompile(`"defaultCountry"\s*:\s*"([A-Z]{2})"`)
	metaCountry  = regexp.MustCompile(`"countryCode"\s*:\s*"([A-Z]{2})"`)
	metaQuery    = regexp.MustCompile(`(?:^|&)country=([A-Z]{2})(?:&|$)`)
	metaBlocked  = []string{"you're temporarily blocked", "you’re temporarily blocked", "confirm you're not a robot", "confirm you’re not a robot"}
	targetParams = []string{"gl", "cr", "uule", "country", "region"}
)

// origin is scheme://host[:port], empty for anything that is not a plain
// HTTP(S) URL (credentials, fragments, bad ports) — the worker's origins.origin.
func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		return u.Scheme + "://" + host + ":" + port
	}
	return u.Scheme + "://" + host
}

// queryKeys are the keys Python's parse_qs keeps: blank values are dropped.
func queryKeys(raw string) map[string][]string {
	values, _ := url.ParseQuery(raw)
	kept := map[string][]string{}
	for key, list := range values {
		for _, value := range list {
			if value != "" {
				kept[key] = append(kept[key], value)
			}
		}
	}
	return kept
}

func codeSet(region Region, source string) (map[string]bool, bool) {
	codes := map[string]bool{}
	if region.Source != source {
		return codes, true
	}
	valid := true
	for _, code := range region.Candidates {
		codes[code] = true
		valid = valid && regionCode.MatchString(code)
	}
	return codes, valid
}

func only(codes map[string]bool) string {
	for code := range codes {
		return code
	}
	return ""
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}

// Extract reads one page. body is document.body.innerText, markup the serialized
// document, finalURL the page's current address and requested what was navigated to.
func Extract(adapter, body, markup, title, finalURL, requested string, region Region) Extraction {
	final, _ := url.Parse(finalURL)
	asked, _ := url.Parse(requested)
	if final == nil {
		final = &url.URL{}
	}
	if asked == nil {
		asked = &url.URL{}
	}
	finalOrigin := origin(finalURL)
	var out Extraction
	switch adapter {
	case "google_home":
		home := (asked.Path == "" || asked.Path == "/") && asked.RawQuery == ""
		search := false
		if asked.Path == "/search" {
			all, _ := url.ParseQuery(asked.RawQuery)
			search = len(all) == 1 && len(all["q"]) == 1 && all["q"][0] == "Google"
		}
		neutral := asked.Scheme == "https" && asked.Host == "www.google.com" && (home || search) && asked.Fragment == ""
		out.Recognized = strings.Contains(strings.ToLower(strings.TrimSpace(title)), "google") && searchForm.MatchString(markup)
		page := (finalOrigin == "https://www.google.com" || finalOrigin == "https://www.google.com.hk") && (final.Path == "" || final.Path == "/" || final.Path == "/search")
		if page {
			keys := queryKeys(final.RawQuery)
			for _, param := range targetParams {
				if _, ok := keys[param]; ok {
					page = false
				}
			}
		}
		codes, valid := codeSet(region, "rendered_footer")
		observed := out.Recognized && page && neutral && valid && len(codes) == 1
		out.Signal = Signal{Metric: "google_page_country", Status: "unknown", NeutralEntry: neutral}
		if observed {
			out.Signal.Status, out.Signal.Value = "observed", only(codes)
		} else if page && len(codes) > 1 {
			out.Signal.Status = "conflict"
		}
		out.Challenge = strings.Contains(strings.ToLower(final.Path), "/sorry/") || sorryForm.MatchString(markup) || strings.Contains(strings.ToLower(body), "unusual traffic from your computer network")
	case "google_terms":
		neutral := asked.Scheme == "https" && asked.Host == "policies.google.com" && asked.Path == "/terms" && asked.RawQuery == "" && asked.Fragment == ""
		page := finalOrigin == "https://policies.google.com" && final.Path == "/terms" && final.RawQuery == "" && final.Fragment == ""
		codes, valid := codeSet(region, "rendered_terms_country")
		out.Recognized = page && neutral && valid && len(codes) == 1 && strings.TrimSpace(body) != ""
		out.Signal = Signal{Metric: "google_terms_country", Status: "unknown", NeutralEntry: neutral}
		if out.Recognized {
			out.Signal.Status, out.Signal.Value = "observed", only(codes)
		}
		out.Challenge = strings.HasPrefix(final.Path, "/sorry/") || strings.HasPrefix(final.Path, "/challenge/")
	case "meta_ads_library":
		neutral := asked.Scheme == "https" && asked.Host == "www.facebook.com" && asked.Path == "/ads/library/" && asked.RawQuery == "" && asked.Fragment == ""
		codes := map[string]bool{}
		for _, match := range metaDefault.FindAllStringSubmatch(markup, -1) {
			codes[match[1]] = true
		}
		for _, marker := range []string{`"AdsCurrentUser"`, `"AdsCurrentUserGrid"`} {
			if position := strings.Index(markup, marker); position >= 0 {
				end := min(position+2000, len(markup))
				for _, match := range metaCountry.FindAllStringSubmatch(markup[position:end], -1) {
					codes[match[1]] = true
				}
			}
		}
		for _, match := range metaQuery.FindAllStringSubmatch(final.RawQuery, -1) {
			codes[match[1]] = true
		}
		rendered, _ := codeSet(region, "rendered_country_selector")
		page := finalOrigin == "https://www.facebook.com" && strings.TrimRight(final.Path, "/") == "/ads/library"
		out.Recognized = page && len(codes) == 1 && sameSet(rendered, codes)
		union := map[string]bool{}
		for code := range codes {
			union[code] = true
		}
		for code := range rendered {
			union[code] = true
		}
		out.Signal = Signal{Metric: "ads_library_country", Status: "unknown", NeutralEntry: neutral}
		if out.Recognized {
			out.Signal.Value = only(codes)
			if neutral {
				out.Signal.Status = "observed"
			}
		} else if page && len(union) > 1 {
			out.Signal.Status = "conflict"
		}
		lower := strings.ToLower(body)
		out.Challenge = strings.HasPrefix(final.Path, "/checkpoint/") || strings.HasPrefix(final.Path, "/challenge/")
		for _, phrase := range metaBlocked {
			out.Challenge = out.Challenge || strings.Contains(lower, phrase)
		}
	}
	if out.Challenge && adapter != "google_terms" {
		out.Recognized = false
		out.Signal.Status = "unknown"
	}
	if out.Signal.Status != "observed" {
		out.Signal.Value = keepValue(adapter, out)
	}
	return out
}

// keepValue: Python leaves the value None unless the signal was observed, except
// that a recognized Meta page outside the neutral entry (the experiment URL) still
// carries the country it shows — observe_service only reports observed values,
// so that value never reaches the Server, but tests can see it.
func keepValue(adapter string, out Extraction) string {
	if adapter == "meta_ads_library" && out.Recognized {
		return out.Signal.Value
	}
	return ""
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
