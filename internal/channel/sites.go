package channel

// The sites capability: the Server renders nginx server blocks for the
// sites it manages on a node, and the Agent installs them as
// /etc/nginx/conf.d/tza-site-<id>.conf, checks with nginx -t and reloads.
// Files it did not write are only read (for the list of other sites).
const (
	CapabilitySites = "sites"
	// KindSitesStatus answers SitesStatus.
	KindSitesStatus = "sites.status"
	// KindSitesApply replaces the whole set of managed files: the request
	// line (SitesApplyParams) is followed by Size bytes of JSON, a map of
	// file name → content. Answers SitesResult; a failed nginx -t restores
	// the previous set.
	KindSitesApply = "sites.apply"
	// SiteFilePrefix names managed files in the nginx conf.d directory.
	SiteFilePrefix = "tza-site-"
	// MaxSiteFiles bounds the managed sites per node.
	MaxSiteFiles = 200
)

// NginxSite is a server block found in nginx's configuration.
type NginxSite struct {
	File    string   `json:"file"`
	Names   []string `json:"names"`
	Managed bool     `json:"managed"`
}

type SitesStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"` // e.g. 1.26.3
	Active    bool   `json:"active"`
	// Managed maps each managed file name to its SHA-256.
	Managed map[string]string `json:"managed"`
	Sites   []NginxSite       `json:"sites"`
	Error   string            `json:"error,omitempty"`
}

type SitesApplyParams struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// MaxSitesPayload bounds the files sent in one apply.
const MaxSitesPayload = 8 << 20

type SitesResult struct {
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}
