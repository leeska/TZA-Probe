package browserworker

import "testing"

// These cases are the Python worker's tests/test_adapters.py, one for one, so
// the Go port reads pages exactly as the worker it replaces did.

const neutralTerms = "https://policies.google.com/terms"

func terms(candidates ...string) Region {
	return Region{Source: "rendered_terms_country", Candidates: candidates}
}
func footer(candidates ...string) Region {
	return Region{Source: "rendered_footer", Candidates: candidates}
}
func selector(candidates ...string) Region {
	return Region{Source: "rendered_country_selector", Candidates: candidates}
}

func TestTermsCountryRequiresNeutralOriginVisibleEvidenceAndNoCountryQuery(t *testing.T) {
	for _, destination := range []string{neutralTerms, neutralTerms + "?gl=US", neutralTerms + "?hl=en", "https://policies.google.com/terms/archive", "https://www.google.com/terms"} {
		got := Extract("google_terms", "Country version: United States", "", "Terms", destination, neutralTerms, terms("US"))
		if got.Recognized != (destination == neutralTerms) {
			t.Errorf("final %s: recognized=%v", destination, got.Recognized)
		}
	}
	for _, requested := range []string{neutralTerms + "?gl=US", neutralTerms + "#US"} {
		if Extract("google_terms", "United States", "", "", neutralTerms, requested, terms("US")).Recognized {
			t.Errorf("requested %s recognized", requested)
		}
	}
	for _, candidates := range [][]string{{}, {"US", "JP"}, {"us"}} {
		if Extract("google_terms", "United States", "", "", neutralTerms, neutralTerms, terms(candidates...)).Recognized {
			t.Errorf("candidates %v recognized", candidates)
		}
	}
	if got := Extract("google_terms", "Country version: United States", "", "Terms", neutralTerms, neutralTerms, terms("US")); got.Signal.Status != "observed" || got.Signal.Value != "US" {
		t.Errorf("neutral terms page = %+v", got.Signal)
	}
}

const searchMarkup = `<form action="/search"></form>`

func TestGoogleNeutralObservationRequiresActualCountry(t *testing.T) {
	got := Extract("google_home", "Search", searchMarkup, "Google", "https://www.google.com/", "https://www.google.com/", Region{})
	if !got.Recognized || got.Signal.Status != "unknown" || got.Signal.Value != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestGoogleExperimentDoesNotCountAsNeutralRecovery(t *testing.T) {
	got := Extract("google_home", "Search", searchMarkup, "Google", "https://www.google.com/", "https://www.google.com/ncr", footer("US"))
	if got.Signal.Status != "unknown" || got.Signal.NeutralEntry {
		t.Fatalf("got %+v", got.Signal)
	}
}

func TestGoogleCountryIsNotInferredFromRedirectOrLanguage(t *testing.T) {
	for _, destination := range []string{"https://www.google.com/", "https://www.google.com.hk/"} {
		for _, language := range []string{"zh-CN", "ja", "en-US"} {
			markup := `<html lang="` + language + `"><form action="/search"></form></html>`
			got := Extract("google_home", "Search", markup, "Google", destination, "https://www.google.com/", Region{Source: "rendered_footer", Candidates: []string{"DE"}, Labels: []string{"Deutschland"}})
			if got.Signal.Value != "DE" || got.Signal.Status != "observed" {
				t.Errorf("%s %s: %+v", destination, language, got.Signal)
			}
		}
	}
	got := Extract("google_home", "Search", searchMarkup, "Google", "https://www.google.com/", "https://www.google.com/", footer("JP", "US"))
	if got.Signal.Status != "conflict" || got.Signal.Value != "" {
		t.Errorf("two countries: %+v", got.Signal)
	}
}

func TestGoogleNeutralSearchCountryAndForcedQueryAreDistinct(t *testing.T) {
	markup := `<title>Google - Google Search</title><form action="/search"></form>`
	region := Region{Source: "rendered_footer", Candidates: []string{"US"}, Labels: []string{"United States"}}
	got := Extract("google_home", "United States", markup, "Google - Google Search", "https://www.google.com/search?q=Google&sei=server", "https://www.google.com/search?q=Google", region)
	if got.Signal.Status != "observed" || got.Signal.Value != "US" {
		t.Errorf("neutral search: %+v", got.Signal)
	}
	for _, query := range []string{"q=Google&gl=US", "q=Google&uule=forced", "q=Google&country=US"} {
		got := Extract("google_home", "United States", markup, "Google - Google Search", "https://www.google.com/search?"+query, "https://www.google.com/search?"+query, region)
		if got.Signal.Status != "unknown" {
			t.Errorf("%s: %+v", query, got.Signal)
		}
	}
}

func TestGoogleChallengeIsNotAValidNormalPage(t *testing.T) {
	got := Extract("google_home", "unusual traffic from your computer network", `<form action="/sorry/index"></form>`, "Sorry", "https://www.google.com/sorry/", "https://www.google.com/", Region{})
	if !got.Challenge || got.Recognized {
		t.Fatalf("got %+v", got)
	}
	// The /sorry/ page reached by Google's own re-navigation is a challenge too.
	got = Extract("google_home", "", "<html></html>", "https://www.google.com/search?q=Google", "https://www.google.com/sorry/index?continue=x", "https://www.google.com/search?q=Google", Region{})
	if !got.Challenge {
		t.Fatal("sorry page not detected")
	}
}

func TestMetaCountryQueryDoesNotEstablishCountry(t *testing.T) {
	got := Extract("meta_ads_library", "Ad Library", `{"defaultCountry":"US"}`, "Ad Library", "https://www.facebook.com/ads/library/?country=US", "https://www.facebook.com/ads/library/?country=US", selector("US"))
	if !got.Recognized || got.Signal.Status == "observed" {
		t.Fatalf("got %+v", got)
	}
}

func TestMetaCleanPageAndConflictingEvidence(t *testing.T) {
	for markup, want := range map[string]string{
		`{"defaultCountry":"US"}`: "observed",
		`{"defaultCountry":"US","AdsCurrentUser":{"countryCode":"CN"}}`: "conflict",
	} {
		got := Extract("meta_ads_library", "Ad Library", markup, "Ad Library", "https://www.facebook.com/ads/library/", "https://www.facebook.com/ads/library/", selector("US"))
		if got.Signal.Status != want {
			t.Errorf("%s: %s, want %s", markup, got.Signal.Status, want)
		}
	}
}

func TestMetaLocalizedPageRequiresVisibleCountryAndConsistentEvidence(t *testing.T) {
	for _, title := range []string{"広告ライブラリ", "广告资料库", "Werbebibliothek", "Ad Library"} {
		for _, c := range []struct {
			visible []string
			want    string
		}{{[]string{"JP"}, "observed"}, {nil, "unknown"}, {[]string{"US"}, "conflict"}, {[]string{"JP", "US"}, "conflict"}} {
			got := Extract("meta_ads_library", "content", `{"defaultCountry":"JP"}`, title, "https://www.facebook.com/ads/library/?country=JP", "https://www.facebook.com/ads/library/", selector(c.visible...))
			if got.Signal.Status != c.want || got.Recognized != (c.want == "observed") {
				t.Errorf("%s %v: %+v recognized=%v, want %s", title, c.visible, got.Signal, got.Recognized, c.want)
			}
		}
	}
}

func TestMetaBlockedPageWithCountryHintIsNotNormal(t *testing.T) {
	got := Extract("meta_ads_library", "You’re temporarily blocked", `{"defaultCountry":"US"}`, "Ad Library", "https://www.facebook.com/ads/library/", "https://www.facebook.com/ads/library/", selector("US"))
	if !got.Challenge || got.Recognized || got.Signal.Status == "observed" {
		t.Fatalf("got %+v", got)
	}
}

func TestRemovedAdaptersCannotProduceEvidence(t *testing.T) {
	for _, adapter := range []string{"youtube_region", "cloudflare_trace", "meta_legal", "generic"} {
		got := Extract(adapter, "", "", "", "https://www.google.com/", "https://www.google.com/", Region{})
		if got.Recognized || got.Signal.Metric != "" {
			t.Errorf("%s: %+v", adapter, got)
		}
	}
}

func TestOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://WWW.Google.com/search?q=x": "https://www.google.com",
		"https://www.google.com:443/":       "https://www.google.com",
		"https://www.google.com:8443/":      "https://www.google.com:8443",
		"http://example.com:80/":            "http://example.com",
		"https://user:pw@www.google.com/":   "",
		"https://www.google.com/#fragment":  "",
		"chrome-error://chromewebdata/":     "",
		"https://[2001:db8::1]/":            "https://[2001:db8::1]",
	} {
		if got := origin(raw); got != want {
			t.Errorf("origin(%q) = %q, want %q", raw, got, want)
		}
	}
}
