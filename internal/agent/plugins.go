package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type Plugins struct {
	Transport *HTTPTransport
	Config    *RuntimeInstallConfig
	Directory string
}

func (plugins Plugins) Run(ctx context.Context) {
	for ctx.Err() == nil {
		config := plugins.Config
		var err error
		if config == nil {
			config, err = plugins.bootstrap(ctx)
		}
		if err == nil {
			journal := FileRuntimePending{Directory: filepath.Join(config.StagingDir, "agent-pending")}
			local, provisionErr := ProvisionRuntime(*config, journal)
			err = provisionErr
			if err == nil {
				installer := RuntimeInstaller{Transport: plugins.Transport, Worker: local, Enrollment: local.Enrollment, Journal: journal}
				installer.Run(ctx)
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		slog.Warn("Agent plugin setup unavailable; ordinary probes continue", "error", err)
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (plugins Plugins) bootstrap(ctx context.Context) (*RuntimeInstallConfig, error) {
	if plugins.Transport == nil || !filepath.IsAbs(plugins.Directory) {
		return nil, errors.New("plugin setup requires a transport and private absolute directory")
	}
	identityHash := sha256.Sum256([]byte(plugins.Transport.endpoint + "\n" + plugins.Transport.token))
	directory := filepath.Join(plugins.Directory, hex.EncodeToString(identityHash[:]))
	store := FileRuntimePending{Directory: directory}
	root, err := store.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := readRuntimeJournal(root, "config.json")
	if err == nil {
		var config RuntimeInstallConfig
		if err := ipsentinel.DecodeStrict(raw, &config); err != nil {
			return nil, err
		}
		return &config, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var identity struct {
		ControllerID string `json:"controller_id"`
		NodeID       string `json:"node_id"`
		CCURL        string `json:"cc_url"`
		ProbeVersion string `json:"probe_version"`
	}
	if err := plugins.Transport.request(ctx, http.MethodPost, "/api/v1/install/identity", map[string]string{"token": plugins.Transport.token}, &identity); err != nil {
		return nil, err
	}
	if !ipsentinel.ValidUUID(identity.ControllerID) || !ipsentinel.ValidUUID(identity.NodeID) || !tzapkg.ValidVersion(identity.ProbeVersion) {
		return nil, errors.New("invalid plugin node identity")
	}
	keysRaw, err := readRuntimeJournal(root, "cc-keys")
	if errors.Is(err, os.ErrNotExist) {
		keysRaw, err = plugins.Transport.pluginAsset(ctx, "/dl/runtime-cc-keys", 64<<10)
		if err == nil {
			if _, err = parseRuntimeKeys(keysRaw); err == nil {
				err = writeRuntimeJournal(root, "cc-keys", keysRaw)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	keys, err := parseRuntimeKeys(keysRaw)
	if err != nil {
		return nil, err
	}
	if _, err := runtimepkg.NewRepository(identity.CCURL, keys); err != nil {
		return nil, err
	}
	installer, err := plugins.Transport.pluginAsset(ctx, "/dl/ipsentinel-install-runtime.py", 1<<20)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(installer)
	if hex.EncodeToString(digest[:]) != runtimepkg.ReferenceInstallerSHA256 {
		return nil, errors.New("reference installer does not match the digest pinned in Agent")
	}
	if err := writeRuntimeJournal(root, "installer.py", installer); err != nil {
		return nil, err
	}
	config := RuntimeInstallConfig{
		CCURL: identity.CCURL, CCKeysPath: filepath.Join(directory, "cc-keys"),
		ProbeVersion: identity.ProbeVersion, StagingDir: filepath.Join(directory, "staging"),
		InstallRoot: "/opt/ipsentinel-runtime", TrustedInstaller: filepath.Join(directory, "installer.py"),
		StateDir: "/var/lib/ipsentinel/state/managed", ConfigPath: "/etc/ipsentinel/runtime.toml",
		PolicyPath: "/etc/ipsentinel/runtime-policy.toml", Unit: "ipsentinel-runtime.service",
		Target: ipsentinel.Target{ControllerID: identity.ControllerID, NodeID: identity.NodeID},
	}
	raw, err = json.Marshal(config)
	if err != nil {
		return nil, err
	}
	if err := writeRuntimeJournal(root, "config.json", raw); err != nil {
		return nil, err
	}
	return &config, nil
}

func (transport *HTTPTransport) pluginAsset(ctx context.Context, path string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, transport.endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := transport.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &HTTPStatusError{Status: response.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("plugin setup asset exceeds its limit")
	}
	return raw, nil
}
