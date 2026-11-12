package ipguard

type Environment struct {
	Locale   string `json:"locale"`
	Timezone string `json:"timezone"`
}

var Environments = map[string]Environment{
	"US": {"en-US", "America/New_York"}, "CA": {"en-CA", "America/Toronto"},
	"GB": {"en-GB", "Europe/London"}, "DE": {"de-DE", "Europe/Berlin"},
	"FR": {"fr-FR", "Europe/Paris"}, "NL": {"nl-NL", "Europe/Amsterdam"},
	"SE": {"sv-SE", "Europe/Stockholm"}, "NO": {"nb-NO", "Europe/Oslo"},
	"FI": {"fi-FI", "Europe/Helsinki"}, "CH": {"de-CH", "Europe/Zurich"},
	"AT": {"de-AT", "Europe/Vienna"}, "IT": {"it-IT", "Europe/Rome"},
	"ES": {"es-ES", "Europe/Madrid"}, "PT": {"pt-PT", "Europe/Lisbon"},
	"IE": {"en-IE", "Europe/Dublin"}, "PL": {"pl-PL", "Europe/Warsaw"},
	"CZ": {"cs-CZ", "Europe/Prague"}, "AU": {"en-AU", "Australia/Sydney"},
	"NZ": {"en-NZ", "Pacific/Auckland"}, "JP": {"ja-JP", "Asia/Tokyo"},
	"KR": {"ko-KR", "Asia/Seoul"}, "SG": {"en-SG", "Asia/Singapore"},
	"HK": {"zh-HK", "Asia/Hong_Kong"}, "TW": {"zh-TW", "Asia/Taipei"},
	"CN": {"zh-CN", "Asia/Shanghai"}, "MO": {"zh-MO", "Asia/Macau"},
	"IN": {"en-IN", "Asia/Kolkata"}, "BR": {"pt-BR", "America/Sao_Paulo"},
	"MX": {"es-MX", "America/Mexico_City"}, "ZA": {"en-ZA", "Africa/Johannesburg"},
	"AE": {"ar-AE", "Asia/Dubai"}, "TR": {"tr-TR", "Europe/Istanbul"},
}
