package executorruntime

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type Host struct {
	Platform     string `json:"platform"`
	ProbeVersion string `json:"probe_version"`
	AgentVersion string `json:"agent_version"`
}

type Artifact struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type Plan struct {
	FormatVersion    int                          `json:"format_version"`
	Target           ipsentinel.Target            `json:"target"`
	Package          string                       `json:"package"`
	Version          string                       `json:"version"`
	ArchiveSHA256    string                       `json:"archive_sha256"`
	DescriptorSHA256 string                       `json:"descriptor_sha256"`
	SigningKeyID     string                       `json:"signing_key_id"`
	Host             Host                         `json:"host"`
	Compatibility    tzapkg.ExecutorCompatibility `json:"compatibility"`
	Permissions      []string                     `json:"permissions"`
	Artifacts        []Artifact                   `json:"artifacts"`
	TotalBytes       int64                        `json:"total_bytes"`
}

type Approval struct {
	FormatVersion int    `json:"format_version"`
	PlanSHA256    string `json:"plan_sha256"`
	Approved      bool   `json:"approved"`
}

type Review struct {
	Plan             Plan     `json:"plan"`
	PlanSHA256       string   `json:"plan_sha256"`
	ApprovalTemplate Approval `json:"approval_template"`
}

type Release struct {
	plan      Plan
	digest    string
	archive   []byte
	signature []byte
}

func Inspect(archive, signature []byte, keys map[string]ed25519.PublicKey, host Host, target ipsentinel.Target) (*Release, error) {
	if len(archive) == 0 || len(archive) > tzapkg.MaxUpload || len(signature) == 0 || len(signature) > 4096 {
		return nil, errors.New("runtime archive or signature exceeds its metadata limit")
	}
	if !ipsentinel.ValidUUID(target.ControllerID) || !ipsentinel.ValidUUID(target.NodeID) {
		return nil, errors.New("a locally provisioned controller and node UUID are required")
	}
	if host.Platform != "linux-amd64" && host.Platform != "linux-arm64" {
		return nil, errors.New("runtime staging requires a supported Linux node")
	}
	var signed tzapkg.Signature
	if err := ipsentinel.DecodeStrict(signature, &signed); err != nil {
		return nil, errors.New("invalid runtime signature envelope")
	}
	if len(keys[signed.KeyID]) != ed25519.PublicKeySize {
		return nil, errors.New("runtime signing key is not pinned or is invalid")
	}
	if err := signed.Verify(keys, archive); err != nil {
		return nil, err
	}
	manifest, err := tzapkg.OpenExecutor(archive)
	if err != nil {
		return nil, err
	}
	if signed.Package != manifest.ID || signed.Version != manifest.Version {
		return nil, errors.New("signed runtime identity differs from executor.json")
	}
	if !versionAtLeast(host.ProbeVersion, manifest.Compatibility.ProbeMin) || !versionAtLeast(host.AgentVersion, manifest.Compatibility.AgentMin) {
		return nil, errors.New("declared Probe or Agent version does not meet the signed compatibility matrix")
	}
	var descriptor ipsentinel.RuntimeDescriptor
	if err := json.Unmarshal(manifest.Descriptor, &descriptor); err != nil {
		return nil, err
	}
	descriptorDigest, err := ipsentinel.CanonicalSHA256(descriptor)
	if err != nil {
		return nil, err
	}
	plan := Plan{
		FormatVersion: 1, Target: target, Package: manifest.ID, Version: manifest.Version,
		ArchiveSHA256: signed.SHA256, DescriptorSHA256: descriptorDigest, SigningKeyID: signed.KeyID,
		Host: host, Compatibility: manifest.Compatibility, Permissions: manifest.Permissions(),
		Artifacts: []Artifact{},
	}
	for _, artifact := range descriptor.RuntimeArtifacts {
		if artifact.Platform == host.Platform {
			plan.Artifacts = append(plan.Artifacts, Artifact{
				ID: "runtime", Kind: "runtime", Version: manifest.Version,
				SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes,
			})
		}
	}
	if len(plan.Artifacts) != 1 {
		return nil, errors.New("no signed runtime artifact for this node architecture")
	}
	for _, dependencies := range manifest.Dependencies {
		if dependencies.Platform == host.Platform {
			for _, artifact := range dependencies.Artifacts {
				plan.Artifacts = append(plan.Artifacts, Artifact(artifact))
			}
		}
	}
	seen := map[string]bool{}
	for _, artifact := range plan.Artifacts {
		if !seen[artifact.SHA256] {
			plan.TotalBytes += artifact.SizeBytes
			seen[artifact.SHA256] = true
		}
	}
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil {
		return nil, err
	}
	return &Release{plan: plan, digest: digest, archive: slices.Clone(archive), signature: slices.Clone(signature)}, nil
}

func (release *Release) Review() Review {
	plan := release.plan
	plan.Permissions = slices.Clone(plan.Permissions)
	plan.Artifacts = slices.Clone(plan.Artifacts)
	return Review{
		Plan: plan, PlanSHA256: release.digest,
		ApprovalTemplate: Approval{FormatVersion: 1, PlanSHA256: release.digest},
	}
}

func (release *Release) approve(raw []byte) error {
	var approval Approval
	if len(raw) == 0 || len(raw) > 4096 || ipsentinel.DecodeStrict(raw, &approval) != nil {
		return errors.New("invalid runtime approval")
	}
	if approval.FormatVersion != 1 || !approval.Approved || approval.PlanSHA256 != release.digest {
		return errors.New("explicit approval of this exact node, release, permissions and compatibility plan is required")
	}
	return nil
}

func versionAtLeast(actual, minimum string) bool {
	if !tzapkg.ValidVersion(actual) || !tzapkg.ValidVersion(minimum) {
		return false
	}
	actualParts, minimumParts := strings.Split(actual, "."), strings.Split(minimum, ".")
	for index := range actualParts {
		actualNumber, _ := strconv.Atoi(actualParts[index])
		minimumNumber, _ := strconv.Atoi(minimumParts[index])
		if actualNumber != minimumNumber {
			return actualNumber > minimumNumber
		}
	}
	return true
}

func validDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, character := range digest {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func artifactError(artifact Artifact) error {
	return fmt.Errorf("artifact %s (%s) differs from its signed size or SHA-256", artifact.ID, artifact.Kind)
}
