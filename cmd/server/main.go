package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("listen", "127.0.0.1:25774", "HTTP listen address; put TLS reverse proxy in front")
	dir := flag.String("data", "./data", "private data directory")
	version := flag.Bool("version", false, "print version")
	flag.Parse()
	if *version {
		slog.Info(model.Version)
		return nil
	}
	if err := os.MkdirAll(*dir, 0700); err != nil {
		return err
	}
	token := os.Getenv("TZA_ADMIN_TOKEN")
	if token == "" {
		path := filepath.Join(*dir, "admin-token")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			token = model.Secret()
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, writeErr := f.WriteString(token + "\n")
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
			slog.Info("administrator key created; read locally to log in", "path", path)
		} else if err != nil {
			return err
		} else {
			token = strings.TrimSpace(string(data))
		}
	}
	if len(token) < 32 {
		return errors.New("administrator key must contain at least 32 characters")
	}
	store, err := storage.Open(filepath.Join(*dir, "tza-v2.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	api := &httpapi.API{Store: store, Notifier: notify.Transport{}, AdminToken: token, Assets: web.Handler()}
	server := &http.Server{Addr: *addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); (notify.Engine{Repository: store, Sender: notify.Transport{}}).Run(ctx) }()
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := store.Clean(ctx, time.Now()); err != nil && ctx.Err() == nil {
				slog.Error("retention cleanup failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		slog.Info("TZA Probe listening", "address", *addr, "version", model.Version)
		done <- server.ListenAndServe()
	}()
	select {
	case err = <-done:
		stop()
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdown); shutdownErr != nil {
		server.Close()
	}
	stop()
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
