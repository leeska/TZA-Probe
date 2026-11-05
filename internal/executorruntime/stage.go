package executorruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type Receipt struct {
	FormatVersion int       `json:"format_version"`
	State         string    `json:"state"`
	Activated     bool      `json:"activated"`
	PlanSHA256    string    `json:"plan_sha256"`
	Plan          Plan      `json:"plan"`
	StagedAt      time.Time `json:"staged_at"`
}

type Store struct {
	Dir string
}

func (store Store) Stage(ctx context.Context, release *Release, approval []byte, artifactDir string) (Receipt, error) {
	info, err := os.Lstat(artifactDir)
	if err != nil {
		return Receipt{}, err
	}
	if !info.IsDir() {
		return Receipt{}, errors.New("runtime artifact source must be a directory without a symlink")
	}
	source, err := os.OpenRoot(artifactDir)
	if err != nil {
		return Receipt{}, err
	}
	defer source.Close()
	return store.stage(ctx, release, approval, func(_ context.Context, artifact Artifact) (io.ReadCloser, error) {
		return openRegular(source, artifact.SHA256+".bin", artifact.SizeBytes)
	})
}

type artifactOpener func(context.Context, Artifact) (io.ReadCloser, error)

func (store Store) stage(ctx context.Context, release *Release, approval []byte, openArtifact artifactOpener) (Receipt, error) {
	if release == nil || release.digest == "" {
		return Receipt{}, errors.New("a verified runtime release is required")
	}
	if err := release.approve(approval); err != nil {
		return Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	root, err := store.open(true)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	for _, directory := range []string{"staged", "locks", "pending"} {
		if err := privateSubdirectory(root, directory); err != nil {
			return Receipt{}, err
		}
	}
	lock := path.Join("locks", release.digest)
	if err := root.Mkdir(lock, 0o700); err != nil {
		return Receipt{}, errors.New("runtime plan is locked; inspect the local operation before retrying")
	}
	defer root.Remove(lock)
	destination := path.Join("staged", release.digest)
	if _, err := root.Lstat(destination); err == nil {
		receipt, err := verifyStaged(ctx, root, release, destination)
		if err != nil {
			return Receipt{}, err
		}
		if err := syncDirectory(root, "staged"); err != nil {
			return Receipt{}, err
		}
		return receipt, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, err
	}
	pending := path.Join("pending", rand.Text())
	if err := root.Mkdir(pending, 0o700); err != nil {
		return Receipt{}, err
	}
	defer root.RemoveAll(pending)
	bundle, err := root.OpenRoot(pending)
	if err != nil {
		return Receipt{}, err
	}
	defer bundle.Close()
	if err := bundle.Mkdir("artifacts", 0o700); err != nil {
		return Receipt{}, err
	}
	seen := map[string]bool{}
	for _, artifact := range release.plan.Artifacts {
		if seen[artifact.SHA256] {
			continue
		}
		if err := copyArtifact(ctx, openArtifact, bundle, artifact); err != nil {
			return Receipt{}, err
		}
		seen[artifact.SHA256] = true
	}
	receipt := Receipt{
		FormatVersion: 1, State: "staged", Activated: false,
		PlanSHA256: release.digest, Plan: release.Review().Plan, StagedAt: time.Now().UTC(),
	}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	for _, metadata := range []struct {
		name string
		body []byte
	}{
		{"archive.zip", release.archive}, {"signature.json", release.signature},
		{"approval.json", approval}, {"receipt.json", receiptRaw},
	} {
		if err := writeNew(bundle, metadata.name, metadata.body); err != nil {
			return Receipt{}, err
		}
	}
	if err := syncDirectory(bundle, "artifacts"); err != nil {
		return Receipt{}, err
	}
	if err := syncDirectory(bundle, "."); err != nil {
		return Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := root.Rename(pending, destination); err != nil {
		return Receipt{}, err
	}
	if err := syncDirectory(root, "staged"); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (store Store) Verify(ctx context.Context, digest string, keys map[string]ed25519.PublicKey, host Host, target ipsentinel.Target) (Receipt, error) {
	if !validDigest(digest) {
		return Receipt{}, errors.New("invalid runtime plan SHA-256")
	}
	root, err := store.open(false)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	if err := privateDirectory(root, "staged"); err != nil {
		return Receipt{}, err
	}
	destination := path.Join("staged", digest)
	if err := privateDirectory(root, destination); err != nil {
		return Receipt{}, err
	}
	bundle, err := root.OpenRoot(destination)
	if err != nil {
		return Receipt{}, err
	}
	defer bundle.Close()
	archive, err := readMetadata(bundle, "archive.zip", tzapkg.MaxUpload)
	if err != nil {
		return Receipt{}, err
	}
	signature, err := readMetadata(bundle, "signature.json", 4096)
	if err != nil {
		return Receipt{}, err
	}
	release, err := Inspect(archive, signature, keys, host, target)
	if err != nil {
		return Receipt{}, err
	}
	if release.digest != digest {
		return Receipt{}, errors.New("staged runtime does not match this node or host versions")
	}
	return verifyStaged(ctx, root, release, destination)
}

func (store Store) open(create bool) (*os.Root, error) {
	if store.Dir == "" {
		return nil, errors.New("a local runtime staging directory is required")
	}
	if create {
		if err := os.MkdirAll(store.Dir, 0o700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(store.Dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("runtime staging directory must be a private directory without a symlink")
	}
	root, err := os.OpenRoot(store.Dir)
	if err != nil {
		return nil, err
	}
	if err := privateDirectory(root, "."); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func privateDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("runtime directory %s must be private and must not be a symlink", name)
	}
	return nil
}

func privateSubdirectory(root *os.Root, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return privateDirectory(root, name)
}

func openRegular(root *os.Root, name string, maximum int64) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("runtime file %s must be a bounded regular file without a symlink", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return nil, errors.New("runtime file changed while opening")
	}
	return file, nil
}

func readMetadata(root *os.Root, name string, maximum int64) ([]byte, error) {
	file, err := openRegular(root, name, maximum)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, fmt.Errorf("cannot read bounded runtime metadata %s", name)
	}
	return raw, nil
}

func writeNew(root *os.Root, name string, body []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	syncErr := file.Sync()
	modeErr := file.Chmod(0o400)
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, modeErr, closeErr)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func copyArtifact(ctx context.Context, openArtifact artifactOpener, bundle *os.Root, artifact Artifact) error {
	file, err := openArtifact(ctx, artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	output, err := bundle.OpenFile(path.Join("artifacts", artifact.SHA256+".bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(contextReader{ctx, file}, artifact.SizeBytes+1))
	syncErr := output.Sync()
	modeErr := output.Chmod(0o400)
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, modeErr, closeErr); err != nil {
		return err
	}
	if written != artifact.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return artifactError(artifact)
	}
	return nil
}

func verifyStaged(ctx context.Context, root *os.Root, release *Release, destination string) (Receipt, error) {
	if err := privateDirectory(root, destination); err != nil {
		return Receipt{}, err
	}
	bundle, err := root.OpenRoot(destination)
	if err != nil {
		return Receipt{}, err
	}
	defer bundle.Close()
	if err := privateDirectory(bundle, "artifacts"); err != nil {
		return Receipt{}, err
	}
	archive, err := readMetadata(bundle, "archive.zip", tzapkg.MaxUpload)
	if err != nil {
		return Receipt{}, err
	}
	archiveHash := sha256.Sum256(archive)
	if hex.EncodeToString(archiveHash[:]) != release.plan.ArchiveSHA256 {
		return Receipt{}, errors.New("staged runtime archive has changed")
	}
	signature, err := readMetadata(bundle, "signature.json", 4096)
	if err != nil {
		return Receipt{}, err
	}
	var storedSignature, expectedSignature tzapkg.Signature
	if ipsentinel.DecodeStrict(signature, &storedSignature) != nil || ipsentinel.DecodeStrict(release.signature, &expectedSignature) != nil || storedSignature != expectedSignature {
		return Receipt{}, errors.New("staged runtime signature has changed")
	}
	approval, err := readMetadata(bundle, "approval.json", 4096)
	if err != nil {
		return Receipt{}, err
	}
	if err := release.approve(approval); err != nil {
		return Receipt{}, err
	}
	raw, err := readMetadata(bundle, "receipt.json", 1<<20)
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := ipsentinel.DecodeStrict(raw, &receipt); err != nil {
		return Receipt{}, err
	}
	digest, err := ipsentinel.CanonicalSHA256(receipt.Plan)
	if err != nil || digest != release.digest || receipt.PlanSHA256 != release.digest || receipt.FormatVersion != 1 || receipt.State != "staged" || receipt.Activated || receipt.StagedAt.IsZero() {
		return Receipt{}, errors.New("staged runtime receipt differs from the approved plan")
	}
	seen := map[string]bool{}
	for _, artifact := range release.plan.Artifacts {
		if seen[artifact.SHA256] {
			continue
		}
		file, err := openRegular(bundle, path.Join("artifacts", artifact.SHA256+".bin"), artifact.SizeBytes)
		if err != nil {
			return Receipt{}, err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, artifact.SizeBytes+1))
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return Receipt{}, err
		}
		if size != artifact.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
			return Receipt{}, artifactError(artifact)
		}
		seen[artifact.SHA256] = true
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func syncDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
