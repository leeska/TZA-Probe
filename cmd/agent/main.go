package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/leeska/TZA-Probe/internal/agent"
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
	if *version {
		slog.Info(model.Version)
		return nil
	}
	if *applyUpdate {
		// 由 systemd 的 ExecStartPre=-+ 或 OpenRC 的 start_pre 调用：没有待装的版本就什么也不做，
		// 换失败只记一笔，不挡住旧版本照常启动。
		exe, err := os.Executable()
		if err == nil {
			var applied bool
			if applied, err = agent.ApplyStaged(agent.DefaultStateDir, exe); applied {
				slog.Info("staged agent upgrade installed")
			}
		}
		if err != nil {
			slog.Warn("staged agent upgrade not installed", "error", err)
		}
		return nil
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	var cfg config
	if err = d.Decode(&cfg); err != nil {
		return err
	}
	if err = d.Decode(&struct{}{}); err != io.EOF {
		return errors.New("configuration must contain one JSON object")
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
	app := agent.Agent{Collector: &agent.SystemCollector{Interfaces: cfg.Interfaces, Mount: cfg.Mount}, Prober: prober, Transport: transport}
	restart := false
	if *selfUpdate {
		updater, err := agent.NewUpdater(cfg.Endpoint)
		if err != nil {
			return err
		}
		app.Upgrader = updater
		app.Restart = func() { restart = true; cancel() }
	}
	slog.Info("TZA Probe Agent started", "version", model.Version, "self_update", *selfUpdate)
	app.Run(ctx)
	if restart {
		return errRestart
	}
	return nil
}
