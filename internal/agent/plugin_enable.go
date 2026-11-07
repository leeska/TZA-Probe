package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

const pluginServiceUser = "tza-ipguard"

const pluginService = `[Unit]
Description=TZA IP region protection plugin
After=network-online.target

[Service]
Type=simple
User=tza-ipguard
Group=tza-ipguard
WorkingDirectory=/var/lib/ipsentinel/state/managed
Environment=HOME=/var/lib/ipsentinel/state/managed
Environment=PYTHONDONTWRITEBYTECODE=1
RuntimeDirectory=ipsentinel
RuntimeDirectoryMode=0700
ExecStart=/opt/ipsentinel-runtime/current/bin/ipsentinel-control serve --config /etc/ipsentinel/runtime.toml --policy /etc/ipsentinel/runtime-policy.toml --socket /run/ipsentinel/control.sock --allow-uid 0
Delegate=cpu memory pids
DelegateSubgroup=control
OOMPolicy=continue
Restart=on-failure
RestartSec=5
TimeoutStopSec=15
KillMode=control-group
UMask=0077
MemoryMax=640M
MemorySwapMax=0
CPUQuota=100%
TasksMax=384
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/ipsentinel/state/managed
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
RestrictSUIDSGID=yes
RestrictRealtime=yes

[Install]
WantedBy=multi-user.target
`

type pluginEnableInput struct {
	Config  RuntimeInstallConfig      `json:"config"`
	Request runtimepkg.ChannelRequest `json:"request"`
}

func (local *LocalRuntime) prepareEnable(ctx context.Context, request runtimepkg.ChannelRequest) (runtimepkg.OperationReview, error) {
	config := local.Config
	if config.InstallRoot != runtimepkg.DefaultInstallRoot || config.StateDir != "/var/lib/ipsentinel/state/managed" ||
		config.ConfigPath != "/etc/ipsentinel/runtime.toml" || config.PolicyPath != "/etc/ipsentinel/runtime-policy.toml" || config.Unit != "ipsentinel-runtime.service" ||
		request.Enable == nil || request.Enable.Validate() != nil {
		return runtimepkg.OperationReview{}, errors.New("first enablement requires the standard Agent plugin installation")
	}
	review, err := runtimepkg.PrepareOperation(ctx, runtimepkg.Store{Dir: config.StagingDir}, request.StagePlanSHA256, local.Keys,
		local.Enrollment.Host, local.Enrollment.Target, runtimepkg.OperationOptions{Action: "install", InstallRoot: config.InstallRoot, ReferencePath: config.TrustedInstaller})
	if err != nil {
		return review, err
	}
	receipt, err := runtimepkg.InspectInstallation(ctx, review.Plan)
	if err != nil || receipt == nil {
		return review, errors.New("the approved runtime must already be installed and verified")
	}
	if review.Plan.ExpectedCurrent != "" || review.Plan.SelectionSHA256 != "" {
		return review, errors.New("first enablement cannot take over a selected or previously configured installation")
	}
	for _, path := range []string{config.ConfigPath, config.PolicyPath, "/etc/systemd/system/" + config.Unit, "/run/systemd/system/" + config.Unit, "/usr/lib/systemd/system/" + config.Unit, "/run/ipsentinel/control.sock"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return review, errors.New("an existing IPSentinel configuration or service requires local migration")
		}
	}
	entries, err := os.ReadDir(config.StateDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
		return review, errors.New("first enablement requires an empty plugin state directory")
	}
	legacy, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "list-unit-files", "--no-legend", "--no-pager", "ipsentinel-observe@*.timer").Output()
	var exitErr *exec.ExitError
	noMatches := errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(bytes.TrimSpace(legacy)) == 0
	if err != nil && !noMatches || len(bytes.TrimSpace(legacy)) != 0 {
		return review, errors.New("legacy IPSentinel schedules must be migrated explicitly")
	}
	review.Plan.Options.Action = "enable"
	settings := *request.Enable
	review.Plan.Enable = &settings
	review.PlanSHA256, err = ipsentinel.CanonicalSHA256(review.Plan)
	review.ApprovalTemplate = runtimepkg.OperationApproval{FormatVersion: 1, Action: "enable", PlanSHA256: review.PlanSHA256}
	return review, err
}

func (local *LocalRuntime) enablePlugin(ctx context.Context, request runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error) {
	review, err := local.prepareEnable(ctx, request)
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if request.State == "requested" {
		return runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: review.PlanSHA256, Operation: &review}, nil
	}
	if review.PlanSHA256 != request.PlanSHA256 {
		return runtimepkg.ChannelResult{}, errors.New("plugin enablement inputs changed after confirmation")
	}
	journal := FileRuntimePending{Directory: filepath.Join(local.Config.StagingDir, "agent-pending")}
	root, err := journal.open()
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	defer root.Close()
	name := "enable-" + request.ID + ".json"
	raw, err := json.Marshal(pluginEnableInput{Config: local.Config, Request: request})
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if err := writeRuntimeJournal(root, name, raw); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	command := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--quiet", "--wait", "--collect", "--service-type=exec",
		"--unit=tza-plugin-enable-"+request.ID, "--property=RuntimeMaxSec=180",
		"--", "/usr/local/bin/tza-agent", "--plugin-enable", filepath.Join(journal.Directory, name))
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return runtimepkg.ChannelResult{}, errors.New("plugin initialization did not complete; retained local evidence for inspection")
	}
	raw, err = readRuntimeJournal(root, "enabled.json")
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	var result runtimepkg.ChannelResult
	if err := ipsentinel.DecodeStrict(raw, &result); err != nil || runtimepkg.ValidateChannelReceipt(request, result) != nil {
		return runtimepkg.ChannelResult{}, errors.New("plugin did not return the approved startup receipt")
	}
	return result, nil
}

func ExecutePluginEnable(ctx context.Context, path string) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("plugin initialization requires a private local request and root")
	}
	journal := FileRuntimePending{Directory: filepath.Dir(path)}
	root, err := journal.open()
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := readRuntimeJournal(root, filepath.Base(path))
	if err != nil {
		return err
	}
	var input pluginEnableInput
	if err := ipsentinel.DecodeStrict(raw, &input); err != nil {
		return err
	}
	if filepath.Join(input.Config.StagingDir, "agent-pending") != journal.Directory {
		return errors.New("plugin request is outside its local installation journal")
	}
	if err := validateRuntimePaths(input.Config); err != nil {
		return err
	}
	keys, err := loadRuntimeKeys(input.Config.CCKeysPath)
	if err != nil {
		return err
	}
	identity, err := readRuntimeJournal(root, "identity")
	if err != nil {
		return err
	}
	local, err := runtimeForIdentity(input.Config, keys, string(identity))
	if err != nil {
		return err
	}
	request := input.Request
	review := runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: request.PlanSHA256, Operation: request.Operation}
	if request.State != "approved" || request.Action != "enable" || request.Enrollment != local.Enrollment ||
		request.ApprovedAt < time.Now().Unix()-900 || request.ApprovedAt > time.Now().Unix()+5 || runtimepkg.ValidateChannelPlan(request, review) != nil {
		return errors.New("plugin initialization has no matching current approval")
	}
	current, err := local.prepareEnable(ctx, request)
	if err != nil || current.PlanSHA256 != request.PlanSHA256 {
		if err == nil {
			err = errors.New("plan digest changed between confirmation and initialization")
		}
		_ = writeRuntimeJournal(root, "enable-error-"+request.ID, []byte(err.Error()))
		return errors.New("plugin initialization inputs changed")
	}
	if _, err := os.Lstat(filepath.Join(journal.Directory, "enable-started-"+request.ID)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("plugin initialization already started; it must not be repeated")
	}
	marker, err := root.OpenFile("enable-started-"+request.ID, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(marker, request.PlanSHA256)
	if err := errors.Join(writeErr, marker.Sync(), marker.Close(), syncRuntimeJournal(root)); err != nil {
		return err
	}
	result, err := local.applyEnable(ctx, request)
	if err != nil {
		_ = writeRuntimeJournal(root, "enable-error-"+request.ID, []byte(err.Error()))
		return err
	}
	raw, err = json.Marshal(result)
	if err != nil {
		return err
	}
	return writeRuntimeJournal(root, "enabled.json", raw)
}

func pluginCommand(ctx context.Context, path string, arguments ...string) error {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1"}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("plugin setup command %s failed: %w", filepath.Base(path), err)
	}
	return nil
}

func pluginConfigFiles(target ipsentinel.Target, settings runtimepkg.EnableSettings) ([]byte, []byte) {
	config := fmt.Sprintf(`node_id = %q
expected_ip = %q
egress_url = "https://api64.ipify.org"
urls = ["https://www.google.com/"]
allowed_origins = ["https://api64.ipify.org", "https://www.google.com"]
state_dir = "/var/lib/ipsentinel/state/managed"
page_adapters = ["google_home"]
target_country = %q
schedule_enabled = false
memory_limit_mib = 440
network_limit_mib = 20
total_timeout_seconds = 120
max_requests = 150
daily_run_limit = 8
daily_navigation_limit = 32
daily_network_limit_mib = 160
storage_limit_mib = 500
`, target.NodeID, settings.ExpectedIP, settings.Country)
	policy := fmt.Sprintf(`policy_version = 1
state_dir = "/var/lib/ipsentinel/state/managed"
browser_path = "/opt/ipsentinel-runtime/current/browsers"
permissions = ["network.public_https", "browser.execute", "profile.persist"]
profile_ids = ["ipify-public-v1", "google-home-neutral-v1", "google-home-ncr-v1", "youtube-premium-v1", "meta-ads-neutral-v1", "meta-ads-country-v1"]
allowed_egress_ips = [%q]
legacy_timers = []
legacy_reviewed = true
[max_limits]
timeout_ms = 120000
memory_bytes = 461373440
cpu_millicores = 850
pids = 256
network_bytes = 20971520
http_requests = 150
[max_daily_budget]
runs = 8
navigations = 32
network_bytes = 167772160
`, settings.ExpectedIP)
	return []byte(config), []byte(policy)
}

func (local *LocalRuntime) applyEnable(ctx context.Context, request runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error) {
	for _, path := range []string{"/var/lib/ipsentinel", "/var/lib/ipsentinel/state", "/etc/ipsentinel"} {
		if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return runtimepkg.ChannelResult{}, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return runtimepkg.ChannelResult{}, err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || !ok || owner.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return runtimepkg.ChannelResult{}, errors.New("plugin state and configuration parents must be protected root-owned directories")
		}
	}
	if err := os.Mkdir(local.Config.StateDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return runtimepkg.ChannelResult{}, err
	}
	info, err := os.Lstat(local.Config.StateDir)
	if err != nil || !info.IsDir() {
		return runtimepkg.ChannelResult{}, errors.New("plugin state must be a real directory")
	}
	serviceUser, err := user.Lookup(pluginServiceUser)
	if errors.Is(err, user.UnknownUserError(pluginServiceUser)) {
		err = pluginCommand(ctx, "/usr/sbin/useradd", "--system", "--user-group", "--home-dir", local.Config.StateDir, "--no-create-home", "--shell", "/usr/sbin/nologin", pluginServiceUser)
		if err == nil {
			serviceUser, err = user.Lookup(pluginServiceUser)
		}
	}
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	uid, uidErr := strconv.Atoi(serviceUser.Uid)
	gid, gidErr := strconv.Atoi(serviceUser.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || serviceUser.HomeDir != local.Config.StateDir {
		return runtimepkg.ChannelResult{}, errors.New("the plugin service account is not a dedicated non-root identity")
	}
	if err := os.Chown(local.Config.StateDir, uid, gid); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if err := os.Chmod(local.Config.StateDir, 0o700); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(local.Config.ConfigPath), 0o755); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	config, policy := pluginConfigFiles(local.Enrollment.Target, *request.Enable)
	for path, raw := range map[string][]byte{local.Config.ConfigPath: config, local.Config.PolicyPath: policy} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return runtimepkg.ChannelResult{}, err
		}
		_, writeErr := file.Write(raw)
		syncErr := file.Sync()
		ownerErr := file.Chown(uid, gid)
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, ownerErr, closeErr); err != nil {
			return runtimepkg.ChannelResult{}, err
		}
	}
	options := runtimepkg.OperationOptions{
		Action: "select", InstallRoot: local.Config.InstallRoot, ReferencePath: local.Config.TrustedInstaller,
		StateDir: local.Config.StateDir, ConfigPath: local.Config.ConfigPath, PolicyPath: local.Config.PolicyPath, Unit: local.Config.Unit,
	}
	store := runtimepkg.Store{Dir: local.Config.StagingDir}
	review, err := runtimepkg.PrepareOperation(ctx, store, request.StagePlanSHA256, local.Keys, local.Enrollment.Host, local.Enrollment.Target, options)
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	approval := review.ApprovalTemplate
	approval.Approved = true
	approvalRaw, _ := json.Marshal(approval)
	selected, err := runtimepkg.ApplyOperation(ctx, store, request.StagePlanSHA256, local.Keys, local.Enrollment.Host, local.Enrollment.Target, options, approvalRaw)
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	command := exec.CommandContext(ctx, filepath.Join(local.Config.InstallRoot, "current/bin/ipsentinel-control"), "adopt", "--config", local.Config.ConfigPath,
		"--policy", local.Config.PolicyPath, "--controller-id", local.Enrollment.Target.ControllerID, "--node-id", local.Enrollment.Target.NodeID)
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1", "HOME=" + local.Config.StateDir}
	command.Dir = local.Config.StateDir
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return runtimepkg.ChannelResult{}, errors.New("plugin local adoption failed")
	}
	unitPath := "/etc/systemd/system/" + local.Config.Unit
	unit, err := os.OpenFile(unitPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	_, writeErr := io.WriteString(unit, pluginService)
	err = errors.Join(writeErr, unit.Sync(), unit.Close())
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if err := pluginCommand(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if err := pluginCommand(ctx, "/usr/bin/systemctl", "start", local.Config.Unit); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	client := ipsentinel.NewClient(ipsentinel.DefaultSocketPath, local.Enrollment.Target)
	defer client.Close()
	var hello ipsentinel.Hello
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		hello, err = client.Hello(ctx, "")
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return runtimepkg.ChannelResult{}, errors.New("plugin startup did not return its local identity")
		case <-timer.C:
		}
	}
	if hello.Status.Control.Paused {
		return runtimepkg.ChannelResult{}, errors.New("plugin isolation preflight failed; service remains paused")
	}
	native := runtimepkg.EnableConfig(*request.Enable)
	valid, err := client.ConfigValidate(ctx, "", native)
	if err != nil || !valid.Valid {
		return runtimepkg.ChannelResult{}, errors.New("plugin rejected the approved service configuration")
	}
	fence := ipsentinel.Fence{ExecutorInstanceID: hello.Status.ExecutorInstanceID, Epoch: hello.Status.Control.Epoch}
	if _, err := client.ConfigApply(ctx, "", fence, 0, native); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	hello, err = client.Hello(ctx, "")
	if err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if err := runtimepkg.ValidateEnabledHello(hello, request.Version, *request.Enable, time.Now()); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	if err := pluginCommand(ctx, "/usr/bin/systemctl", "enable", local.Config.Unit); err != nil {
		return runtimepkg.ChannelResult{}, err
	}
	receipt := runtimepkg.OperationReceipt{
		FormatVersion: 1, State: "enabled", Selected: true, Activated: true, PlanSHA256: request.PlanSHA256,
		Plan: request.Operation.Plan, Release: request.Operation.Plan.ArchiveSHA256, Backup: selected.Backup, Hello: &hello,
	}
	return runtimepkg.ChannelResult{RequestID: request.ID, State: "enabled", PlanSHA256: request.PlanSHA256, OperationReceipt: &receipt}, nil
}
