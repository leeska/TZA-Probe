package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/agentsig"
	"github.com/leeska/TZA-Probe/internal/model"
)

// 这两个子命令在运维者自己的电脑上用：私钥不该出现在 Server 上，Server 只拿到签好的
// 安装包、它们的 .sig 和公钥（见 internal/agentsig 与 docs/DEPLOYMENT.md「一键升级」）。

// agentKeygenCommand writes a new upgrade signing key pair.
func agentKeygenCommand(args []string) error {
	fs := flag.NewFlagSet("agent-keygen", flag.ContinueOnError)
	id := fs.String("id", "", "key id: lowercase letters, digits, dots and dashes (for example ops-2026)")
	out := fs.String("out", "", "private key file to create (kept off the Server); the public key goes beside it as "+agentdist.KeyFile)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *out == "" {
		return errors.New("用法：tza-server agent-keygen --id <key id> --out <私钥文件>")
	}
	private, public, err := agentsig.GenerateKey(*id)
	if err != nil {
		return err
	}
	pubPath := filepath.Join(filepath.Dir(*out), agentdist.KeyFile)
	if _, err := os.Stat(pubPath); err == nil {
		return fmt.Errorf("%s 已存在，换一个目录，免得覆盖正在用的公钥", pubPath)
	}
	// O_EXCL：不覆盖已有的私钥。
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(private); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, []byte(public), 0o644); err != nil {
		return err
	}
	fmt.Printf("私钥：%s（只留在这台电脑上并备份；不要放到 Server）\n", *out)
	fmt.Printf("公钥：%s\n  %s", pubPath, public)
	fmt.Printf("把公钥复制到 Server 数据目录的 agents/ 下，节点重跑一次安装命令后即钉住它。\n")
	return nil
}

// agentSignCommand signs Agent binaries named tza-agent-linux-<arch>, writing
// <binary>.sig beside each.
func agentSignCommand(args []string) error {
	fs := flag.NewFlagSet("agent-sign", flag.ContinueOnError)
	keyPath := fs.String("key", "", "private key file from agent-keygen")
	version := fs.String("version", "", "release version the binaries were built as (MAJOR.MINOR.PATCH, the -X model.Version value)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" || *version == "" || fs.NArg() == 0 {
		return errors.New("用法：tza-server agent-sign --key <私钥文件> --version X.Y.Z tza-agent-linux-amd64 [tza-agent-linux-arm64 ...]")
	}
	if !agentsig.Releasable(*version) {
		return fmt.Errorf("版本 %q 须为 MAJOR.MINOR.PATCH（不带 -dev 之类后缀）：Agent 只接受比自己新的签名版本，带后缀的版本排不出先后", *version)
	}
	raw, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	keyID, key, err := agentsig.ParsePrivateKey(string(raw))
	if err != nil {
		return err
	}
	for _, path := range fs.Args() {
		arch := strings.TrimPrefix(filepath.Base(path), "tza-agent-linux-")
		if _, _, ok := agentdist.ParseDownload(filepath.Base(path)); !ok || strings.HasSuffix(path, ".sha256") {
			return fmt.Errorf("%s：文件名须为 tza-agent-linux-<amd64|arm64>", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		sig := agentsig.Sign(key, keyID, arch, *version, hex.EncodeToString(digest[:]))
		out, err := json.MarshalIndent(sig, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path+".sig", append(out, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("已签名 %s（%s，版本 %s，密钥 %s）\n", path, arch, *version, keyID)
	}
	if *version != model.Version {
		fmt.Printf("提示：这个 tza-server 自己是 %s；签名写的是 %s，请确认它就是那批 Agent 编译时的版本号。\n", model.Version, *version)
	}
	return nil
}
