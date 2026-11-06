package executorruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

const ReferenceInstallerSHA256 = "8df4220b5a6ada13e9c4f238849a4dc691fc3e1170b2645784d5c0bfdcd96640"
const DefaultReferenceInstaller = "/usr/local/libexec/tza/ipsentinel-install-runtime.py"
const DefaultInstallRoot = "/opt/ipsentinel-runtime"

var executorUnit = regexp.MustCompile(`^ipsentinel-[A-Za-z0-9@_.-]+\.service$`)

type OperationOptions struct {
	Action        string `json:"action"`
	InstallRoot   string `json:"install_root"`
	ReferencePath string `json:"reference_installer"`
	StateDir      string `json:"state_dir,omitempty"`
	ConfigPath    string `json:"config_path,omitempty"`
	PolicyPath    string `json:"policy_path,omitempty"`
	Unit          string `json:"unit,omitempty"`
}

type OperationPlan struct {
	FormatVersion    int               `json:"format_version"`
	Options          OperationOptions  `json:"options"`
	Target           ipsentinel.Target `json:"target"`
	StagePlanSHA256  string            `json:"stage_plan_sha256"`
	ArchiveSHA256    string            `json:"archive_sha256"`
	Version          string            `json:"version"`
	InstallerSHA256  string            `json:"installer_sha256"`
	SigningKeyID     string            `json:"signing_key_id"`
	SigningKeySHA256 string            `json:"signing_key_sha256"`
	ExpectedCurrent  string            `json:"expected_current"`
	SelectionSHA256  string            `json:"selection_sha256"`
	ConfigSHA256     string            `json:"config_sha256,omitempty"`
	PolicySHA256     string            `json:"policy_sha256,omitempty"`
}

type OperationApproval struct {
	FormatVersion int    `json:"format_version"`
	Action        string `json:"action"`
	PlanSHA256    string `json:"plan_sha256"`
	Approved      bool   `json:"approved"`
}

type OperationReview struct {
	Plan             OperationPlan     `json:"plan"`
	PlanSHA256       string            `json:"plan_sha256"`
	ApprovalTemplate OperationApproval `json:"approval_template"`
}

type OperationReceipt struct {
	FormatVersion int           `json:"format_version"`
	State         string        `json:"state"`
	Selected      bool          `json:"selected"`
	Activated     bool          `json:"activated"`
	PlanSHA256    string        `json:"plan_sha256"`
	Plan          OperationPlan `json:"plan"`
	Release       string        `json:"release"`
	Backup        string        `json:"backup,omitempty"`
}

func PrepareOperation(ctx context.Context, store Store, stageDigest string, keys map[string]ed25519.PublicKey, host Host, target ipsentinel.Target, options OperationOptions) (OperationReview, error) {
	if options.Action != "install" && options.Action != "select" {
		return OperationReview{}, errors.New("runtime operation must be install or select")
	}
	if options.InstallRoot == "" {
		options.InstallRoot = DefaultInstallRoot
	}
	if options.ReferencePath == "" {
		options.ReferencePath = DefaultReferenceInstaller
	}
	if !absoluteClean(options.InstallRoot) || options.InstallRoot == "/" || !absoluteClean(options.ReferencePath) {
		return OperationReview{}, errors.New("runtime installation and trusted installer require absolute canonical local paths")
	}
	if options.Action == "install" && (options.StateDir != "" || options.ConfigPath != "" || options.PolicyPath != "" || options.Unit != "") {
		return OperationReview{}, errors.New("install does not accept selection or service configuration")
	}
	if options.Action == "select" && (!absoluteClean(options.StateDir) || !absoluteClean(options.ConfigPath) || !absoluteClean(options.PolicyPath) || !executorUnit.MatchString(options.Unit)) {
		return OperationReview{}, errors.New("selection requires local state, config, policy paths and a fixed IPSentinel service")
	}
	staged, err := store.Verify(ctx, stageDigest, keys, host, target)
	if err != nil {
		return OperationReview{}, err
	}
	if staged.Plan.Host.Platform != "linux-amd64" {
		return OperationReview{}, errors.New("the pinned reference installer only supports the Debian 13 amd64 candidate")
	}
	installer, err := readLocalFile(options.ReferencePath, 128<<10)
	if err != nil {
		return OperationReview{}, err
	}
	if hashBytes(installer) != ReferenceInstallerSHA256 {
		return OperationReview{}, errors.New("local installer differs from the pinned IPSentinel reference implementation")
	}
	current, selection, err := selectionSnapshot(options.InstallRoot)
	if err != nil {
		return OperationReview{}, err
	}
	plan := OperationPlan{
		FormatVersion: 1, Options: options, Target: target, StagePlanSHA256: stageDigest,
		ArchiveSHA256: staged.Plan.ArchiveSHA256, Version: staged.Plan.Version,
		InstallerSHA256: ReferenceInstallerSHA256, SigningKeyID: staged.Plan.SigningKeyID,
		SigningKeySHA256: hashBytes(keys[staged.Plan.SigningKeyID]),
		ExpectedCurrent:  current, SelectionSHA256: selection,
	}
	if options.Action == "select" {
		config, err := readLocalFile(options.ConfigPath, 1<<20)
		if err != nil {
			return OperationReview{}, err
		}
		policy, err := readLocalFile(options.PolicyPath, 16384)
		if err != nil {
			return OperationReview{}, err
		}
		plan.ConfigSHA256, plan.PolicySHA256 = hashBytes(config), hashBytes(policy)
	}
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil {
		return OperationReview{}, err
	}
	return OperationReview{
		Plan: plan, PlanSHA256: digest,
		ApprovalTemplate: OperationApproval{FormatVersion: 1, Action: options.Action, PlanSHA256: digest},
	}, nil
}

func ApplyOperation(ctx context.Context, store Store, stageDigest string, keys map[string]ed25519.PublicKey, host Host, target ipsentinel.Target, options OperationOptions, approval []byte) (OperationReceipt, error) {
	return applyOperation(ctx, store, stageDigest, keys, host, target, options, approval, runReferenceOperation)
}

func InspectInstallation(ctx context.Context, plan OperationPlan) (*OperationReceipt, error) {
	if plan.Options.Action != "install" || !absoluteClean(plan.Options.InstallRoot) || !validDigest(plan.ArchiveSHA256) {
		return nil, errors.New("installation inspection requires an exact installation plan")
	}
	if err := trustedFile(filepath.Join(plan.Options.InstallRoot, ".install.lock")); err != nil {
		return nil, err
	}
	check := plan
	check.Options.Action = "inspect-install"
	result, err := runReferenceOperation(ctx, referenceRequest{Plan: check})
	if err != nil {
		return nil, err
	}
	if result.State == "not_applied" && result.Release == "" {
		return nil, nil
	}
	if result.State != "installed" || result.Selected == nil || *result.Selected || result.Release != filepath.Join(plan.Options.InstallRoot, "releases", plan.ArchiveSHA256) {
		return nil, errors.New("installation inspection did not confirm the approved release")
	}
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil {
		return nil, err
	}
	return &OperationReceipt{FormatVersion: 1, State: "installed", PlanSHA256: digest, Plan: plan, Release: result.Release}, nil
}

type referenceRequest struct {
	Plan      OperationPlan `json:"plan"`
	StagePath string        `json:"stage_path"`
	PublicKey string        `json:"public_key"`
}

type referenceResult struct {
	State     string `json:"state"`
	Selected  *bool  `json:"selected,omitempty"`
	Activated *bool  `json:"activated,omitempty"`
	Release   string `json:"release"`
	Backup    string `json:"backup,omitempty"`
}

type referenceRunner func(context.Context, referenceRequest) (referenceResult, error)

func applyOperation(ctx context.Context, store Store, stageDigest string, keys map[string]ed25519.PublicKey, host Host, target ipsentinel.Target, options OperationOptions, approval []byte, runner referenceRunner) (OperationReceipt, error) {
	review, err := PrepareOperation(ctx, store, stageDigest, keys, host, target, options)
	if err != nil {
		return OperationReceipt{}, err
	}
	var permission OperationApproval
	if len(approval) == 0 || len(approval) > 4096 || ipsentinel.DecodeStrict(approval, &permission) != nil || permission.FormatVersion != 1 || !permission.Approved || permission.Action != options.Action || permission.PlanSHA256 != review.PlanSHA256 {
		return OperationReceipt{}, errors.New("explicit approval of this exact runtime installation or selection operation is required; staging approval is insufficient")
	}
	if err := ctx.Err(); err != nil {
		return OperationReceipt{}, err
	}
	public := keys[review.Plan.SigningKeyID]
	if len(public) != ed25519.PublicKeySize || hashBytes(public) != review.Plan.SigningKeySHA256 {
		return OperationReceipt{}, errors.New("verified staging signing key is unavailable")
	}
	source, err := filepath.Abs(filepath.Join(store.Dir, "staged", stageDigest))
	if err != nil {
		return OperationReceipt{}, err
	}
	result, err := runner(ctx, referenceRequest{Plan: review.Plan, StagePath: source, PublicKey: tzapkg.FormatPublicKey(review.Plan.SigningKeyID, public)})
	if err != nil {
		return OperationReceipt{}, err
	}
	if options.Action == "install" {
		expected := filepath.Join(review.Plan.Options.InstallRoot, "releases", review.Plan.ArchiveSHA256)
		if result.State != "installed" || result.Selected == nil || *result.Selected || (result.Activated != nil && *result.Activated) || result.Release != expected {
			return OperationReceipt{}, errors.New("installer did not confirm the approved installation without selecting or activating it")
		}
	} else {
		if result.State != "selected" || result.Activated == nil || *result.Activated || result.Release != review.Plan.ArchiveSHA256 || !backupPath(review.Plan.Options.InstallRoot, result.Backup) {
			return OperationReceipt{}, errors.New("installer did not confirm the approved selection and backup without activating it")
		}
	}
	return OperationReceipt{
		FormatVersion: 1, State: result.State, Selected: result.State == "selected", Activated: false,
		PlanSHA256: review.PlanSHA256, Plan: review.Plan, Release: result.Release, Backup: result.Backup,
	}, nil
}

func absoluteClean(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, 0)
}

func hashBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func readLocalFile(name string, maximum int64) ([]byte, error) {
	if !absoluteClean(name) {
		return nil, errors.New("runtime operation input requires an absolute canonical local file")
	}
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readMetadata(root, filepath.Base(name), maximum)
}

func selectionSnapshot(root string) (string, string, error) {
	current := filepath.Join(root, "current")
	info, err := os.Lstat(current)
	pointer := ""
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", "", errors.New("current runtime must be a managed release symlink")
		}
		pointer, err = os.Readlink(current)
		if err != nil || !strings.HasPrefix(pointer, "releases/") || !validDigest(strings.TrimPrefix(pointer, "releases/")) {
			return "", "", errors.New("current runtime points outside the managed releases")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	selection, err := readLocalFile(filepath.Join(root, "selection.json"), 16384)
	if errors.Is(err, os.ErrNotExist) {
		return pointer, "", nil
	}
	if err != nil {
		return "", "", err
	}
	var transaction map[string]json.RawMessage
	if ipsentinel.DecodeStrict(selection, &transaction) != nil || !bytes.Equal(bytes.TrimSpace(transaction["state"]), []byte(`"committed"`)) {
		return "", "", errors.New("runtime selection is interrupted or invalid; explicit local recovery is required")
	}
	return pointer, hashBytes(selection), nil
}

func backupPath(root, name string) bool {
	if !absoluteClean(name) {
		return false
	}
	parent, base := filepath.Dir(name), filepath.Base(name)
	return parent == filepath.Join(root, "backups") && len(base) == 32 && validDigest(base+base)
}
