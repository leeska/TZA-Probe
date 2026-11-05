package ipsentinel

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed testdata/contracts/ipsentinel-executor-v1/contract.schema.json
var contractSchema []byte

var compiledContract = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
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
	definitions := document.(map[string]any)["$defs"].(map[string]any)
	result := map[string]*jsonschema.Schema{}
	for name := range definitions {
		compiled, err := compiler.Compile(schemaURL + "#/$defs/" + name)
		if err != nil {
			return nil, err
		}
		result[name] = compiled
	}
	return result, nil
})

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidUUID(value string) bool { return uuidPattern.MatchString(value) }

func schemaErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSchema, fmt.Sprintf(format, args...))
}

func semanticErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSemantic, fmt.Sprintf(format, args...))
}

func asObject(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, schemaErr("expected object")
	}
	return object, nil
}

func ParseContractTime(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, schemaErr("expected UTC timestamp")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, schemaErr("invalid timestamp")
	}
	return parsed, nil
}

func ValidateDefinition(definition string, raw []byte, profiles map[string]Profile) error {
	value, err := ParseStrictJSON(raw)
	if err != nil {
		return err
	}
	compiled, err := compiledContract()
	if err != nil {
		return schemaErr("contract compilation failed: %v", err)
	}
	schema, exists := compiled[definition]
	if !exists {
		return schemaErr("unknown definition")
	}
	if err := schema.Validate(value); err != nil {
		return schemaErr("contract definition %s rejected", definition)
	}
	return validateSemantics(definition, raw, profiles)
}

func ValidateRequest(raw []byte, profiles map[string]Profile) (Request, error) {
	var request Request
	if len(raw) > MaxRequestBytes {
		return request, schemaErr("request exceeds limit")
	}
	if err := ValidateDefinition("request", raw, profiles); err != nil {
		return request, err
	}
	err := DecodeStrict(raw, &request)
	return request, err
}

func ValidateResponse(raw []byte, profiles map[string]Profile) (Response, error) {
	var response Response
	if len(raw) > MaxResponseBytes {
		return response, schemaErr("response exceeds limit")
	}
	if err := ValidateDefinition("response", raw, profiles); err != nil {
		return response, err
	}
	err := DecodeStrict(raw, &response)
	return response, err
}

func validateSemantics(definition string, raw []byte, profiles map[string]Profile) error {
	switch definition {
	case "request":
		var request Request
		if err := DecodeStrict(raw, &request); err != nil {
			return err
		}
		var params map[string]json.RawMessage
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return err
		}
		switch request.Method {
		case MethodConfigApply, MethodConfigValidate:
			return validateSemantics("config", params["config"], profiles)
		case MethodRunSubmit:
			return validateSemantics("job", request.Params, profiles)
		}
	case "response":
		var response Response
		if err := DecodeStrict(raw, &response); err != nil {
			return err
		}
		if !response.OK {
			return nil
		}
		definition := map[string]string{
			MethodHello: "hello", MethodStatusGet: "status", MethodRunGet: "run", MethodRunCancel: "run",
			MethodRecordsList: "record_page", MethodRunSubmit: "admission", MethodConfigValidate: "validation",
			MethodConfigApply: "config_receipt", MethodControlPause: "control", MethodControlResume: "control", MethodRecordsAck: "ack",
		}[response.Method]
		if definition != "" {
			return validateSemantics(definition, response.Result, profiles)
		}
	case "config":
		var config Config
		if err := DecodeStrict(raw, &config); err != nil {
			return err
		}
		return ValidateConfigSemantic(config, profiles)
	case "job":
		var job JobParams
		if err := DecodeStrict(raw, &job); err != nil {
			return err
		}
		return ValidateJobSemantic(job)
	case "result":
		if len(raw) > MaxResultBytes {
			return schemaErr("result exceeds limit")
		}
		var result RunResult
		if err := DecodeStrict(raw, &result); err != nil {
			return err
		}
		return ValidateResultSemantic(result, profiles)
	case "run":
		var run Run
		if err := DecodeStrict(raw, &run); err != nil {
			return err
		}
		if run.Result != nil {
			if run.JobID != run.Result.JobID || run.RunID != run.Result.RunID || run.State != run.Result.Execution || run.AcceptedAt != run.Result.AcceptedAt {
				return semanticErr("run/result identity mismatch")
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				return err
			}
			return validateSemantics("result", object["result"], profiles)
		}
	case "admission":
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		return validateSemantics("run", object["run"], profiles)
	case "record":
		if len(raw) > MaxResultBytes {
			return schemaErr("record exceeds limit")
		}
		var record Record
		if err := DecodeStrict(raw, &record); err != nil {
			return err
		}
		switch record.Kind {
		case RecordRunTerminal:
			var result RunResult
			if err := DecodeStrict(record.Data, &result); err != nil {
				return err
			}
			if result.ExecutorInstanceID != record.ExecutorInstanceID {
				return semanticErr("record/result instance mismatch")
			}
			return validateSemantics("result", record.Data, profiles)
		case RecordRunAccepted, RecordRunStarted:
			return validateSemantics("run", record.Data, profiles)
		case RecordAssessmentChanged, RecordAssessmentSnapshot:
			return validateSemantics("assessment", record.Data, profiles)
		}
	case "record_page":
		var page RecordPage
		if err := DecodeStrict(raw, &page); err != nil {
			return err
		}
		for index, record := range page.Records {
			if record.ExecutorInstanceID != page.ExecutorInstanceID || (index > 0 && record.Seq != page.Records[index-1].Seq+1) {
				return semanticErr("record page instance or sequence mismatch")
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err := validateSemantics("record", encoded, profiles); err != nil {
				return err
			}
		}
		if len(page.Records) > 0 && page.NextSeq != page.Records[len(page.Records)-1].Seq {
			return semanticErr("invalid page cursor")
		}
	case "hello":
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		return validateSemantics("status", object["status"], profiles)
	case "status":
		var status Status
		if err := DecodeStrict(raw, &status); err != nil {
			return err
		}
		if status.Journal.AckedSeq > status.Journal.LatestSeq || status.Journal.EarliestSeq > status.Journal.LatestSeq+1 {
			return semanticErr("invalid journal watermarks")
		}
		seen := map[string]bool{}
		for _, assessment := range status.Assessments {
			if seen[assessment.ServiceID] {
				return semanticErr("duplicate service assessment")
			}
			seen[assessment.ServiceID] = true
			if err := validateAssessment(assessment); err != nil {
				return err
			}
		}
	case "assessment":
		var assessment Assessment
		if err := DecodeStrict(raw, &assessment); err != nil {
			return err
		}
		return validateAssessment(assessment)
	}
	return nil
}

func ValidateConfigSemantic(config Config, profiles map[string]Profile) error {
	address, err := netip.ParseAddr(config.Egress.ExpectedIP)
	if err != nil || !publicAddress(address) || (config.Egress.Family == "ipv4") != address.Is4() {
		return semanticErr("invalid public egress or address family")
	}
	seen := map[string]bool{}
	check, hasCheck := profiles[config.Egress.CheckProfileID]
	if profiles != nil && (!hasCheck || check.Purpose != PurposeEgressCheck) {
		return semanticErr("unknown egress profile")
	}
	for _, service := range config.Services {
		if seen[service.ServiceID] {
			return semanticErr("duplicate service ID")
		}
		seen[service.ServiceID] = true
		if _, err := time.LoadLocation(service.ObservationEnvironment.Timezone); err != nil {
			return semanticErr("invalid timezone")
		}
		observation, exists := profiles[service.ObservationProfileID]
		if profiles != nil && (!exists || observation.Purpose != PurposeObserve || observation.AdapterID != service.AdapterID) {
			return semanticErr("observation profile mismatch")
		}
		permitted := map[string]bool{}
		for _, origin := range append(append([]string{}, check.Origins...), observation.Origins...) {
			permitted[origin] = true
		}
		if experiment := service.Experiment; experiment != nil {
			if experiment.DwellMinMS > experiment.DwellMaxMS {
				return semanticErr("dwell range reversed")
			}
			if _, err := time.LoadLocation(experiment.Environment.Timezone); err != nil {
				return semanticErr("invalid experiment timezone")
			}
			profile, exists := profiles[experiment.ProfileID]
			if profiles != nil && (!exists || profile.Purpose != PurposeExperiment || profile.AdapterID != service.AdapterID) {
				return semanticErr("experiment profile mismatch")
			}
			for _, origin := range profile.Origins {
				permitted[origin] = true
			}
		}
		origins := map[string]bool{}
		for _, origin := range service.AllowedOrigins {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return semanticErr("invalid origin")
			}
			if profiles != nil && !permitted[origin] {
				return semanticErr("origin outside catalog")
			}
			origins[origin] = true
		}
		for _, origin := range check.Origins {
			if !origins[origin] {
				return semanticErr("missing egress origin")
			}
		}
	}
	if config.DailyBudget.NetworkBytes < config.Limits.NetworkBytes {
		return semanticErr("daily reservation too small")
	}
	return nil
}

func publicAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.Is4In6() {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(prefix).Contains(address) {
			return false
		}
	}
	return true
}

func ValidateJobSemantic(job JobParams) error {
	start, err := ParseContractTime(job.NotBefore)
	if err != nil {
		return err
	}
	end, err := ParseContractTime(job.ExpiresAt)
	if err != nil {
		return err
	}
	if !start.Before(end) || end.Sub(start) > 15*time.Minute {
		return semanticErr("invalid run window")
	}
	return nil
}

func ValidateResultSemantic(result RunResult, _ map[string]Profile) error {
	accepted, err := ParseContractTime(result.AcceptedAt)
	if err != nil {
		return err
	}
	finished, err := ParseContractTime(result.FinishedAt)
	if err != nil {
		return err
	}
	if finished.Before(accepted) {
		return semanticErr("finish before admission")
	}
	if result.StartedAt != nil {
		started, err := ParseContractTime(*result.StartedAt)
		if err != nil {
			return err
		}
		if started.Before(accepted) || started.After(finished) {
			return semanticErr("invalid start time")
		}
	}
	providers := map[string]string{"google_home": "google", "meta_ads_library": "meta", "meta_legal": "meta", "cloudflare_trace": "cloudflare", "youtube_region": "youtube"}
	for _, page := range result.Pages {
		for _, signal := range page.Signals {
			if providers[page.AdapterID] != signal.Provider {
				return semanticErr("signal provider mismatch")
			}
		}
	}
	if result.AssessmentEligible {
		if result.Purpose != PurposeObserve || result.Execution != StateCompleted || result.Resources.Cleanup != "complete" || !result.Resources.HardLimitsVerified || result.Resources.OOMKillCount != 0 {
			return semanticErr("unsafe or incomplete qualified execution")
		}
		for _, check := range []*EgressCheck{result.Egress.Before, result.Egress.After} {
			if check == nil || !check.MatchesExpected || check.IP != result.Egress.ExpectedIP {
				return semanticErr("missing or mismatched egress check")
			}
		}
		if len(result.Pages) == 0 {
			return semanticErr("missing qualified pages")
		}
		for _, page := range result.Pages {
			if !page.NeutralEntry || page.Outcome != "normal" {
				return semanticErr("non-neutral or abnormal page")
			}
			observed := false
			for _, signal := range page.Signals {
				observed = observed || signal.Status == "observed"
			}
			if !observed {
				return semanticErr("no observed signals")
			}
		}
	}
	return nil
}

func validateAssessment(assessment Assessment) error {
	if assessment.State == AssessmentUnknown || assessment.State == AssessmentNotApplicable {
		return nil
	}
	if assessment.LastObservedAt == nil || assessment.ValidUntil == nil {
		return semanticErr("assessment without validity")
	}
	observed, err := ParseContractTime(*assessment.LastObservedAt)
	if err != nil {
		return err
	}
	until, err := ParseContractTime(*assessment.ValidUntil)
	if err != nil {
		return err
	}
	if !observed.Before(until) || until.Sub(observed) > 48*time.Hour {
		return semanticErr("invalid assessment validity")
	}
	return nil
}
