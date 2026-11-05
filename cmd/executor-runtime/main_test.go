package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

func localFixture(t *testing.T) ([]string, string) {
	t.Helper()
	directory := t.TempDir()
	raw, err := os.ReadFile("../../pkg/tzapkg/testdata/runtime/executor.json")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("executor.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := json.Marshal(tzapkg.Sign(private, "test-only", tzapkg.OfficialExecutorID, "0.4.0", archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := json.Marshal(ipsentinel.Target{
		ControllerID: "22222222-2222-4222-8222-222222222222",
		NodeID:       "33333333-3333-4333-8333-333333333333",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"archive.zip": archive.Bytes(), "signature.json": signature, "binding.json": binding,
		"cc-keys": []byte(tzapkg.FormatPublicKey("test-only", public) + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime", "python", "chromium", "libraries"} {
		body := []byte("fixture " + name + ", no executable\n")
		digest := sha256.Sum256(body)
		if err := os.WriteFile(filepath.Join(directory, "artifacts", hex.EncodeToString(digest[:])+".bin"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{
		"--archive", filepath.Join(directory, "archive.zip"),
		"--signature", filepath.Join(directory, "signature.json"),
		"--cc-keys", filepath.Join(directory, "cc-keys"),
		"--binding", filepath.Join(directory, "binding.json"),
		"--probe-version", "v1.9.2", "--agent-version", "v1.9.2",
	}, directory
}

func TestRuntimeCLIPlanStageAndReverify(t *testing.T) {
	common, directory := localFixture(t)
	var output, diagnostics bytes.Buffer
	if err := runOnHost(context.Background(), append([]string{"plan"}, common...), &output, &diagnostics, "linux-amd64"); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	var review executorruntime.Review
	if err := json.Unmarshal(output.Bytes(), &review); err != nil || review.ApprovalTemplate.Approved {
		t.Fatal(review, err)
	}
	approvalPath := filepath.Join(directory, "approval.json")
	storePath := filepath.Join(directory, "store")
	stageArguments := append([]string{"stage"}, common...)
	stageArguments = append(stageArguments, "--approval", approvalPath, "--store", storePath, "--artifacts", filepath.Join(directory, "artifacts"))
	declined, _ := json.Marshal(review.ApprovalTemplate)
	if err := os.WriteFile(approvalPath, declined, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runOnHost(context.Background(), stageArguments, &output, &diagnostics, "linux-amd64"); err == nil || output.Len() != 0 {
		t.Fatal("unapproved CLI stage succeeded", err)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatal("unapproved stage created storage", err)
	}
	review.ApprovalTemplate.Approved = true
	approval, _ := json.Marshal(review.ApprovalTemplate)
	if err := os.WriteFile(approvalPath, approval, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runOnHost(context.Background(), stageArguments, &output, &diagnostics, "linux-amd64"); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	var receipt executorruntime.Receipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil || receipt.Activated || receipt.State != "staged" {
		t.Fatal(receipt, err)
	}
	verifyArguments := []string{
		"verify", "--cc-keys", filepath.Join(directory, "cc-keys"), "--binding", filepath.Join(directory, "binding.json"),
		"--probe-version", "1.9.2", "--agent-version", "1.9.2", "--store", storePath, "--plan-sha256", review.PlanSHA256,
	}
	output.Reset()
	if err := runOnHost(context.Background(), verifyArguments, &output, &diagnostics, "linux-amd64"); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	artifact := review.Plan.Artifacts[0]
	staged := filepath.Join(storePath, "staged", review.PlanSHA256, "artifacts", artifact.SHA256+".bin")
	if err := os.Chmod(staged, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("changed"), 0o400); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runOnHost(context.Background(), verifyArguments, &output, &diagnostics, "linux-amd64"); err == nil || output.Len() != 0 {
		t.Fatal("tampered CLI stage verified", err)
	}
}

func TestRuntimeCLINativePlatformAndNoActivationCommand(t *testing.T) {
	common, _ := localFixture(t)
	var output, diagnostics bytes.Buffer
	err := run(context.Background(), append([]string{"plan"}, common...), &output, &diagnostics)
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil || output.Len() != 0 {
		t.Fatal("native incompatible host accepted")
	}
	for _, arguments := range [][]string{nil, {"activate"}, {"install"}, {"plan", "--unexpected"}} {
		output.Reset()
		if err := run(context.Background(), arguments, &output, &diagnostics); err == nil || output.Len() != 0 {
			t.Fatal("unsupported CLI operation succeeded", arguments, err)
		}
	}
	for _, arguments := range [][]string{
		append(append([]string{"plan"}, common...), "--store", t.TempDir()),
		append(append([]string{"stage"}, common...), "--plan-sha256", "bad"),
		append(append([]string{"verify"}, common...), "--plan-sha256", "bad"),
		append(append([]string{"plan"}, common...), "extra"),
	} {
		output.Reset()
		if err := runOnHost(context.Background(), arguments, &output, &diagnostics, "linux-amd64"); err == nil || output.Len() != 0 {
			t.Fatal("unexpected flags accepted", arguments, err)
		}
	}
}

func TestRuntimeCLIBoundedLocalFilesAndPinnedKeys(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "file")
	if err := os.WriteFile(file, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocal(file, 4); err == nil {
		t.Fatal("oversized input accepted")
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocal(link, 10); err == nil {
		t.Fatal("symlink input accepted")
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := tzapkg.FormatPublicKey("test-only", public)
	for _, raw := range []string{"", "invalid", key + "\n" + key} {
		if _, err := parseKeys([]byte(raw)); err == nil {
			t.Fatal("invalid pinned keys accepted")
		}
	}
	if keys, err := parseKeys([]byte("\n" + key + "\n")); err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
}
