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
	"github.com/leeska/TZA-Probe/internal/brand"
	"github.com/leeska/TZA-Probe/internal/certs"
	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/docker"
	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/ipguard"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/proxy"
	"github.com/leeska/TZA-Probe/internal/regiondrift"
	"github.com/leeska/TZA-Probe/internal/remote"
	"github.com/leeska/TZA-Probe/internal/service"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/internal/totp"
	"github.com/leeska/TZA-Probe/internal/users"
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
		case "agent-keygen":
			return agentKeygenCommand(os.Args[2:])
		case "agent-sign":
			return agentSignCommand(os.Args[2:])
		case "browser-worker":
			return browserWorkerCommand(os.Args[2:])
		case "browser-install":
			return browserInstallCommand(os.Args[2:])
		case "check-data":
			return checkDataCommand(os.Args[2:])
		}
	}
	addr := flag.String("listen", "127.0.0.1:25774", "HTTP listen address; put TLS reverse proxy in front")
	dir := flag.String("data", "./data", "private data directory")
	version := flag.Bool("version", false, "print version")
	workerSocket := flag.String("browser-socket", "", "private Unix socket for the isolated browser worker")
	// The TZA-CC market was removed. The old flags are still accepted, so a unit
	// file or compose command that passes them keeps starting.
	legacyCC := false
	flag.Func("cc-url", "removed: the TZA-CC market no longer exists; ignored", func(string) error { legacyCC = true; return nil })
	flag.Func("cc-key", "removed: the TZA-CC market no longer exists; ignored", func(string) error { legacyCC = true; return nil })
	flag.Parse()
	if legacyCC || os.Getenv("TZA_CC_URL") != "" || os.Getenv("TZA_CC_KEYS") != "" {
		slog.Warn("--cc-url / --cc-key / TZA_CC_URL / TZA_CC_KEYS are ignored: the TZA-CC market was removed; install themes and plugins by uploading zips")
	}
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
	// Docker 与手动启动不经过安装脚本的 init，首次启动时在这里建账号。初始密码不写日志
	// ——容器日志会被永久保留：配置了 TZA_ADMIN_PASSWORD 就用它，否则写进数据目录的
	// 一次性文件 admin-password.txt，登录改密后删除该文件。
	if env := os.Getenv("TZA_ADMIN_PASSWORD"); env != "" {
		if _, err := account.Load(*dir); errors.Is(err, account.ErrNotFound) {
			if err := account.Validate(account.DefaultUsername, env); err != nil {
				return fmt.Errorf("TZA_ADMIN_PASSWORD %w", err)
			}
			if err := account.Save(*dir, account.DefaultUsername, env); err != nil {
				return err
			}
			slog.Info("administrator account created from TZA_ADMIN_PASSWORD; change it in the panel")
		} else if err != nil {
			return err
		}
	}
	if username, password, created, err := account.Ensure(*dir); err != nil {
		return err
	} else if created {
		if err := account.WriteInitialCredentials(*dir, username, password); err != nil {
			return err
		}
		slog.Warn("administrator account created; initial credentials are in admin-password.txt in the data directory, delete that file after signing in", "dir", *dir)
	}
	dbPath := databasePath(*dir)
	modules, drift, remoteMod, usersMod, proxyMod := shippedModules()
	store, err := storage.OpenWith(dbPath, module.Migrations(modules))
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
	themes := &theme.Store{Dir: filepath.Join(*dir, "themes")}
	plugins := &plugin.Store{Dir: filepath.Join(*dir, "plugins")}
	// 同一个发送器给后台的「发送测试」和告警引擎用：通知插件渠道经它查已安装的插件。
	sender := notify.Transport{Plugins: plugins}
	guardStore := ipguard.New(store.DB())
	if err = guardStore.Recover(ctx); err != nil {
		return err
	}
	guardBroker := ipguard.NewBroker(guardStore)
	guardBroker.WorkerConfigured = *workerSocket != ""
	drift.Broker, drift.Nodes = guardBroker, store
	hub := &channel.Hub{}
	remoteMod.Hub, remoteMod.Nodes = hub, store
	var certsMod *certs.Module
	for _, m := range modules {
		switch m := m.(type) {
		case *docker.Module:
			m.Nodes = store
		case *certs.Module:
			m.Store, m.Hub, m.Nodes = &certs.Store{DB: store.DB()}, hub, store
			m.Init()
			certsMod = m
		}
	}
	usersMod.Store = &users.Store{DB: store.DB()}
	// 代理：入站存在主库，内核缓存在数据目录的 kernels/，经同一个管理通道下发到节点。
	proxyMod.Store, proxyMod.Users, proxyMod.Hub, proxyMod.Nodes = &proxy.Store{DB: store.DB()}, usersMod.Store, hub, store
	proxyMod.Kernels = &proxy.Cache{Dir: filepath.Join(*dir, "kernels")}
	proxyMod.Init()
	usersMod.OnChange = proxyMod.Kick
	api := &httpapi.API{Store: store, Notifier: sender, AdminToken: token, AccountDir: *dir, Assets: web.Handler(), Agents: agents, Themes: themes, Plugins: plugins, Logo: &brand.Store{Dir: filepath.Join(*dir, "branding")}, Modules: modules}
	server := &http.Server{Addr: *addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var wg sync.WaitGroup
	wg.Add(2)
	if *workerSocket != "" {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := guardBroker.ServeWorker(ctx, *workerSocket); err != nil && ctx.Err() == nil {
				slog.Error("browser worker listener failed", "error", err)
				stop()
			}
		}()
		go func() { defer wg.Done(); guardBroker.Schedule(ctx) }()
	}
	// 地区偏移检测：按任务与节点设置生成每台节点的检测设置（见 regiondrift.reconcile）。
	wg.Add(1)
	go func() { defer wg.Done(); drift.Run(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); usersMod.Run(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); proxyMod.Run(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); certsMod.Run(ctx) }()
	go func() {
		defer wg.Done()
		channels := &service.Service{Store: store, Plugins: plugins}
		(notify.Engine{Repository: store, Channels: channels, Sender: sender}).Run(ctx)
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

func databasePath(dir string) string {
	return filepath.Join(dir, "tza-probe.db")
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
			slog.Info("administrator API key created; browser login uses username and password", "path", path)
		} else if err != nil {
			return "", err
		} else {
			token = strings.TrimSpace(string(data))
		}
	}
	if len(token) < 32 {
		return "", errors.New("administrator key must be at least 32 bytes long")
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

// checkDataCommand exits 0 when this binary can run on --data (missing or
// empty directory, or a database at this schema). It never writes: the
// installer calls it with the new binary before replacing the old one.
func checkDataCommand(args []string) error {
	fs := flag.NewFlagSet("tza-server check-data", flag.ContinueOnError)
	dir := fs.String("data", "./data", "private data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	mods, _, _, _, _ := shippedModules()
	return storage.CheckDataDir(*dir, module.Migrations(mods))
}

// initCommand prepares a data directory before the first start: it makes
// sure the API compatibility key and panel account exist. Only the panel
// credentials are printed; the password is shown only when the account is
// created here. It is stored hashed and cannot be shown again
// (reset-password issues a new one).
func initCommand(args []string) error {
	dir, err := dataDir("init", args, nil)
	if err != nil {
		return err
	}
	if _, err := adminToken(dir); err != nil {
		return err
	}
	username, password, created, err := account.Ensure(dir)
	if err != nil {
		return err
	}
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
	var disableTOTP bool
	dir, err := dataDir("reset-password", args, func(fs *flag.FlagSet) {
		fs.StringVar(&username, "username", "", "also change the username (default: keep the current one)")
		fs.BoolVar(&disableTOTP, "disable-totp", false, "turn two-factor login off (lost authenticator); keeps the password unless combined with a reset")
	})
	if err != nil {
		return err
	}
	if disableTOTP {
		if err := totp.Remove(dir); err != nil {
			return err
		}
		fmt.Println("两步验证已关闭；登录后可在“面板设置 → 账号与安全”重新开启。密码未改动。")
		return nil
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
	fmt.Println("（登录后可在“面板设置 → 后台账号”改成自己的密码；旧浏览器会话已失效，无需重启服务）")
	return nil
}

// shippedModules is every module this binary serves, in migration order,
// still unconfigured: check-data only needs their migrations, and run wires
// the ones it returns separately once the database is open.
func shippedModules() ([]module.Module, *regiondrift.Module, *remote.Module, *users.Module, *proxy.Module) {
	drift, remoteMod, usersMod, proxyMod := &regiondrift.Module{}, &remote.Module{}, &users.Module{}, &proxy.Module{}
	// Docker 不建表，复用远程管理的通道与转接。
	dockerMod := &docker.Module{Remote: remoteMod}
	return []module.Module{drift, remoteMod, usersMod, proxyMod, dockerMod, &certs.Module{}}, drift, remoteMod, usersMod, proxyMod
}
