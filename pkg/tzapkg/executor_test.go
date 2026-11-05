package tzapkg

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"testing"
)

func executorFixture(t *testing.T) ExecutorManifest {
	t.Helper()
	raw, err := os.ReadFile("testdata/runtime/executor.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseExecutor(raw)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func executorArchive(t *testing.T, manifest ExecutorManifest, extra bool) []byte {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("executor.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if extra {
		file, _ = writer.Create("postinstall.sh")
		file.Write([]byte("exit 0"))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExecutorManifestUsesAuthoritativeContractAndSeparatePackageType(t *testing.T) {
	manifest := executorFixture(t)
	archive := executorArchive(t, manifest, false)
	info, err := Inspect(archive)
	if err != nil || info.Type != TypeExecutor || info.Kind != "ipsentinel" || len(info.Permissions) != 4 || len(manifest.Artifacts()) != 4 {
		t.Fatal(info, err)
	}
	if _, _, err := Open(archive); err == nil {
		t.Fatal("runtime installed as a theme")
	}
	if _, err := OpenPlugin(archive); err == nil {
		t.Fatal("runtime installed as a plugin")
	}
	if _, err := OpenExecutor(executorArchive(t, manifest, true)); err == nil {
		t.Fatal("postinstall file accepted")
	}
}

func TestExecutorSignatureCoversDescriptorDependenciesAndPermissions(t *testing.T) {
	manifest := executorFixture(t)
	archive := executorArchive(t, manifest, false)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature := Sign(private, "official", manifest.ID, manifest.Version, archive)
	keys := map[string]ed25519.PublicKey{"official": public}
	if err := signature.Verify(keys, archive); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ExecutorManifest){
		func(manifest *ExecutorManifest) { manifest.Compatibility.AgentMin = "1.8.0" },
		func(manifest *ExecutorManifest) {
			manifest.Dependencies[0].Artifacts[0].SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		func(manifest *ExecutorManifest) {
			manifest.Descriptor = bytes.Replace(manifest.Descriptor, []byte("geolocation.override"), []byte("profile.ephemeral"), 1)
		},
	} {
		changed := executorFixture(t)
		mutate(&changed)
		if err := signature.Verify(keys, executorArchive(t, changed, false)); err == nil {
			t.Fatal("modified runtime declaration retained its signature")
		}
	}
}

func TestExecutorRejectsUnpinnedDependenciesAndExpandedEntrypoint(t *testing.T) {
	for _, mutate := range []func(*ExecutorManifest){
		func(manifest *ExecutorManifest) { manifest.ID = "arbitrary-runtime" },
		func(manifest *ExecutorManifest) { manifest.Compatibility.AgentMin = "1.5.0" },
		func(manifest *ExecutorManifest) { manifest.Compatibility.Python = "3.13" },
		func(manifest *ExecutorManifest) { manifest.Dependencies = nil },
		func(manifest *ExecutorManifest) { manifest.Dependencies[0].Artifacts[0].SizeBytes++ },
		func(manifest *ExecutorManifest) {
			manifest.Descriptor = bytes.Replace(manifest.Descriptor, []byte("ipsentinel-control"), []byte("/bin/sh"), 1)
		},
		func(manifest *ExecutorManifest) {
			manifest.Descriptor = bytes.Replace(manifest.Descriptor, []byte("linux-amd64"), []byte("darwin-arm64"), -1)
		},
		func(manifest *ExecutorManifest) { manifest.Version = "0.4.0-design" },
	} {
		manifest := executorFixture(t)
		mutate(&manifest)
		raw, _ := json.Marshal(manifest)
		if _, err := ParseExecutor(raw); err == nil {
			t.Fatal("invalid runtime accepted", string(raw))
		}
	}
	raw, _ := json.Marshal(executorFixture(t))
	raw = append(raw[:len(raw)-1], []byte(`,"postinstall":"curl | sh"}`)...)
	if _, err := ParseExecutor(raw); err == nil {
		t.Fatal("arbitrary command field accepted")
	}
}
