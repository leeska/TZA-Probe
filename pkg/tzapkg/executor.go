package tzapkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

const TypeExecutor = "executor"
const OfficialExecutorID = "ipsentinel-runtime"
const ExecutorMinProbe = "1.6.0"
const MaxRuntimeArtifact int64 = 1 << 30

type ExecutorCompatibility struct {
	CCFormat int    `json:"cc_format"`
	ProbeMin string `json:"probe_min"`
	AgentMin string `json:"agent_min"`
	Python   string `json:"python"`
}

type RuntimeDependency struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type RuntimeDependencies struct {
	Platform  string              `json:"platform"`
	Artifacts []RuntimeDependency `json:"artifacts"`
}

type ExecutorManifest struct {
	FormatVersion int                   `json:"format_version"`
	ID            string                `json:"id"`
	Type          string                `json:"type"`
	Name          string                `json:"name"`
	Hint          string                `json:"hint"`
	Version       string                `json:"version"`
	Author        string                `json:"author"`
	Descriptor    json.RawMessage       `json:"descriptor"`
	Compatibility ExecutorCompatibility `json:"compatibility"`
	Dependencies  []RuntimeDependencies `json:"dependencies"`
}

type RuntimeArtifactRef struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

var runtimeDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var pythonVersion = regexp.MustCompile(`^3\.(1[1-9]|[2-9][0-9])$`)

func ParseExecutor(raw []byte) (ExecutorManifest, error) {
	var manifest ExecutorManifest
	if err := ipsentinel.DecodeStrict(raw, &manifest); err != nil {
		return manifest, errors.New("executor.json 须为严格 JSON，不能包含命令、URL 或未知字段")
	}
	if manifest.FormatVersion != 1 || manifest.Type != TypeExecutor || manifest.ID != OfficialExecutorID || manifest.Name == "" || len(manifest.Name) > 120 || len(manifest.Hint) > 500 || len(manifest.Author) > 120 || !ValidVersion(manifest.Version) {
		return manifest, errors.New("仅接受格式 1 的官方 IPSentinel runtime 发行清单")
	}
	compat := manifest.Compatibility
	if compat.CCFormat != 1 || !ValidVersion(compat.ProbeMin) || !ValidVersion(compat.AgentMin) || !pythonVersion.MatchString(compat.Python) {
		return manifest, errors.New("runtime 兼容矩阵无效")
	}
	if versionLess(compat.ProbeMin, ExecutorMinProbe) || versionLess(compat.AgentMin, ExecutorMinProbe) {
		return manifest, errors.New("runtime 要求 Server 与 Agent 至少为 " + ExecutorMinProbe)
	}
	if err := ipsentinel.ValidateDescriptor(manifest.Descriptor); err != nil {
		return manifest, errors.New("runtime descriptor 不符合 IPSentinel 权威契约")
	}
	var descriptor ipsentinel.RuntimeDescriptor
	if err := ipsentinel.DecodeStrict(manifest.Descriptor, &descriptor); err != nil {
		return manifest, err
	}
	if descriptor.ExecutorID != "ipsentinel" || descriptor.RuntimeVersion != manifest.Version || descriptor.ContractVersion != ipsentinel.ContractVersion || !slices.Equal(descriptor.Protocols, []string{ipsentinel.Protocol}) {
		return manifest, errors.New("运行时版本、执行器或协议不一致")
	}
	if len(manifest.Dependencies) != len(descriptor.Platforms) || len(descriptor.RuntimeArtifacts) != len(descriptor.Platforms) {
		return manifest, errors.New("每个平台必须有且只有一份 runtime 和依赖清单")
	}
	platforms := map[string]bool{}
	for _, platform := range descriptor.Platforms {
		if !slices.Contains([]string{"linux-amd64", "linux-arm64"}, platform) || platforms[platform] {
			return manifest, errors.New("仅支持明确的 Linux amd64/arm64 制品")
		}
		platforms[platform] = true
	}
	dependencyDigests := map[string]string{}
	for _, dependencies := range manifest.Dependencies {
		if !platforms[dependencies.Platform] || dependencyDigests[dependencies.Platform] != "" || len(dependencies.Artifacts) == 0 || len(dependencies.Artifacts) > 32 {
			return manifest, errors.New("依赖平台缺失、重复或制品数量无效")
		}
		seen := map[string]bool{}
		kinds := map[string]bool{}
		for _, dependency := range dependencies.Artifacts {
			if !ValidID(dependency.ID) || seen[dependency.ID] || dependency.Version == "" || len(dependency.Version) > 120 || !runtimeDigest.MatchString(dependency.SHA256) || dependency.SizeBytes <= 0 || dependency.SizeBytes > MaxRuntimeArtifact || !slices.Contains([]string{"python", "chromium", "wheels", "libraries"}, dependency.Kind) {
				return manifest, errors.New("依赖制品身份、摘要、大小或类别无效")
			}
			if dependency.Kind == "python" && dependency.Version != compat.Python {
				return manifest, errors.New("Python 依赖版本与兼容矩阵不一致")
			}
			seen[dependency.ID], kinds[dependency.Kind] = true, true
		}
		if !kinds["python"] || !kinds["chromium"] || !kinds["libraries"] {
			return manifest, errors.New("须固定 Python、Chromium 和系统依赖制品")
		}
		digest, err := ipsentinel.CanonicalSHA256(dependencies)
		if err != nil {
			return manifest, err
		}
		dependencyDigests[dependencies.Platform] = digest
	}
	for _, artifact := range descriptor.RuntimeArtifacts {
		if !platforms[artifact.Platform] || dependencyDigests[artifact.Platform] != artifact.DependenciesSHA256 || artifact.Entrypoint != "ipsentinel-control" || artifact.SizeBytes <= 0 || artifact.SizeBytes > MaxRuntimeArtifact {
			return manifest, errors.New("runtime 制品必须固定入口、大小及同平台依赖摘要")
		}
		for _, dependencies := range manifest.Dependencies {
			if dependencies.Platform != artifact.Platform {
				continue
			}
			if !slices.ContainsFunc(dependencies.Artifacts, func(dependency RuntimeDependency) bool {
				return dependency.Kind == "chromium" && dependency.Version == artifact.BrowserRevision
			}) {
				return manifest, errors.New("Chromium 修订与依赖制品不一致")
			}
		}
		delete(platforms, artifact.Platform)
	}
	if len(platforms) != 0 {
		return manifest, errors.New("runtime 平台未完整覆盖")
	}
	sizes := map[string]int64{}
	for _, artifact := range manifest.Artifacts() {
		if previous, ok := sizes[artifact.SHA256]; ok && previous != artifact.SizeBytes {
			return manifest, errors.New("同一摘要不能对应不同制品大小")
		}
		sizes[artifact.SHA256] = artifact.SizeBytes
	}
	return manifest, nil
}

func versionLess(left, right string) bool {
	var leftParts, rightParts [3]int
	for index, value := range []string{left, right} {
		var major, minor, patch int
		if _, err := fmt.Sscanf(value, "%d.%d.%d", &major, &minor, &patch); err != nil {
			return true
		}
		if index == 0 {
			leftParts = [3]int{major, minor, patch}
		} else {
			rightParts = [3]int{major, minor, patch}
		}
	}
	for index := range leftParts {
		if leftParts[index] != rightParts[index] {
			return leftParts[index] < rightParts[index]
		}
	}
	return false
}

func OpenExecutor(archive []byte) (ExecutorManifest, error) {
	files, err := Files(archive)
	if err != nil {
		return ExecutorManifest{}, err
	}
	if len(files) != 1 || files["executor.json"] == nil {
		return ExecutorManifest{}, errors.New("执行器声明包仅包含 executor.json；平台制品单独按摘要分发")
	}
	return ParseExecutor(files["executor.json"])
}

func (manifest ExecutorManifest) Artifacts() []RuntimeArtifactRef {
	var descriptor ipsentinel.RuntimeDescriptor
	_ = json.Unmarshal(manifest.Descriptor, &descriptor)
	references := []RuntimeArtifactRef{}
	for _, artifact := range descriptor.RuntimeArtifacts {
		references = append(references, RuntimeArtifactRef{artifact.SHA256, artifact.SizeBytes})
	}
	for _, dependencies := range manifest.Dependencies {
		for _, artifact := range dependencies.Artifacts {
			references = append(references, RuntimeArtifactRef{artifact.SHA256, artifact.SizeBytes})
		}
	}
	return references
}

func (manifest ExecutorManifest) Permissions() []string {
	var descriptor ipsentinel.RuntimeDescriptor
	_ = json.Unmarshal(manifest.Descriptor, &descriptor)
	return slices.Clone(descriptor.Permissions)
}
