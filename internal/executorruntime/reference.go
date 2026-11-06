package executorruntime

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

//go:embed reference_driver.py
var referenceDriver string

func runReferenceOperation(ctx context.Context, request referenceRequest) (referenceResult, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		return referenceResult{}, errors.New("runtime installation and selection require root on Linux amd64")
	}
	interpreter, err := filepath.EvalSymlinks("/usr/bin/python3")
	if err != nil {
		return referenceResult{}, err
	}
	if err := trustedFile(interpreter); err != nil {
		return referenceResult{}, err
	}
	if err := trustedFile(request.Plan.Options.ReferencePath); err != nil {
		return referenceResult{}, err
	}
	raw, err := ipsentinel.CanonicalJSON(request)
	if err != nil {
		return referenceResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, interpreter, "-I", "-c", referenceDriver)
	command.Stdin = bytes.NewReader(raw)
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 2 * time.Second
	var output boundedOutput
	command.Stdout, command.Stderr = &output, io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return referenceResult{}, ctx.Err()
		}
		return referenceResult{}, errors.New("trusted runtime operation was rejected or interrupted; service start and database restoration were not requested")
	}
	var result referenceResult
	if err := ipsentinel.DecodeStrict(output.Bytes(), &result); err != nil {
		return referenceResult{}, errors.New("trusted runtime operation returned an invalid receipt")
	}
	return result, nil
}

type boundedOutput struct {
	buffer bytes.Buffer
}

func (output *boundedOutput) Write(raw []byte) (int, error) {
	if len(raw)+output.Len() > 64<<10 {
		return 0, errors.New("trusted runtime operation output exceeded its limit")
	}
	return output.buffer.Write(raw)
}

func (output *boundedOutput) Bytes() []byte {
	return output.buffer.Bytes()
}

func (output *boundedOutput) Len() int {
	return output.buffer.Len()
}

func trustedFile(name string) error {
	if !absoluteClean(name) {
		return errors.New("trusted runtime helper requires an absolute canonical path")
	}
	for current := name; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("trusted runtime helper and its ancestors must be root-owned, without symlinks or other-user write access")
		}
		if current == name {
			if !info.Mode().IsRegular() {
				return errors.New("trusted runtime helper must be a regular file")
			}
		} else if !info.IsDir() {
			return errors.New("trusted runtime helper ancestor must be a directory")
		}
		if current == "/" {
			return nil
		}
	}
}
