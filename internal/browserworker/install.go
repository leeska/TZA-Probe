package browserworker

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Chromium is the pinned Chrome for Testing headless-shell build. Google
// publishes it for linux64 and linux-arm64; the hashes were taken from the
// official downloads and are checked before anything is extracted.
const ChromiumVersion = "156.0.8078.4"

var chromiumBuilds = map[string]struct{ Platform, SHA256 string }{
	"amd64": {"linux64", "c3e50596aef52c58f8fda3c6b3411c33d34c88b177b3ed1c2243359fd3b8ba20"},
	"arm64": {"linux-arm64", "81ab9d4d3c301dc5fdfb6c541b9e6632b16fc819a6a854460f40a76fe2b2c476"},
}

const maxArchive = 400 << 20

// ChromiumURL is where the build for arch is downloaded from.
func ChromiumURL(arch string) (string, error) {
	build, ok := chromiumBuilds[arch]
	if !ok {
		return "", fmt.Errorf("no Chromium build for %s", arch)
	}
	return fmt.Sprintf("https://storage.googleapis.com/chrome-for-testing-public/%s/%s/chrome-headless-shell-%s.zip", ChromiumVersion, build.Platform, build.Platform), nil
}

// ChromiumPath is the executable inside dir for arch.
func ChromiumPath(dir, arch string) string {
	build := chromiumBuilds[arch]
	return filepath.Join(dir, "chromium-"+ChromiumVersion, "chrome-headless-shell-"+build.Platform, "chrome-headless-shell")
}

// Install downloads (or reads archive, when given), verifies and unpacks the
// pinned Chromium into dir and returns the executable's path. An existing
// complete install of the same version is kept.
func Install(ctx context.Context, dir, archive string) (string, error) {
	arch := runtime.GOARCH
	build, ok := chromiumBuilds[arch]
	if runtime.GOOS != "linux" || !ok {
		return "", fmt.Errorf("the browser worker runs on Linux amd64 / arm64 only")
	}
	binary := ChromiumPath(dir, arch)
	if info, err := os.Stat(binary); err == nil && info.Mode().IsRegular() {
		return binary, nil
	}
	var raw []byte
	var err error
	if archive != "" {
		raw, err = os.ReadFile(archive)
	} else {
		raw, err = download(ctx, arch)
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != build.SHA256 {
		return "", fmt.Errorf("Chromium %s %s: SHA-256 mismatch, refusing to install", ChromiumVersion, build.Platform)
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(dir, ".chromium-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err = unzip(raw, staging); err != nil {
		return "", err
	}
	final := filepath.Join(dir, "chromium-"+ChromiumVersion)
	os.RemoveAll(final)
	if err = os.Rename(staging, final); err != nil {
		return "", err
	}
	if err = os.Chmod(final, 0755); err != nil {
		return "", err
	}
	return binary, nil
}

func download(ctx context.Context, arch string) ([]byte, error) {
	address, err := ChromiumURL(arch)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", address, err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: HTTP %d", address, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxArchive+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxArchive {
		return nil, errors.New("Chromium archive too large")
	}
	return raw, nil
}

// unzip extracts regular files and directories only; no symlinks, no paths
// that leave dest, no setuid bits.
func unzip(raw []byte, dest string) error {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	root := filepath.Clean(dest) + string(os.PathSeparator)
	for _, file := range reader.File {
		target := filepath.Join(dest, file.Name)
		if !strings.HasPrefix(target, root) {
			return fmt.Errorf("archive entry %q leaves the install directory", file.Name)
		}
		mode := file.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("archive entry %q is not a regular file", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		perm := os.FileMode(0644)
		if mode&0111 != 0 {
			perm = 0755
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			source.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(source, int64(file.UncompressedSize64)+1))
		source.Close()
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// MissingLibraries lists the shared libraries the dynamic linker cannot find
// for binary (from ldd), sorted; the installer maps them to distro packages.
func MissingLibraries(binary string) ([]string, error) {
	output, err := exec.Command("ldd", binary).CombinedOutput()
	if err != nil && len(output) == 0 {
		return nil, fmt.Errorf("ldd %s: %w", binary, err)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "not found") {
			if fields := strings.Fields(line); len(fields) > 0 {
				set[fields[0]] = true
			}
		}
	}
	if !FontsInstalled() {
		// Not a library, but just as necessary: see FontsInstalled.
		set["fonts"] = true
	}
	missing := make([]string, 0, len(set))
	for name := range set {
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing, nil
}

// FontsInstalled reports whether fontconfig has a configuration and at least
// one font. headless-shell links its own fontconfig, so ldd cannot see this
// dependency — but without it the renderer stalls as soon as a page loads web
// fonts (Google's homepage never reaches DOMContentLoaded). Minimal server
// images often have no fonts at all.
func FontsInstalled() bool {
	if _, err := os.Stat("/etc/fonts/fonts.conf"); err != nil {
		return false
	}
	for _, dir := range []string{"/usr/share/fonts", "/usr/local/share/fonts"} {
		found := false
		filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || found {
				return filepath.SkipDir
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ttf", ".otf", ".ttc", ".pfb":
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if found {
			return true
		}
	}
	return false
}
