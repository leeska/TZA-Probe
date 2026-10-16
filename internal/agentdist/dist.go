// Package agentdist serves Agent binaries and the one-line installation script
// from the Server, so a node only needs network access to the panel.
//
// Binaries are looked up first in Dir (an operator-managed directory, typically
// <data>/agents), then in the copies embedded at build time under bin/. A
// development build without embedded binaries simply reports them as missing.
package agentdist

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentsig"
)

//go:embed bin install.sh
var files embed.FS

// Archs lists the architectures the installation script knows how to map.
var Archs = []string{"amd64", "arm64"}

// ErrMissing reports that no binary is available for the requested architecture.
var ErrMissing = errors.New("agent binary not available")

type Dist struct {
	Dir     string
	noEmbed bool // tests: ignore binaries embedded in this build
	mu      sync.Mutex
	sums    map[string]string // sha256 of embedded copies, computed once
}

func Name(arch string) string { return "tza-agent-linux-" + arch }

func valid(arch string) bool {
	for _, a := range Archs {
		if a == arch {
			return true
		}
	}
	return false
}

// open returns the binary for arch and whether it came from Dir.
func (d *Dist) open(arch string) (fs.File, bool, error) {
	if !valid(arch) {
		return nil, false, ErrMissing
	}
	if d.Dir != "" {
		if f, err := os.Open(filepath.Join(d.Dir, Name(arch))); err == nil {
			return f, true, nil
		}
	}
	if d.noEmbed {
		return nil, false, ErrMissing
	}
	f, err := files.Open("bin/" + Name(arch))
	if err != nil {
		return nil, false, ErrMissing
	}
	return f, false, nil
}

// KeyFile is the operator's public upgrade key, beside the binaries. The
// install script pins it on the node; see package agentsig.
const KeyFile = "upgrade-key.pub"

// readFile reads an auxiliary file (a signature or the key) from Dir, else
// from the embedded copies.
func (d *Dist) readFile(name string) ([]byte, error) {
	if d.Dir != "" {
		if data, err := os.ReadFile(filepath.Join(d.Dir, name)); err == nil {
			return data, nil
		}
	}
	if d.noEmbed {
		return nil, ErrMissing
	}
	data, err := files.ReadFile("bin/" + name)
	if err != nil {
		return nil, ErrMissing
	}
	return data, nil
}

// UpgradeKey returns the public key file nodes pin, or ErrMissing.
func (d *Dist) UpgradeKey() ([]byte, error) {
	data, err := d.readFile(KeyFile)
	if err != nil {
		return nil, err
	}
	if _, err := agentsig.ParseKeys(string(data)); err != nil {
		return nil, fmt.Errorf("%s: %w", KeyFile, err)
	}
	return data, nil
}

// ErrUnsigned means the binary for an architecture has no usable signature, so
// it cannot be offered as a one-click upgrade.
var ErrUnsigned = errors.New("agent binary not signed")

// Signature returns the operator's signature of the binary for arch, checked
// against the binary actually served and against the published key, so a
// stale or foreign .sig is reported here rather than refused by every node.
func (d *Dist) Signature(arch string) (agentsig.Signature, error) {
	sum, err := d.Sum(arch)
	if err != nil {
		return agentsig.Signature{}, err
	}
	raw, err := d.readFile(Name(arch) + ".sig")
	if err != nil {
		return agentsig.Signature{}, fmt.Errorf("%w: no %s.sig", ErrUnsigned, Name(arch))
	}
	var sig agentsig.Signature
	if err := json.Unmarshal(raw, &sig); err != nil {
		return agentsig.Signature{}, fmt.Errorf("%w: %s.sig is not valid JSON", ErrUnsigned, Name(arch))
	}
	if sig.Arch != arch || sig.SHA256 != sum {
		return agentsig.Signature{}, fmt.Errorf("%w: %s.sig belongs to another binary", ErrUnsigned, Name(arch))
	}
	keyData, err := d.UpgradeKey()
	if err != nil {
		return agentsig.Signature{}, fmt.Errorf("%w: no %s", ErrUnsigned, KeyFile)
	}
	keys, _ := agentsig.ParseKeys(string(keyData))
	if err := sig.Verify(keys); err != nil {
		return agentsig.Signature{}, fmt.Errorf("%w: %v", ErrUnsigned, err)
	}
	return sig, nil
}

// Available reports which architectures can currently be served.
func (d *Dist) Available() []string {
	var out []string
	for _, a := range Archs {
		if f, _, err := d.open(a); err == nil {
			f.Close()
			out = append(out, a)
		}
	}
	return out
}

// Sum returns the hex SHA-256 of the binary for arch.
func (d *Dist) Sum(arch string) (string, error) {
	f, fromDir, err := d.open(arch)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if !fromDir {
		d.mu.Lock()
		defer d.mu.Unlock()
		if s, ok := d.sums[arch]; ok {
			return s, nil
		}
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	s := hex.EncodeToString(h.Sum(nil))
	if !fromDir {
		if d.sums == nil {
			d.sums = map[string]string{}
		}
		d.sums[arch] = s
	}
	return s, nil
}

// ServeBinary writes the binary for arch, with range support so interrupted
// downloads can resume. The write deadline is extended because the Server's
// default timeout is sized for JSON responses, not multi-megabyte files.
func (d *Dist) ServeBinary(w http.ResponseWriter, r *http.Request, arch string) error {
	f, _, err := d.open(arch)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+Name(arch)+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, Name(arch), info.ModTime(), rs)
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	http.ServeContent(w, r, Name(arch), info.ModTime(), bytes.NewReader(data))
	return nil
}

// Script returns the installation script. It is static: the endpoint and node
// key are passed as arguments by the command shown in the admin panel.
func Script() []byte {
	data, _ := files.ReadFile("install.sh")
	return data
}

// ParseDownload splits a /dl/ file name into architecture and whether the
// checksum was requested. ok is false for anything else.
func ParseDownload(name string) (arch string, sum bool, ok bool) {
	sum = strings.HasSuffix(name, ".sha256")
	name = strings.TrimSuffix(name, ".sha256")
	arch = strings.TrimPrefix(name, "tza-agent-linux-")
	if arch == name || !valid(arch) {
		return "", false, false
	}
	return arch, sum, true
}
