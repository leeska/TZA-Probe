package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentsig"
	"github.com/leeska/TZA-Probe/internal/model"
)

// 一键升级：面板点「升级 Agent」后，Server 在下一次上报的回复里带上新版本的 /dl/ 路径与
// sha256。Agent 下载到状态目录、校验摘要、试跑 --version，再把它换到自己的可执行文件上，
// 然后以 ExitRestart 退出，由服务管理器拉起新版本。
//
// 换文件分两种情形：
//   - OpenRC（没有沙箱）：Agent 自己就能写 /usr/local/bin，进程内直接换；
//   - systemd：单元开了 ProtectSystem=strict，/usr 对 Agent 只读。新文件留在状态目录
//     （StateDirectory=tza-agent，沙箱里可写），由单元的 ExecStartPre=+… --apply-update
//     在下一次启动前以完整权限换上。
//
// 两种情形都先在可执行文件旁边写好 .new 再 rename 覆盖，换到一半断电也不会留下半个文件；
// 旧版本留作 .previous，与安装脚本的手工升级一致。
//
// 升级只认运维者的签名（internal/agentsig）：公钥由安装脚本钉在 /etc/tza-probe/upgrade-key.pub，
// Server 手里没有私钥。没签名、签名不对、架构不对或不比自己新的版本一律拒收；
// 以完整权限执行的 --apply-update 换文件前再验一遍，沙箱里的进程放进状态目录的东西不会被直接信任。

// DefaultStateDir is where a downloaded upgrade waits to be applied.
const DefaultStateDir = "/var/lib/tza-agent"

// ExitRestart is the exit status that asks the service manager to start the
// Agent again. The install script's systemd unit lists it under
// SuccessExitStatus and RestartForceExitStatus; supervise-daemon restarts on
// any exit.
const ExitRestart = 3

const stagedName = "tza-agent.new"

// ErrAlreadyCurrent means the running binary already is the one offered.
var ErrAlreadyCurrent = errors.New("agent binary already current")

// KeyFile is where the install script pins the operator's upgrade key, beside
// the configuration.
const KeyFile = "upgrade-key.pub"

// LoadKeys reads the pinned upgrade keys. No file, or a file without keys,
// means the Agent accepts no upgrades.
func LoadKeys(path string) (map[string]ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return agentsig.ParseKeys(string(data))
}

// Updater downloads and applies the upgrade a report reply names.
type Updater struct {
	Endpoint   string // Server origin, as in the Agent configuration
	Executable string // the running binary, to be replaced
	StateDir   string // where the download is staged
	Keys       map[string]ed25519.PublicKey
	Client     *http.Client
}

// verify checks an offered or staged upgrade: signed by a pinned key, built
// for this architecture and newer than the running Agent.
func verify(sig agentsig.Signature, keys map[string]ed25519.PublicKey) error {
	if len(keys) == 0 {
		return errors.New("no upgrade key pinned on this node; rerun the install command")
	}
	if err := sig.Verify(keys); err != nil {
		return err
	}
	if sig.Arch != runtime.GOARCH {
		return fmt.Errorf("upgrade is for %s, this node runs %s", sig.Arch, runtime.GOARCH)
	}
	if !agentsig.Newer(sig.Version, model.Version) {
		return fmt.Errorf("upgrade %s is not newer than the running %s", sig.Version, model.Version)
	}
	return nil
}

// NewUpdater prepares an Updater for the running binary.
func NewUpdater(endpoint string, keys map[string]ed25519.PublicKey) (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return &Updater{
		Endpoint:   strings.TrimSuffix(endpoint, "/"),
		Executable: exe,
		StateDir:   DefaultStateDir,
		Keys:       keys,
		// 二进制十几 MB，按 JSON 接口定的 15 秒超时下不完；也不跟随跳转，与上报一致。
		Client: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

// Update fetches the offered binary, checks it and puts it in place. A nil
// error means the new binary is installed or staged and the Agent should
// restart; ErrAlreadyCurrent means there is nothing to do.
func (u *Updater) Update(ctx context.Context, up model.AgentUpgrade) error {
	want := strings.ToLower(up.SHA256)
	if len(want) != 64 || strings.Trim(want, "0123456789abcdef") != "" {
		return errors.New("upgrade checksum is not a sha256 hex digest")
	}
	if !strings.HasPrefix(up.Path, "/dl/") || strings.Contains(up.Path, "..") || strings.ContainsAny(up.Path, "?#") {
		return errors.New("upgrade path must be a /dl/ path on the Server")
	}
	if sum, err := fileSum(u.Executable); err == nil && sum == want {
		return ErrAlreadyCurrent
	}
	sig := agentsig.Signature{KeyID: up.KeyID, Arch: up.Arch, Version: up.Version, SHA256: want, Sig: up.Sig}
	if err := verify(sig, u.Keys); err != nil {
		return fmt.Errorf("upgrade refused: %w", err)
	}
	if err := os.MkdirAll(u.StateDir, 0o700); err != nil {
		return err
	}
	staged := filepath.Join(u.StateDir, stagedName)
	tmp := staged + ".part"
	defer os.Remove(tmp)
	if err := u.download(ctx, up.Path, tmp, want); err != nil {
		return err
	}
	// 能跑起来才换：架构不对、文件损坏的二进制在这里就会失败，旧版本照常运行。
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := exec.CommandContext(check, tmp, "--version").Run(); err != nil {
		return fmt.Errorf("downloaded agent does not run: %w", err)
	}
	sigJSON, err := json.Marshal(sig)
	if err != nil {
		return err
	}
	if err := os.WriteFile(staged+".sig", sigJSON, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, staged); err != nil {
		return err
	}
	if _, err := ApplyStaged(u.StateDir, u.Executable, u.Keys); err != nil && !readOnly(err) {
		return err
	}
	// 只读（systemd 沙箱）时文件留在状态目录，重启前由 ExecStartPre 换上。
	return nil
}

func (u *Updater) download(ctx context.Context, path, dest, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.Endpoint+path, nil)
	if err != nil {
		return err
	}
	resp, err := u.Client.Do(req)
	if err != nil {
		return fmt.Errorf("agent download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent download returned HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 256<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("agent download failed: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("agent download checksum mismatch: got %s", got)
	}
	return nil
}

// ApplyStaged replaces exe with the binary staged in stateDir, if any, after
// checking the signature stored next to it against keys and the binary
// against that signature. It reports whether a binary was installed. A staged
// file that fails the check is discarded so the next start does not trip over
// it again.
func ApplyStaged(stateDir, exe string, keys map[string]ed25519.PublicKey) (bool, error) {
	staged := filepath.Join(stateDir, stagedName)
	if _, err := os.Stat(staged); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	discard := func(err error) (bool, error) {
		os.Remove(staged)
		os.Remove(staged + ".sig")
		os.Remove(staged + ".sha256") // 早于签名的版本留下的
		return false, err
	}
	raw, err := os.ReadFile(staged + ".sig")
	if err != nil {
		return discard(fmt.Errorf("staged agent has no signature: %w", err))
	}
	var sig agentsig.Signature
	if err := json.Unmarshal(raw, &sig); err != nil {
		return discard(errors.New("staged agent signature is not valid JSON"))
	}
	if err := verify(sig, keys); err != nil {
		return discard(fmt.Errorf("staged agent refused: %w", err))
	}
	if sum, err := fileSum(staged); err != nil || sum != sig.SHA256 {
		return discard(errors.New("staged agent does not match its signature"))
	}
	// 先在目标目录写好 .new 再 rename：/var/lib 与 /usr/local 常不在同一文件系统，
	// 直接 rename 过去会失败；同目录内的 rename 则是原子的。
	next := exe + ".new"
	if err := copyFile(staged, next, 0o755); err != nil {
		os.Remove(next)
		return false, err
	}
	if err := copyFile(exe, exe+".previous", 0o755); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(next)
		return false, err
	}
	if err := os.Rename(next, exe); err != nil {
		os.Remove(next)
		return false, err
	}
	os.Remove(staged)
	os.Remove(staged + ".sig")
	return true, nil
}

// readOnly reports an error from writing where the sandbox forbids it.
func readOnly(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS)
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(dst, mode)
	}
	return err
}
