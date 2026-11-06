package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const pluginPrepared = "/var/lib/tza-agent/plugins-prepared"
const pluginDropIn = "/etc/systemd/system/tza-agent.service.d/50-plugins.conf"
const legacyPluginConfig = "/etc/tza-probe/runtime-installer.json"
const legacyPluginUnit = "/etc/systemd/system/tza-runtime-installer.service"

func pluginsDisabled(path string, cfg config) bool {
	if cfg.PluginsDisabled {
		return true
	}
	_, err := os.Stat(filepath.Join(filepath.Dir(path), "plugins.disabled"))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func standardAgent(path string) bool {
	executable, err := os.Executable()
	if err != nil || executable != "/usr/local/bin/tza-agent" || path != "/etc/tza-probe/agent.json" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	info, err := os.Stat("/run/systemd/system")
	return err == nil && info.IsDir()
}

func integratedPluginsReady(path string) (bool, bool, error) {
	if !standardAgent(path) {
		return false, false, nil
	}
	return pluginReadiness(pluginPrepared)
}

func pluginReadiness(marker string) (bool, bool, error) {
	raw, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			return false, false, err
		}
		return false, true, writePrivate(marker, []byte("requested"))
	}
	if err != nil {
		return false, false, err
	}
	if string(raw) == "restart" {
		return false, true, writePrivate(marker, []byte("ready"))
	}
	switch string(raw) {
	case "ready":
		return true, false, nil
	case "requested":
		return false, false, nil
	default:
		return false, false, errors.New("invalid Agent plugin preparation state")
	}
}

func prepareAgentPlugins(path string) error {
	if !standardAgent(path) {
		return nil
	}
	if err := beginPluginPreparation(pluginPrepared); err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	if raw, err := os.ReadFile(legacyPluginConfig); err == nil {
		var legacy config
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return err
		}
		if cfg.RuntimeInstall == nil && legacy.Endpoint == cfg.Endpoint && legacy.Token == cfg.Token {
			cfg.RuntimeInstall = legacy.RuntimeInstall
			merged, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return err
			}
			if err := writePrivate(path, merged); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(legacyPluginUnit); err == nil {
		if err := pluginSystemctl("disable", "--now", "tza-runtime-installer.service"); err != nil {
			return err
		}
		if err := os.Remove(legacyPluginUnit); err != nil {
			return err
		}
		if err := os.Remove("/usr/local/libexec/tza/tza-runtime-installer"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	dropIn := ""
	if !pluginsDisabled(path, cfg) {
		directories := []string{"/var/lib/tza-agent-runtime", "/opt/ipsentinel-runtime", "/var/lib/ipsentinel/state/managed"}
		if cfg.RuntimeInstall != nil {
			directories = append(directories, cfg.RuntimeInstall.StagingDir, cfg.RuntimeInstall.InstallRoot, cfg.RuntimeInstall.StateDir)
		}
		for _, directory := range directories {
			if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" || strings.ContainsAny(directory, " \t\r\n\"\\%") {
				return errors.New("plugin directories must be canonical absolute paths without systemd specifiers")
			}
		}
		for _, directory := range directories {
			if err := os.MkdirAll(directory, 0o755); err != nil {
				return err
			}
		}
		dropIn = "[Service]\nCapabilityBoundingSet=CAP_NET_RAW CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_SETUID CAP_SETGID CAP_KILL\nReadWritePaths=" + strings.Join(directories, " ") + "\nTimeoutStopSec=25\nKillMode=control-group\n"
	}
	old, err := os.ReadFile(pluginDropIn)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed := string(old) != dropIn
	if changed {
		if dropIn == "" {
			if err := os.Remove(pluginDropIn); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(pluginDropIn), 0o755); err != nil {
				return err
			}
			if err := writePrivate(pluginDropIn, []byte(dropIn)); err != nil {
				return err
			}
		}
	}
	if err := pluginSystemctl("daemon-reload"); err != nil {
		return err
	}
	if changed && os.Getenv("INVOCATION_ID") != "" {
		return writePrivate(pluginPrepared, []byte("restart"))
	}
	return writePrivate(pluginPrepared, []byte("ready"))
}

func beginPluginPreparation(marker string) error {
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writePrivate(marker, []byte("requested"))
}

func pluginSystemctl(arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/systemctl", arguments...).Run()
}

func writePrivate(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
