package ipsentinel

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const Protocol = "ipsentinel.executor/1"
const ContractVersion = "1.0.0"

type Profile struct {
	ProfileID       string   `json:"profile_id"`
	AdapterID       string   `json:"adapter_id"`
	AdapterVersion  string   `json:"adapter_version"`
	Purpose         string   `json:"purpose"`
	Origins         []string `json:"origins"`
	RecoveryCapable bool     `json:"recovery_capable"`
}

type RuntimeArtifact struct {
	Platform           string `json:"platform"`
	SHA256             string `json:"sha256"`
	SizeBytes          int64  `json:"size_bytes"`
	Entrypoint         string `json:"entrypoint"`
	BrowserRevision    string `json:"browser_revision"`
	DependenciesSHA256 string `json:"dependencies_sha256"`
}

type RuntimeDescriptor struct {
	DescriptorVersion int64             `json:"descriptor_version"`
	ExecutorID        string            `json:"executor_id"`
	RuntimeVersion    string            `json:"runtime_version"`
	Protocols         []string          `json:"protocols"`
	ContractVersion   string            `json:"contract_version"`
	Platforms         []string          `json:"platforms"`
	Permissions       []string          `json:"permissions"`
	CatalogSHA256     string            `json:"catalog_sha256"`
	Profiles          []Profile         `json:"profiles"`
	RuntimeArtifacts  []RuntimeArtifact `json:"runtime_artifacts"`
}

//go:embed testdata/contracts/ipsentinel-executor-v1/contract.schema.json
var contractSchema []byte

var descriptorSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(contractSchema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const schemaURL = "urn:ipsentinel:executor:contract:1.0.0"
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return nil, err
	}
	return compiler.Compile(schemaURL + "#/$defs/runtime_descriptor")
})

func ValidateDescriptor(raw []byte) error {
	value, err := ParseStrictJSON(raw)
	if err != nil {
		return err
	}
	schema, err := descriptorSchema()
	if err != nil {
		return err
	}
	return schema.Validate(value)
}

func schemaErr(format string, arguments ...any) error { return fmt.Errorf(format, arguments...) }
