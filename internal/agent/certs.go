package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/channel"
)

// CertHandlers serves the certs capability: write a certificate where the
// administrator said and reload what uses it.
func CertHandlers() map[string]channel.Handler {
	return map[string]channel.Handler{channel.KindCertDeploy: deployCert}
}

// certDir accepts an absolute, clean directory outside the system trees a
// certificate never belongs in.
func certDir(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || len(dir) > 255 || strings.ContainsAny(dir, "\x00\n") {
		return errors.New("目录须是规范的绝对路径")
	}
	for _, bad := range []string{"/", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var"} {
		if dir == bad {
			return fmt.Errorf("不能直接写到 %s，请用它下面的子目录", bad)
		}
	}
	for _, bad := range []string{"/proc/", "/sys/", "/dev/"} {
		if strings.HasPrefix(dir, bad) {
			return fmt.Errorf("不能写到 %s 下", bad)
		}
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tza-new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func deployCert(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.CertDeployParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return channel.WriteLine(conn, channel.CertDeployResult{Error: "请求无效"})
	}
	if err := certDir(p.Dir); err != nil {
		return channel.WriteLine(conn, channel.CertDeployResult{Error: err.Error()})
	}
	if !strings.Contains(p.Fullchain, "-----BEGIN CERTIFICATE-----") || !strings.Contains(p.Key, "PRIVATE KEY-----") || len(p.Reload) > channel.MaxReload {
		return channel.WriteLine(conn, channel.CertDeployResult{Error: "证书内容或重载命令无效"})
	}
	if err := os.MkdirAll(p.Dir, 0755); err != nil {
		return channel.WriteLine(conn, channel.CertDeployResult{Error: "建目录失败：" + err.Error()})
	}
	// The key first: a reader that sees the new chain also finds its key.
	if err := writeAtomic(filepath.Join(p.Dir, "privkey.pem"), []byte(p.Key), 0600); err != nil {
		return channel.WriteLine(conn, channel.CertDeployResult{Error: "写私钥失败：" + err.Error()})
	}
	if err := writeAtomic(filepath.Join(p.Dir, "fullchain.pem"), []byte(p.Fullchain), 0644); err != nil {
		return channel.WriteLine(conn, channel.CertDeployResult{Error: "写证书失败：" + err.Error()})
	}
	if strings.TrimSpace(p.Reload) == "" {
		return channel.WriteLine(conn, channel.CertDeployResult{})
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", p.Reload)
	cmd.Env = shellEnv()
	cmd.Dir = p.Dir
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	result := channel.CertDeployResult{Output: tailString(out, 4096)}
	if err != nil {
		result.Error = "证书已写入，但重载命令失败：" + err.Error()
	}
	return channel.WriteLine(conn, result)
}
