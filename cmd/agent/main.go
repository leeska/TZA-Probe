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
	Endpoint   string   `json:"endpoint"`
	Token      string   `json:"token"`
	Interfaces []string `json:"interfaces"`
	Mount      string   `json:"mount"`
	Nexttrace  string   `json:"nexttrace"`
	AllowHTTP  bool     `json:"allow_http"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "agent.json", "private JSON configuration file")
	version := flag.Bool("version", false, "print version")
	flag.Parse()
	if *version {
		slog.Info(model.Version)
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
	if cfg.Nexttrace != "" {
		cfg.Nexttrace, err = exec.LookPath(cfg.Nexttrace)
		if err != nil {
			return errors.New("configured nexttrace executable was not found")
		}
	} else {
		slog.Info("route probing unavailable until nexttrace is configured")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := agent.Agent{Collector: &agent.SystemCollector{Interfaces: cfg.Interfaces, Mount: cfg.Mount}, Prober: probe.Runner{Nexttrace: cfg.Nexttrace}, Transport: transport}
	slog.Info("TZA Probe Agent started", "version", model.Version)
	app.Run(ctx)
	return nil
}
