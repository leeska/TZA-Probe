package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output, diagnostics io.Writer) error {
	return runOnHost(ctx, arguments, output, diagnostics, runtime.GOOS+"-"+runtime.GOARCH)
}

func runOnHost(ctx context.Context, arguments []string, output, diagnostics io.Writer, platform string) error {
	if len(arguments) == 0 || (arguments[0] != "plan" && arguments[0] != "stage" && arguments[0] != "verify") {
		return errors.New("usage: executor-runtime plan|stage|verify [flags]; staging never activates or executes a runtime")
	}
	command := arguments[0]
	flags := flag.NewFlagSet("executor-runtime "+command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	archivePath := flags.String("archive", "", "local CC-signed executor declaration archive")
	signaturePath := flags.String("signature", "", "local CC signature JSON")
	keysPath := flags.String("cc-keys", "", "local pinned CC public keys, one key-id:base64 per line")
	bindingPath := flags.String("binding", "", "local controller_id/node_id UUID JSON")
	probeVersion := flags.String("probe-version", "", "declared Probe release version X.Y.Z")
	agentVersion := flags.String("agent-version", "", "declared node Agent release version X.Y.Z")
	storePath := flags.String("store", "", "private local staging directory")
	artifactsPath := flags.String("artifacts", "", "local artifact directory containing SHA256.bin files")
	ccURL := flags.String("cc-url", "", "configured TZA-CC HTTPS repository URL")
	releaseVersion := flags.String("release-version", "", "exact published executor version to stage from CC")
	approvalPath := flags.String("approval", "", "explicitly approved plan JSON")
	planDigest := flags.String("plan-sha256", "", "staged plan SHA-256 to verify")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected runtime arguments")
	}
	keysRaw, err := readLocal(*keysPath, 64<<10)
	if err != nil {
		return fmt.Errorf("CC public keys: %w", err)
	}
	keys, err := parseKeys(keysRaw)
	if err != nil {
		return err
	}
	bindingRaw, err := readLocal(*bindingPath, 4096)
	if err != nil {
		return fmt.Errorf("node binding: %w", err)
	}
	var target ipsentinel.Target
	if err := ipsentinel.DecodeStrict(bindingRaw, &target); err != nil {
		return err
	}
	host := executorruntime.Host{
		Platform:     platform,
		ProbeVersion: strings.TrimPrefix(*probeVersion, "v"),
		AgentVersion: strings.TrimPrefix(*agentVersion, "v"),
	}
	store := executorruntime.Store{Dir: *storePath}
	var result any
	if command == "verify" {
		if *archivePath != "" || *signaturePath != "" || *artifactsPath != "" || *approvalPath != "" || *ccURL != "" || *releaseVersion != "" {
			return errors.New("verify reads the staged bundle; input archive, signature, artifacts, approval and repository flags are not accepted")
		}
		result, err = store.Verify(ctx, *planDigest, keys, host, target)
	} else {
		if *planDigest != "" {
			return errors.New("plan-sha256 is only accepted by verify")
		}
		var repository *executorruntime.Repository
		var release *executorruntime.Release
		if *ccURL != "" {
			if *archivePath != "" || *signaturePath != "" || !tzapkg.ValidVersion(*releaseVersion) {
				return errors.New("CC staging requires --release-version and does not accept local archive or signature files")
			}
			repository, err = executorruntime.NewRepository(*ccURL, keys)
			if err != nil {
				return err
			}
			release, err = repository.Fetch(ctx, *releaseVersion, host, target)
			if err != nil {
				return err
			}
		} else {
			if *releaseVersion != "" {
				return errors.New("--release-version requires --cc-url")
			}
			archive, readErr := readLocal(*archivePath, tzapkg.MaxUpload)
			if readErr != nil {
				return fmt.Errorf("runtime archive: %w", readErr)
			}
			signature, readErr := readLocal(*signaturePath, 4096)
			if readErr != nil {
				return fmt.Errorf("runtime signature: %w", readErr)
			}
			release, err = executorruntime.Inspect(archive, signature, keys, host, target)
			if err != nil {
				return err
			}
		}
		if command == "plan" {
			if *storePath != "" || *artifactsPath != "" || *approvalPath != "" {
				return errors.New("plan is read-only; store, artifacts and approval flags are not accepted")
			}
			result = release.Review()
		} else {
			approval, readErr := readLocal(*approvalPath, 4096)
			if readErr != nil {
				return fmt.Errorf("runtime approval: %w", readErr)
			}
			if repository != nil {
				if *artifactsPath != "" {
					return errors.New("CC staging fetches signed artifacts from CC and does not accept --artifacts")
				}
				result, err = repository.Stage(ctx, store, release, approval)
			} else {
				result, err = store.Stage(ctx, release, approval, *artifactsPath)
			}
		}
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func readLocal(name string, maximum int64) ([]byte, error) {
	if name == "" {
		return nil, errors.New("a local file is required")
	}
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.New("input must be a bounded regular file without a symlink")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("input file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, errors.New("cannot read bounded local input")
	}
	return raw, nil
}

func parseKeys(raw []byte) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		id, key, err := tzapkg.ParsePublicKey(line)
		if err != nil {
			return nil, err
		}
		if _, exists := keys[id]; exists {
			return nil, errors.New("duplicate pinned CC signing key ID")
		}
		keys[id] = key
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("at least one locally pinned CC signing key is required")
	}
	return keys, nil
}
