package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/leeska/TZA-ControlCenter/internal/browserworker"
)

// browserWorkerCommand runs the isolated browser for the region drift check.
// It is started by its own systemd unit (deploy/tza-browser.service) as the
// tza-browser user with PrivateNetwork=yes; see docs/IP-GUARD-INTEGRATION.md.
func browserWorkerCommand(args []string) error {
	fs := flag.NewFlagSet("browser-worker", flag.ContinueOnError)
	socket := fs.String("socket", "/run/tza-browser/control.sock", "the Server's private worker socket (--browser-socket)")
	state := fs.String("state", "/var/lib/tza-browser", "private state directory (results awaiting delivery, experiment profiles)")
	chromium := fs.String("chromium", "", "Chromium headless-shell executable (default: the pinned build under /opt/tza-browser)")
	selfTest := fs.Bool("self-test", false, "check isolation and the Chromium sandbox, print a JSON report and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *chromium == "" {
		*chromium = browserworker.ChromiumPath("/opt/tza-browser", runtime.GOARCH)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *selfTest {
		report := browserworker.SelfTest(ctx, *state, *chromium)
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		encoder.Encode(report)
		if !report.OK() {
			return errors.New("browser worker self-test failed")
		}
		return nil
	}
	return browserworker.New(*socket, *state, *chromium).Run(ctx)
}

// browserInstallCommand puts the pinned Chromium build under --dir (verifying
// its SHA-256) and reports shared libraries the system still lacks. Exit status
// 3 means "installed, but libraries are missing"; the list is printed one per
// line on stdout so the installer can map it to packages.
func browserInstallCommand(args []string) error {
	fs := flag.NewFlagSet("browser-install", flag.ContinueOnError)
	dir := fs.String("dir", "/opt/tza-browser", "install directory")
	archive := fs.String("archive", "", "use this already downloaded chrome-headless-shell zip instead of downloading")
	check := fs.Bool("check-libs", false, "only list missing shared libraries of the installed build")
	if err := fs.Parse(args); err != nil {
		return err
	}
	binary := browserworker.ChromiumPath(*dir, runtime.GOARCH)
	if !*check {
		var err error
		binary, err = browserworker.Install(context.Background(), *dir, *archive)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Chromium %s: %s\n", browserworker.ChromiumVersion, binary)
	}
	missing, err := browserworker.MissingLibraries(binary)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		fmt.Println(strings.Join(missing, "\n"))
		os.Exit(3)
	}
	return nil
}
