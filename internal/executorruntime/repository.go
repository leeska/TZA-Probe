package executorruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type Repository struct {
	base   *url.URL
	keys   map[string]ed25519.PublicKey
	client *http.Client
}

func NewRepository(address string, keys map[string]ed25519.PublicKey) (*Repository, error) {
	return newRepository(address, keys, nil)
}

func newRepository(address string, keys map[string]ed25519.PublicKey, transport http.RoundTripper) (*Repository, error) {
	base, err := url.Parse(strings.TrimSpace(address))
	if err != nil || base.Host == "" || base.User != nil || base.Opaque != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.RawPath != "" {
		return nil, errors.New("CC repository requires an absolute address without credentials, query or fragment")
	}
	if base.Scheme != "https" {
		ip, err := netip.ParseAddr(base.Hostname())
		if base.Scheme != "http" || err != nil || !ip.IsLoopback() {
			return nil, errors.New("CC repository requires HTTPS; plain HTTP is restricted to literal loopback addresses")
		}
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if base.Path == "" {
		base.Path = "/"
	}
	pinned := map[string]ed25519.PublicKey{}
	for id, key := range keys {
		if _, _, err := tzapkg.ParsePublicKey(tzapkg.FormatPublicKey(id, key)); err != nil {
			return nil, errors.New("CC repository signing keys must be valid pinned Ed25519 public keys")
		}
		pinned[id] = slices.Clone(key)
	}
	if len(pinned) == 0 {
		return nil, errors.New("CC repository requires at least one locally pinned signing key")
	}
	if transport == nil {
		connection := http.DefaultTransport.(*http.Transport).Clone()
		connection.DisableCompression = true
		connection.ResponseHeaderTimeout = 15 * time.Second
		connection.MaxResponseHeaderBytes = 64 << 10
		transport = connection
	}
	return &Repository{
		base: base, keys: pinned,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (repository *Repository) Fetch(ctx context.Context, version string, host Host, target ipsentinel.Target) (*Release, error) {
	if repository == nil || !tzapkg.ValidVersion(version) || !tzapkg.ValidVersion(host.ProbeVersion) || !tzapkg.ValidVersion(host.AgentVersion) {
		return nil, errors.New("CC runtime fetch requires exact release, Probe and Agent versions")
	}
	if (host.Platform != "linux-amd64" && host.Platform != "linux-arm64") || !ipsentinel.ValidUUID(target.ControllerID) || !ipsentinel.ValidUUID(target.NodeID) {
		return nil, errors.New("CC runtime fetch requires a supported Linux platform and provisioned node binding")
	}
	payload, _ := json.Marshal(map[string]string{"version": version, "probe": host.ProbeVersion})
	response, err := repository.request(ctx, http.MethodPost, repository.base.JoinPath("api", "v1", "packages", tzapkg.OfficialExecutorID, "download"), payload, 15*time.Second, "release metadata")
	if err != nil {
		return nil, err
	}
	var ticket struct {
		URL       string           `json:"url"`
		Size      int64            `json:"size"`
		Signature tzapkg.Signature `json:"signature"`
		Receipt   string           `json:"receipt"`
	}
	raw, err := readResponse(response, 64<<10, -1)
	if err != nil || ipsentinel.DecodeStrict(raw, &ticket) != nil {
		return nil, errors.New("CC runtime release metadata is invalid or exceeds its size limit")
	}
	if ticket.Size <= 0 || ticket.Size > tzapkg.MaxUpload || ticket.Signature.Package != tzapkg.OfficialExecutorID || ticket.Signature.Version != version || len(repository.keys[ticket.Signature.KeyID]) != ed25519.PublicKeySize {
		return nil, errors.New("CC runtime release metadata does not match the requested signed release")
	}
	address, err := url.Parse(ticket.URL)
	expected := repository.base.JoinPath("dl", tzapkg.OfficialExecutorID, version)
	if err != nil || address.User != nil || address.Fragment != "" || address.RawPath != "" || !strings.EqualFold(address.Scheme, expected.Scheme) || !strings.EqualFold(address.Host, expected.Host) || address.Path != expected.Path {
		return nil, errors.New("CC runtime declaration URL must be the exact release path on the configured repository origin")
	}
	query, err := url.ParseQuery(address.RawQuery)
	if err != nil || len(query) != 2 || len(query["exp"]) != 1 || len(query["sig"]) != 1 || query.Get("sig") == "" {
		return nil, errors.New("CC runtime declaration URL has invalid ticket parameters")
	}
	if expiration, err := strconv.ParseInt(query.Get("exp"), 10, 64); err != nil || expiration <= time.Now().Add(-30*time.Second).Unix() || expiration > time.Now().Add(15*time.Minute).Unix() {
		return nil, errors.New("CC runtime declaration URL has an invalid expiration")
	}
	response, err = repository.request(ctx, http.MethodGet, address, nil, 2*time.Minute, "runtime declaration")
	if err != nil {
		return nil, err
	}
	archive, err := readResponse(response, ticket.Size, ticket.Size)
	if err != nil {
		return nil, errors.New("CC runtime declaration differs from its declared size or could not be read")
	}
	signature, err := json.Marshal(ticket.Signature)
	if err != nil {
		return nil, err
	}
	return Inspect(archive, signature, repository.keys, host, target)
}

func (repository *Repository) Stage(ctx context.Context, store Store, release *Release, approval []byte) (Receipt, error) {
	if repository == nil || release == nil || release.digest == "" {
		return Receipt{}, errors.New("a configured repository and verified release are required")
	}
	current, err := Inspect(release.archive, release.signature, repository.keys, release.plan.Host, release.plan.Target)
	if err != nil || current.digest != release.digest {
		return Receipt{}, errors.New("runtime release is not trusted by this CC repository")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	return store.stage(ctx, current, approval, func(ctx context.Context, artifact Artifact) (io.ReadCloser, error) {
		address := repository.base.JoinPath("api", "v1", "packages", current.plan.Package, "versions", current.plan.Version, "artifacts", artifact.SHA256)
		response, err := repository.request(ctx, http.MethodGet, address, nil, 10*time.Minute, "runtime artifact")
		if err != nil {
			return nil, err
		}
		if (response.ContentLength >= 0 && response.ContentLength != artifact.SizeBytes) || response.Header.Get("X-Content-SHA256") != artifact.SHA256 {
			response.Body.Close()
			return nil, artifactError(artifact)
		}
		return response.Body, nil
	})
}

type repositoryBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *repositoryBody) Close() error {
	body.cancel()
	return body.ReadCloser.Close()
}

func (repository *Repository) request(ctx context.Context, method string, address *url.URL, payload []byte, timeout time.Duration, operation string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	request, err := http.NewRequestWithContext(ctx, method, address.String(), bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cannot create CC %s request", operation)
	}
	request.Header.Set("User-Agent", "tza-executor-runtime/1")
	request.Header.Set("Accept-Encoding", "identity")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := repository.client.Do(request)
	if err != nil {
		cause := ctx.Err()
		cancel()
		if cause != nil {
			return nil, fmt.Errorf("CC %s request cancelled: %w", operation, cause)
		}
		return nil, fmt.Errorf("CC %s transport failed", operation)
	}
	if response.StatusCode != http.StatusOK || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		response.Body.Close()
		cancel()
		return nil, fmt.Errorf("CC %s rejected (HTTP %d or unsupported encoding)", operation, response.StatusCode)
	}
	response.Body = &repositoryBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

func readResponse(response *http.Response, maximum, expected int64) ([]byte, error) {
	defer response.Body.Close()
	if response.ContentLength > maximum || (expected >= 0 && response.ContentLength >= 0 && response.ContentLength != expected) {
		return nil, errors.New("CC response has an invalid content length")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || int64(len(raw)) > maximum || (expected >= 0 && int64(len(raw)) != expected) {
		return nil, errors.New("CC response differs from its bounded content length")
	}
	return raw, nil
}
