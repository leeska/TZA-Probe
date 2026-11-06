package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

func runOperationCommand(ctx context.Context, arguments []string, output, diagnostics io.Writer, platform string) error {
	command := arguments[0]
	action := strings.TrimSuffix(command, "-plan")
	flags := flag.NewFlagSet("executor-runtime "+command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	storeDir := flags.String("store", "", "private local staging directory")
	stageDigest := flags.String("stage-plan-sha256", "", "previously staged plan SHA-256")
	keysPath := flags.String("cc-keys", "", "locally pinned CC signing public keys")
	bindingPath := flags.String("binding", "", "locally provisioned controller/node UUID JSON")
	probeVersion := flags.String("probe-version", "", "declared Probe release version")
	agentVersion := flags.String("agent-version", "", "declared Agent release version")
	approvalPath := flags.String("approval", "", "separate approval for this installation or selection operation")
	root := flags.String("install-root", executorruntime.DefaultInstallRoot, "locally provisioned immutable runtime root")
	reference := flags.String("trusted-installer", executorruntime.DefaultReferenceInstaller, "independently provisioned, hash-pinned IPSentinel reference installer")
	state := flags.String("state-dir", "", "existing local executor state directory (select only)")
	config := flags.String("runtime-config", "", "approved local executor config (select only)")
	policy := flags.String("runtime-policy", "", "approved local executor policy (select only)")
	unit := flags.String("unit", "", "stopped IPSentinel service (select only)")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || (strings.HasSuffix(command, "-plan") && *approvalPath != "") {
		return errors.New("operation plans are read-only and do not accept approval files or positional arguments")
	}
	keyRaw, err := readLocal(*keysPath, 64<<10)
	if err != nil {
		return err
	}
	keys, err := parseKeys(keyRaw)
	if err != nil {
		return err
	}
	bindingRaw, err := readLocal(*bindingPath, 4096)
	if err != nil {
		return err
	}
	var target ipsentinel.Target
	if err := ipsentinel.DecodeStrict(bindingRaw, &target); err != nil {
		return err
	}
	host := executorruntime.Host{
		Platform: platform, ProbeVersion: strings.TrimPrefix(*probeVersion, "v"),
		AgentVersion: strings.TrimPrefix(*agentVersion, "v"),
	}
	if action == "select" && *unit == "" {
		*unit = "ipsentinel-runtime.service"
	}
	options := executorruntime.OperationOptions{
		Action: action, InstallRoot: *root, ReferencePath: *reference, StateDir: *state,
		ConfigPath: *config, PolicyPath: *policy, Unit: *unit,
	}
	store := executorruntime.Store{Dir: *storeDir}
	var result any
	if strings.HasSuffix(command, "-plan") {
		result, err = executorruntime.PrepareOperation(ctx, store, *stageDigest, keys, host, target, options)
	} else {
		approval, readErr := readLocal(*approvalPath, 4096)
		if readErr != nil {
			return readErr
		}
		result, err = executorruntime.ApplyOperation(ctx, store, *stageDigest, keys, host, target, options, approval)
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
