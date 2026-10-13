package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/service"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			return initCommand(os.Args[2:])
		case "reset-password":
			return resetPasswordCommand(os.Args[2:])
		}
	}
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
	token, err := adminToken(*dir)
	if err != nil {
		return err
	}
	// Docker 与手动启动不经过安装脚本的 init，首次启动时在这里建账号，初始密码只在这一次
	// 写进日志；安装脚本会先跑 init，systemd 部署的日志里不会出现密码。
	if username, password, created, err := account.Ensure(*dir); err != nil {
		return err
	} else if created {
		slog.Warn("administrator account created; this password is shown only once, change it in the panel", "username", username, "password", password)
	}
	dbPath := databasePath(*dir)
	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	slog.Info("database opened", "path", dbPath)
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	agents := &agentdist.Dist{Dir: filepath.Join(*dir, "agents")}
	if available := agents.Available(); len(available) == 0 {
		slog.Warn("no agent binaries embedded or in data directory; one-line installation unavailable", "dir", agents.Dir)
	} else {
		slog.Info("agent binaries available for one-line installation", "arch", available)
	}
	api := &httpapi.API{Store: store, Notifier: notify.Transport{}, AdminToken: token, AccountDir: *dir, Assets: web.Handler(), Agents: agents}
	server := &http.Server{Addr: *addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		channels := &service.Service{Store: store}
		(notify.Engine{Repository: store, Channels: channels, Sender: notify.Transport{}}).Run(ctx)
	}()
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

// databasePath is the database file in dir. A data directory created before the
// file was named tza-probe.db still holds tza-v2.db; that file keeps being used
// in place rather than renamed, so rolling back to an older binary still finds
// its data.
func databasePath(dir string) string {
	current := filepath.Join(dir, "tza-probe.db")
	if fileExists(current) {
		return current
	}
	if legacy := filepath.Join(dir, "tza-v2.db"); fileExists(legacy) {
		return legacy
	}
	return current
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// adminToken is the administrator key: TZA_ADMIN_TOKEN, or the admin-token
// file in dir, created with a fresh key the first time.
func adminToken(dir string) (token string, err error) {
	token = os.Getenv("TZA_ADMIN_TOKEN")
	if token == "" {
		path := filepath.Join(dir, "admin-token")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			token = model.Secret()
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return "", err
			}
			_, writeErr := f.WriteString(token + "\n")
			closeErr := f.Close()
			if writeErr != nil {
				return "", writeErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			if err := account.OwnLikeDir(path, dir); err != nil {
				return "", err
			}
			slog.Info("administrator key created; read locally to log in", "path", path)
		} else if err != nil {
			return "", err
		} else {
			token = strings.TrimSpace(string(data))
		}
	}
	if len(token) < 32 {
		return "", errors.New("administrator key must contain at least 32 characters")
	}
	return token, nil
}

// dataDir parses the --data flag shared by the subcommands. The directory
// must already exist: creating it here as root would leave it owned by root.
func dataDir(name string, args []string, extra func(*flag.FlagSet)) (string, error) {
	fs := flag.NewFlagSet("tza-server "+name, flag.ContinueOnError)
	dir := fs.String("data", "./data", "private data directory")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("多余的参数：%s", strings.Join(fs.Args(), " "))
	}
	info, err := os.Stat(*dir)
	if err != nil {
		return "", fmt.Errorf("数据目录不可用：%w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录", *dir)
	}
	return *dir, nil
}

// initCommand prepares a data directory before the first start: it makes
// sure the administrator key and the panel account exist and prints both. The
// password is printed only when the account is created here; it is stored
// hashed and cannot be shown again (reset-password issues a new one).
func initCommand(args []string) error {
	dir, err := dataDir("init", args, nil)
	if err != nil {
		return err
	}
	token, err := adminToken(dir)
	if err != nil {
		return err
	}
	username, password, created, err := account.Ensure(dir)
	if err != nil {
		return err
	}
	fmt.Printf("管理密钥：%s\n", token)
	if created {
		fmt.Printf("后台用户名：%s\n", username)
		fmt.Printf("后台密码：%s\n", password)
		fmt.Println("（密码只显示这一次，请立即保存；登录后可在“面板设置 → 后台账号”修改）")
	} else {
		fmt.Printf("后台用户名：%s（账号已存在，密码不再显示；忘记密码可执行 tza-server reset-password）\n", username)
	}
	return nil
}

// resetPasswordCommand sets a new random password from the terminal, for when
// the panel password is forgotten. The running server rereads the account on
// every login, so no restart is needed.
func resetPasswordCommand(args []string) error {
	var username string
	dir, err := dataDir("reset-password", args, func(fs *flag.FlagSet) {
		fs.StringVar(&username, "username", "", "also change the username (default: keep the current one)")
	})
	if err != nil {
		return err
	}
	if username == "" {
		// 账号文件不存在或已损坏也要能重置，用户名回到默认。
		username = account.DefaultUsername
		if current, err := account.Load(dir); err == nil {
			username = current.Username
		}
	}
	password := account.RandomPassword()
	if err := account.Save(dir, username, password); err != nil {
		return err
	}
	fmt.Printf("后台用户名：%s\n", strings.TrimSpace(username))
	fmt.Printf("后台新密码：%s\n", password)
	fmt.Println("（登录后可在“面板设置 → 后台账号”改成自己的密码；已登录的浏览器会话在服务重启后失效）")
	return nil
}
