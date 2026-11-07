package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/agent"
	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/probe"
)

type config struct {
	Endpoint        string                      `json:"endpoint"`
	Token           string                      `json:"token"`
	Interfaces      []string                    `json:"interfaces"`
	Mount           string                      `json:"mount"`
	Nexttrace       string                      `json:"nexttrace"`
	GeoProvider     string                      `json:"geo_provider"`
	TraceMethod     string                      `json:"trace_method"`
	AllowHTTP       bool                        `json:"allow_http"`
	IPSentinel      *executorConfig             `json:"ipsentinel,omitempty"`
	RuntimeInstall  *agent.RuntimeInstallConfig `json:"runtime_install,omitempty"`
	PluginsDisabled bool                        `json:"plugins_disabled,omitempty"`
}

type executorConfig struct {
	SocketPath string            `json:"socket_path"`
	Target     ipsentinel.Target `json:"target"`
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
	runtimeInstaller := flag.Bool("runtime-installer", false, "run only the explicitly provisioned runtime installation channel in a dedicated privileged service")
	runtimePeer := flag.Bool("runtime-peer", false, "serve one bounded local runtime inspection or pause request as a non-root peer")
	preparePlugins := flag.Bool("prepare-plugins", false, "prepare integrated Agent plugins during installation or upgrade")
	pluginEnable := flag.String("plugin-enable", "", "complete one approved first-use plugin initialization from the private local journal")
	flag.Parse()
	if *pluginEnable != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
		defer cancel()
		return agent.ExecutePluginEnable(ctx, *pluginEnable)
	}
	if *runtimePeer {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runtimepkg.ServeRuntimePeer(ctx, os.Stdin, os.Stdout)
	}
	// 升级公钥由安装脚本放在配置文件旁边；没有它就不接受任何升级。
	keyPath := filepath.Join(filepath.Dir(*path), agent.KeyFile)
	if *version {
		slog.Info(model.Version)
		return nil
	}
	if *preparePlugins {
		return prepareAgentPlugins(*path)
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
		if err := prepareAgentPlugins(*path); err != nil {
			slog.Warn("Agent plugin preparation failed; ordinary probes continue", "error", err)
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
	if *runtimeInstaller {
		if cfg.RuntimeInstall == nil || *selfUpdate {
			return errors.New("dedicated runtime installer requires runtime_install config and cannot enable Agent self-updates")
		}
		journal := agent.FileRuntimePending{Directory: filepath.Join(cfg.RuntimeInstall.StagingDir, "agent-pending")}
		local, err := agent.ProvisionRuntime(*cfg.RuntimeInstall, journal)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		installer := agent.RuntimeInstaller{Transport: transport, Worker: local, Enrollment: local.Enrollment, Journal: journal}
		installer.Run(ctx)
		return nil
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
	app := agent.Agent{Collector: &agent.SystemCollector{Interfaces: cfg.Interfaces, Mount: cfg.Mount}, Prober: prober, Transport: transport}
	pluginsReady, restartPlugins, err := integratedPluginsReady(*path)
	if err != nil {
		slog.Warn("Agent plugin setup unavailable; ordinary probes continue", "error", err)
	}
	if restartPlugins && *selfUpdate {
		return errRestart
	}
	if !pluginsDisabled(*path, cfg) && runtime.GOOS == "linux" && os.Geteuid() == 0 && (pluginsReady || (!standardAgent(*path) && cfg.RuntimeInstall != nil)) {
		app.Plugins = agent.Plugins{Transport: transport, Config: cfg.RuntimeInstall, Directory: filepath.Join(agent.DefaultStateDir, "plugins")}
	}
	app.Prober = prober.DefaultRegistry()
	if cfg.IPSentinel != nil {
		if runtime.GOOS != "linux" {
			return errors.New("IPSentinel integration requires Linux")
		}
		if cfg.IPSentinel.SocketPath != "" && !filepath.IsAbs(cfg.IPSentinel.SocketPath) {
			return errors.New("IPSentinel socket_path must be absolute")
		}
		if !ipsentinel.ValidUUID(cfg.IPSentinel.Target.ControllerID) || !ipsentinel.ValidUUID(cfg.IPSentinel.Target.NodeID) {
			return errors.New("IPSentinel target requires provisioned controller/node UUIDs")
		}
		client := ipsentinel.NewClient(cfg.IPSentinel.SocketPath, cfg.IPSentinel.Target)
		defer client.Close()
		app.ExecutorSync = ipsentinel.Synchronizer{Reader: client, Transport: transport, Target: cfg.IPSentinel.Target}
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
