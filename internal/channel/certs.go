package channel

// The certs capability: the Server sends an issued certificate and the Agent
// writes it to a directory and runs the reload command the administrator set.
const (
	CapabilityCerts = "certs"
	// KindCertDeploy answers CertDeployResult.
	KindCertDeploy = "certs.deploy"
	// MaxReload bounds a reload command.
	MaxReload = 1024
)

// CertDeployParams writes Fullchain (PEM, 0644) and Key (PEM, 0600) as
// fullchain.pem and privkey.pem in Dir, then runs Reload with /bin/sh -c.
type CertDeployParams struct {
	Dir       string `json:"dir"`
	Fullchain string `json:"fullchain"`
	Key       string `json:"key"`
	Reload    string `json:"reload,omitempty"`
}

type CertDeployResult struct {
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}
