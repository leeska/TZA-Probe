package model

// The unlock catalogue is the fixed list of services a node's own egress is
// tested against. It lives in the model package because both sides need the
// same ids, display names and grouping: the Agent runs the check for an id and
// the panel renders the verdict under the same name and category. The check
// behaviour itself is not here — that is internal/unlock, which the Agent
// links in and the Server does not.

// Unlock categories, in panel order.
const (
	UnlockCategoryStream   = "流媒体"
	UnlockCategoryAI       = "AI"
	UnlockCategoryExchange = "交易所"
	UnlockCategoryMisc     = "其他"
)

// UnlockService is one catalogue entry.
type UnlockService struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Category groups the service in the panel: 流媒体 / AI / 交易所 / 其他.
	Category string `json:"category"`
	// RegionOnly marks services whose answer is informational — a region or a
	// currency, not an unlock verdict (Apple Region, Steam Currency, …). The
	// panel shows that text instead of a lock state, the way 妙妙屋 does, and
	// keeps the lock icon off it.
	RegionOnly bool `json:"region_only,omitempty"`
}

// UnlockCatalog is the whole list. Order matters: it is the order the panel
// lists the services in, grouped by category.
var UnlockCatalog = []UnlockService{
	{ID: "netflix", Name: "Netflix", Category: UnlockCategoryStream},
	{ID: "disneyplus", Name: "Disney+", Category: UnlockCategoryStream},
	{ID: "youtube_premium", Name: "YouTube Premium", Category: UnlockCategoryStream},
	{ID: "prime_video", Name: "Prime Video", Category: UnlockCategoryStream},
	{ID: "tvb_anywhere", Name: "TVB Anywhere+", Category: UnlockCategoryStream},
	{ID: "iqiyi", Name: "iQIYI 国际版", Category: UnlockCategoryStream},
	{ID: "dazn", Name: "DAZN", Category: UnlockCategoryStream},
	{ID: "netflix_cdn", Name: "Netflix CDN", Category: UnlockCategoryStream},
	{ID: "youtube_cdn", Name: "YouTube CDN", Category: UnlockCategoryStream},

	{ID: "openai", Name: "ChatGPT", Category: UnlockCategoryAI},
	{ID: "gemini", Name: "Gemini", Category: UnlockCategoryAI},
	{ID: "claude", Name: "Claude", Category: UnlockCategoryAI},

	{ID: "bybit", Name: "Bybit", Category: UnlockCategoryExchange},
	{ID: "binance", Name: "Binance", Category: UnlockCategoryExchange},
	{ID: "okx", Name: "OKX", Category: UnlockCategoryExchange},

	{ID: "bing", Name: "Bing Region", Category: UnlockCategoryMisc, RegionOnly: true},
	{ID: "apple", Name: "Apple Region", Category: UnlockCategoryMisc, RegionOnly: true},
	{ID: "google_play", Name: "Google Play", Category: UnlockCategoryMisc, RegionOnly: true},
	{ID: "google_search", Name: "Google Search", Category: UnlockCategoryMisc},
	{ID: "wikipedia", Name: "Wikipedia", Category: UnlockCategoryMisc},
	{ID: "reddit", Name: "Reddit", Category: UnlockCategoryMisc},
	{ID: "steam", Name: "Steam Currency", Category: UnlockCategoryMisc, RegionOnly: true},
	{ID: "onetrust", Name: "OneTrust Region", Category: UnlockCategoryMisc, RegionOnly: true},
	{ID: "sdggge", Name: "SD Gundam G Generation", Category: UnlockCategoryMisc},
}

// UnlockCategories is the category order the panel groups by.
var UnlockCategories = []string{UnlockCategoryStream, UnlockCategoryAI, UnlockCategoryExchange, UnlockCategoryMisc}

func KnownUnlockService(id string) bool {
	for _, service := range UnlockCatalog {
		if service.ID == id {
			return true
		}
	}
	return false
}

// UnlockServiceByID returns the catalogue entry, false when the id is unknown
// (an Agent newer than this Server).
func UnlockServiceByID(id string) (UnlockService, bool) {
	for _, service := range UnlockCatalog {
		if service.ID == id {
			return service, true
		}
	}
	return UnlockService{}, false
}

// UnlockServiceIDs is the id list in catalogue order, which is what an Agent
// iterates and what a test pins against the catalogue.
func UnlockServiceIDs() []string {
	out := make([]string, 0, len(UnlockCatalog))
	for _, service := range UnlockCatalog {
		out = append(out, service.ID)
	}
	return out
}
