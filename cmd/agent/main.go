package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/leeska/TZA-Probe/internal/agent"
	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/probe"
)

type config struct {
	Endpoint    string   `json:"endpoint"`
	Token       string   `json:"token"`
	Interfaces  []string `json:"interfaces"`
	Mount       string   `json:"mount"`
	Nexttrace   string   `json:"nexttrace"`
	GeoProvider string   `json:"geo_provider"`
	TraceMethod string   `json:"trace_method"`
	AllowHTTP   bool     `json:"allow_http"`
}

// manageFile next to agent.json turns on the management capabilities (shell,
// later docker and so on) over the management channel. The install script
// writes it with --manage, together with a service unit that is not
// sandboxed, and removes it with --manage-off.
const manageFile = "manage.json"

// manageConfig is manage.json. DisabledCapabilities switches single
// capabilities off on this host whatever the Server asks: the local brake.
type manageConfig struct {
	Enabled              bool     `json:"enabled"`
	DisabledCapabilities []string `json:"disabled_capabilities"`
}

// loadManage reads manage.json; a missing file means management is off. Like
// the egress policy it must be a private regular file.
func loadManage(path string) (manageConfig, error) {
	var cfg manageConfig
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return cfg, fmt.Errorf("%s must be a private regular file (0600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if len(raw) > 8192 {
		return cfg, fmt.Errorf("%s is too large", path)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// managementCapabilities is every capability Manage can turn on, with its
// stream handlers.
func managementCapabilities() map[string]map[string]channel.Handler {
	return map[string]map[string]channel.Handler{
		channel.CapabilityShell: agent.ShellHandlers(),
		// 代理：主控下发内核与配置，本机用 systemd 托管 tza-proxy-<内核> 服务。
		channel.CapabilityProxy: agent.NewProxyHost().Handlers(),
		// Docker：本机 Docker Engine API（白名单）、日志、拉镜像、Compose 项目与容器终端。
		channel.CapabilityDocker: agent.NewDockerHost().Handlers(),
	}
}

// errRestart ends the process with agent.ExitRestart so the service manager
// starts the freshly installed binary.
var errRestart = errors.New("restart to finish the upgrade")

func main() {
	if err := run(); errors.Is(err, errRestart) {
		os.Exit(agent.ExitRestart)
	} else if err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "agent.json", "private JSON configuration file")
	version := flag.Bool("version", false, "print version")
	selfUpdate := flag.Bool("self-update", false, "accept upgrades offered by the Server (needs a service manager that restarts the Agent)")
	applyUpdate := flag.Bool("apply-update", false, "install an upgrade staged by --self-update, then exit (run before start, with write access to the binary)")
	flag.Parse()
	// 升级公钥由安装脚本放在配置文件旁边；没有它就不接受任何升级。
	keyPath := filepath.Join(filepath.Dir(*path), agent.KeyFile)
	if *version {
		slog.Info(model.Version)
		return nil
	}
	if *applyUpdate {
		// 由 systemd 的 ExecStartPre=-+ 或 OpenRC 的 start_pre 调用：没有待装的版本就什么也不做，
		// 换失败只记一笔，不挡住旧版本照常启动。
		exe, err := os.Executable()
		keys, keyErr := agent.LoadKeys(keyPath)
		if err == nil {
			err = keyErr
		}
		if err == nil {
			var applied bool
			if applied, err = agent.ApplyStaged(agent.DefaultStateDir, exe, keys); applied {
				slog.Info("staged agent upgrade installed")
			}
		}
		if err != nil {
			slog.Warn("staged agent upgrade not installed", "error", err)
		}
		return nil
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		return err
	}
	transport, err := agent.NewTransport(cfg.Endpoint, cfg.Token, cfg.AllowHTTP)
	if err != nil {
		return err
	}
	prober := probe.Runner{}
	if cfg.Nexttrace != "" {
		// An operator-supplied binary stays an explicit override.
		cfg.Nexttrace, err = exec.LookPath(cfg.Nexttrace)
		if err != nil {
			return errors.New("configured nexttrace executable was not found")
		}
		prober.Nexttrace = cfg.Nexttrace
		slog.Info("route probing uses the configured nexttrace binary", "path", cfg.Nexttrace)
	} else {
		prober.Embedded = &probe.Embedded{GeoProvider: cfg.GeoProvider, Method: cfg.TraceMethod}
		slog.Info("route probing runs in process through the embedded NTrace-core tracer", "geo_provider", probe.EffectiveGeoProvider(cfg.GeoProvider), "configured_geo_provider", cfg.GeoProvider, "trace_method", cfg.TraceMethod)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	app := agent.Agent{Collector: &agent.SystemCollector{Interfaces: cfg.Interfaces, Mount: cfg.Mount}, Prober: prober, Transport: transport, Egress: &agent.Egress{Transport: transport, PolicyFile: filepath.Join(filepath.Dir(*path), "ipguard-egress.json")}}
	app.Prober = prober.DefaultRegistry()
	manage, err := loadManage(filepath.Join(filepath.Dir(*path), manageFile))
	if err != nil {
		return err
	}
	if manage.Enabled {
		handlers, caps := map[string]channel.Handler{}, []string{}
		for capability, kinds := range managementCapabilities() {
			if slices.Contains(manage.DisabledCapabilities, capability) {
				slog.Info("management capability disabled in the local configuration", "capability", capability)
				continue
			}
			caps = append(caps, capability)
			maps.Copy(handlers, kinds)
		}
		if len(handlers) > 0 {
			app.Background = append(app.Background, &agent.Channel{Transport: transport, Handlers: handlers, Capabilities: caps})
		}
	}
	restart := false
	if *selfUpdate {
		keys, err := agent.LoadKeys(keyPath)
		switch {
		case err != nil:
			return fmt.Errorf("upgrade key %s: %w", keyPath, err)
		case len(keys) == 0:
			// 没钉公钥的节点不报 self_update，后台会提示在机器上重跑一次安装命令。
			slog.Warn("one-click upgrade disabled: no upgrade key pinned; rerun the install command to pin the Server's key", "path", keyPath)
			*selfUpdate = false
		default:
			updater, err := agent.NewUpdater(cfg.Endpoint, keys)
			if err != nil {
				return err
			}
			app.Upgrader = updater
			app.Restart = func() { restart = true; cancel() }
		}
	}
	slog.Info("TZA Probe Agent started", "version", model.Version, "self_update", *selfUpdate)
	app.Run(ctx)
	if restart {
		return errRestart
	}
	return nil
}

func loadConfig(path string) (config, error) {
	var cfg config
	file, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return cfg, errors.New("configuration must contain one JSON object")
	}
	return cfg, nil
}
