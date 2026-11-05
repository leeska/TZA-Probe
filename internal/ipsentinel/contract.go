package ipsentinel

import (
	"encoding/json"
	"errors"
)

const (
	Protocol        = "ipsentinel.executor/1"
	ContractVersion = "1.0.0"
	ExecutorID      = "ipsentinel"
	RuleVersion     = "ipsentinel-assessment/1"
)

// RPC method names.
const (
	MethodHello          = "hello"
	MethodStatusGet      = "status.get"
	MethodConfigValidate = "config.validate"
	MethodConfigApply    = "config.apply"
	MethodRunSubmit      = "run.submit"
	MethodRunGet         = "run.get"
	MethodRunCancel      = "run.cancel"
	MethodControlPause   = "control.pause"
	MethodControlResume  = "control.resume"
	MethodRecordsList    = "records.list"
	MethodRecordsAck     = "records.ack"
)

// Persistent control modes.
const (
	ModeStandalone      = "standalone"
	ModeManagedReadonly = "managed_readonly"
	ModeManaged         = "managed"
)

// Job purposes and phases.
const (
	PurposeObserve     = "observe"
	PurposeExperiment  = "experiment"
	PurposeEgressCheck = "egress_check"

	PhaseBaseline   = "baseline"
	PhaseFollowup   = "followup"
	PhaseExperiment = "experiment"
)

// Run and execution states.
const (
	StateAccepted    = "accepted"
	StateRunning     = "running"
	StateCancelling  = "cancelling"
	StateCompleted   = "completed"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
	StateInterrupted = "interrupted"
	StateExpired     = "expired"
)

// Assessment states.
const (
	AssessmentUnknown         = "unknown"
	AssessmentAnomalyObserved = "anomaly_observed"
	AssessmentTargetObserved  = "target_observed"
	AssessmentChangePending   = "change_pending"
	AssessmentChangeConfirmed = "change_confirmed"
	AssessmentOtherRegion     = "other_region"
	AssessmentNotApplicable   = "not_applicable"
)

// Journal record kinds.
const (
	RecordRunAccepted        = "run.accepted"
	RecordRunStarted         = "run.started"
	RecordRunTerminal        = "run.terminal"
	RecordControlChanged     = "control.changed"
	RecordConfigApplied      = "config.applied"
	RecordAssessmentChanged  = "assessment.changed"
	RecordAssessmentSnapshot = "assessment.snapshot"
)

// Stable RPC error codes.
const (
	ErrCodeUnsupportedVersion  = "unsupported_version"
	ErrCodeUnsupportedMethod   = "unsupported_method"
	ErrCodeUnsupportedPlatform = "unsupported_platform"
	ErrCodeBindingMismatch     = "binding_mismatch"
	ErrCodeStaleInstance       = "stale_instance"
	ErrCodeStaleEpoch          = "stale_epoch"
	ErrCodeInvalidConfig       = "invalid_config"
	ErrCodeInvalidRequest      = "invalid_request"
	ErrCodePolicyDenied        = "policy_denied"
	ErrCodePermissionDenied    = "permission_denied"
	ErrCodeUnknownProfile      = "unknown_profile"
	ErrCodeRevisionConflict    = "revision_conflict"
	ErrCodePauseConflict       = "pause_conflict"
	ErrCodeBusy                = "busy"
	ErrCodeNotDue              = "not_due"
	ErrCodeExpired             = "expired"
	ErrCodePaused              = "paused"
	ErrCodeModeConflict        = "mode_conflict"
	ErrCodeBudgetExhausted     = "budget_exhausted"
	ErrCodeAlreadySatisfied    = "already_satisfied"
	ErrCodeIdempotencyConflict = "idempotency_conflict"
	ErrCodeResultConflict      = "result_conflict"
	ErrCodeUnknownJob          = "unknown_job"
	ErrCodeCursorExpired       = "cursor_expired"
	ErrCodeStoragePressure     = "storage_pressure"
	ErrCodeUnhealthy           = "unhealthy"
	ErrCodeInternalError       = "internal_error"
)

// Validation sentinels distinguish JSON Schema rejections from cross-field semantic rejections.
var (
	ErrSchema   = errors.New("schema validation failed")
	ErrSemantic = errors.New("semantic validation failed")
)

type Target struct {
	ControllerID string `json:"controller_id"`
	NodeID       string `json:"node_id"`
}

type Fence struct {
	ExecutorInstanceID string `json:"executor_instance_id"`
	Epoch              int64  `json:"epoch"`
}

type ConfigRef struct {
	Revision int64  `json:"revision"`
	SHA256   string `json:"sha256"`
}

type Coords struct {
	LatitudeMicrodegrees  int64 `json:"latitude_microdegrees"`
	LongitudeMicrodegrees int64 `json:"longitude_microdegrees"`
	AccuracyM             int64 `json:"accuracy_m"`
}

type Environment struct {
	Locale   string `json:"locale"`
	Timezone string `json:"timezone"`
}

type ExperimentPlan struct {
	ProfileID   string      `json:"profile_id"`
	Environment Environment `json:"environment"`
	Geolocation *Coords     `json:"geolocation"`
	DwellMinMS  int64       `json:"dwell_min_ms"`
	DwellMaxMS  int64       `json:"dwell_max_ms"`
}

type ServicePlan struct {
	ServiceID              string          `json:"service_id"`
	Enabled                bool            `json:"enabled"`
	AdapterID              string          `json:"adapter_id"`
	ObservationProfileID   string          `json:"observation_profile_id"`
	ObservationEnvironment Environment     `json:"observation_environment"`
	Experiment             *ExperimentPlan `json:"experiment"`
	AllowedOrigins         []string        `json:"allowed_origins"`
	TargetCountry          string          `json:"target_country"`
}

type Limits struct {
	TimeoutMS     int64 `json:"timeout_ms"`
	MemoryBytes   int64 `json:"memory_bytes"`
	CPUMillicores int64 `json:"cpu_millicores"`
	PIDs          int64 `json:"pids"`
	NetworkBytes  int64 `json:"network_bytes"`
	HTTPRequests  int64 `json:"http_requests"`
}

type DailyLimits struct {
	Runs         int64 `json:"runs"`
	Navigations  int64 `json:"navigations"`
	NetworkBytes int64 `json:"network_bytes"`
}

type EgressConfig struct {
	ExpectedIP     string `json:"expected_ip"`
	Family         string `json:"family"`
	CheckProfileID string `json:"check_profile_id"`
}

type Config struct {
	ConfigVersion int64         `json:"config_version"`
	Egress        EgressConfig  `json:"egress"`
	Services      []ServicePlan `json:"services"`
	Limits        Limits        `json:"limits"`
	DailyBudget   DailyLimits   `json:"daily_budget"`
}

type ValidationIssue struct {
	Pointer string `json:"pointer"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Validation struct {
	Valid        bool              `json:"valid"`
	ConfigSHA256 string            `json:"config_sha256"`
	Issues       []ValidationIssue `json:"issues"`
}

type ConfigReceipt struct {
	Config  ConfigRef `json:"config"`
	Changed bool      `json:"changed"`
}

type JobParams struct {
	Fence     Fence     `json:"fence"`
	JobID     string    `json:"job_id"`
	Config    ConfigRef `json:"config"`
	ServiceID string    `json:"service_id"`
	Purpose   string    `json:"purpose"`
	Phase     string    `json:"phase"`
	NotBefore string    `json:"not_before"`
	ExpiresAt string    `json:"expires_at"`
	TimeoutMS int64     `json:"timeout_ms"`
}

type Control struct {
	Mode            string  `json:"mode"`
	Epoch           int64   `json:"epoch"`
	Paused          bool    `json:"paused"`
	PauseGeneration int64   `json:"pause_generation"`
	PauseReason     *string `json:"pause_reason"`
	Draining        bool    `json:"draining"`
	ActiveRunID     *string `json:"active_run_id"`
}

type BudgetStatus struct {
	DayUTC                string      `json:"day_utc"`
	UsedRuns              int64       `json:"used_runs"`
	UsedNavigations       int64       `json:"used_navigations"`
	AccountedNetworkBytes int64       `json:"accounted_network_bytes"`
	Limits                DailyLimits `json:"limits"`
	NetworkAccounting     string      `json:"network_accounting"`
}

type Assessment struct {
	ServiceID         string   `json:"service_id"`
	RuleVersion       string   `json:"rule_version"`
	State             string   `json:"state"`
	SignalKind        string   `json:"signal_kind"`
	BasisRunIDs       []string `json:"basis_run_ids"`
	LastObservedAt    *string  `json:"last_observed_at"`
	ValidUntil        *string  `json:"valid_until"`
	EnvironmentSHA256 *string  `json:"environment_sha256"`
	Reason            string   `json:"reason"`
}

type JournalStatus struct {
	EarliestSeq int64 `json:"earliest_seq"`
	LatestSeq   int64 `json:"latest_seq"`
	AckedSeq    int64 `json:"acked_seq"`
}

type Status struct {
	ExecutorInstanceID string        `json:"executor_instance_id"`
	ObservedAt         string        `json:"observed_at"`
	Health             string        `json:"health"`
	Control            Control       `json:"control"`
	Config             *ConfigRef    `json:"config"`
	Budget             *BudgetStatus `json:"budget"`
	Assessments        []Assessment  `json:"assessments"`
	Journal            JournalStatus `json:"journal"`
}

type Profile struct {
	ProfileID       string   `json:"profile_id"`
	AdapterID       string   `json:"adapter_id"`
	AdapterVersion  string   `json:"adapter_version"`
	Purpose         string   `json:"purpose"`
	Origins         []string `json:"origins"`
	RecoveryCapable bool     `json:"recovery_capable"`
}

type LocalPolicy struct {
	Permissions         []string    `json:"permissions"`
	MaxLimits           Limits      `json:"max_limits"`
	MaxDailyBudget      DailyLimits `json:"max_daily_budget"`
	HardLimitsAvailable bool        `json:"hard_limits_available"`
}

type Hello struct {
	ExecutorID       string      `json:"executor_id"`
	RuntimeVersion   string      `json:"runtime_version"`
	Protocols        []string    `json:"protocols"`
	ContractVersion  string      `json:"contract_version"`
	DescriptorSHA256 string      `json:"descriptor_sha256"`
	Platform         string      `json:"platform"`
	DatabaseVersion  int64       `json:"database_version"`
	Features         []string    `json:"features"`
	Profiles         []Profile   `json:"profiles"`
	LocalPolicy      LocalPolicy `json:"local_policy"`
	Status           Status      `json:"status"`
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

type EgressCheck struct {
	ObservedAt      string `json:"observed_at"`
	IP              string `json:"ip"`
	MatchesExpected bool   `json:"matches_expected"`
}

type EgressEvidence struct {
	ExpectedIP string       `json:"expected_ip"`
	Family     string       `json:"family"`
	Before     *EgressCheck `json:"before"`
	After      *EgressCheck `json:"after"`
}

type Signal struct {
	Provider   string   `json:"provider"`
	Metric     string   `json:"metric"`
	Scope      string   `json:"scope"`
	Status     string   `json:"status"`
	Value      *string  `json:"value"`
	Candidates []string `json:"candidates"`
	ObservedAt string   `json:"observed_at"`
	Source     string   `json:"source"`
}

type Page struct {
	AdapterID      string   `json:"adapter_id"`
	AdapterVersion string   `json:"adapter_version"`
	ProfileID      string   `json:"profile_id"`
	HTTPStatus     *int64   `json:"http_status"`
	Outcome        string   `json:"outcome"`
	FinalOrigin    *string  `json:"final_origin"`
	NeutralEntry   bool     `json:"neutral_entry"`
	DocumentSHA256 *string  `json:"document_sha256"`
	Signals        []Signal `json:"signals"`
}

type GeoEvidence struct {
	Source                      string `json:"source"`
	CallbackSucceeded           bool   `json:"callback_succeeded"`
	UULECookiePresent           bool   `json:"uule_cookie_present"`
	UULESentOnCompletedRequest  bool   `json:"uule_sent_on_completed_request"`
	ProviderReceivedCoordinates string `json:"provider_received_coordinates"`
}

type Resources struct {
	WallMS                   int64  `json:"wall_ms"`
	CPUMS                    *int64 `json:"cpu_ms"`
	PeakMemoryBytes          *int64 `json:"peak_memory_bytes"`
	MemoryMetric             string `json:"memory_metric"`
	NetworkBytes             *int64 `json:"network_bytes"`
	NetworkScope             string `json:"network_scope"`
	NetworkIsolationVerified bool   `json:"network_isolation_verified"`
	OOMKillCount             int64  `json:"oom_kill_count"`
	Cleanup                  string `json:"cleanup"`
	HardLimitsVerified       bool   `json:"hard_limits_verified"`
}

type SessionInfo struct {
	Kind                string `json:"kind"`
	GeolocationOverride bool   `json:"geolocation_override"`
}

type RunResult struct {
	ExecutorInstanceID          string         `json:"executor_instance_id"`
	Epoch                       int64          `json:"epoch"`
	JobID                       string         `json:"job_id"`
	RunID                       string         `json:"run_id"`
	Config                      ConfigRef      `json:"config"`
	ServiceID                   string         `json:"service_id"`
	Purpose                     string         `json:"purpose"`
	Phase                       string         `json:"phase"`
	Execution                   string         `json:"execution"`
	Reason                      *string        `json:"reason"`
	AcceptedAt                  string         `json:"accepted_at"`
	StartedAt                   *string        `json:"started_at"`
	FinishedAt                  string         `json:"finished_at"`
	Provenance                  string         `json:"provenance"`
	RuntimeVersion              string         `json:"runtime_version"`
	BrowserVersion              *string        `json:"browser_version"`
	EnvironmentSHA256           *string        `json:"environment_sha256"`
	Egress                      EgressEvidence `json:"egress"`
	Session                     SessionInfo    `json:"session"`
	Pages                       []Page         `json:"pages"`
	GeolocationEvidence         *GeoEvidence   `json:"geolocation_evidence"`
	Resources                   Resources      `json:"resources"`
	AssessmentEligible          bool           `json:"assessment_eligible"`
	AssessmentIneligibleReasons []string       `json:"assessment_ineligible_reasons"`
}

type Run struct {
	JobID      string     `json:"job_id"`
	RunID      string     `json:"run_id"`
	State      string     `json:"state"`
	AcceptedAt string     `json:"accepted_at"`
	Result     *RunResult `json:"result"`
}

type Admission struct {
	Duplicate bool `json:"duplicate"`
	Run       Run  `json:"run"`
}

type Record struct {
	ExecutorInstanceID string          `json:"executor_instance_id"`
	Seq                int64           `json:"seq"`
	RecordID           string          `json:"record_id"`
	RecordedAt         string          `json:"recorded_at"`
	Kind               string          `json:"kind"`
	Data               json.RawMessage `json:"data"`
}

type RecordPage struct {
	ExecutorInstanceID string   `json:"executor_instance_id"`
	Records            []Record `json:"records"`
	NextSeq            int64    `json:"next_seq"`
	HasMore            bool     `json:"has_more"`
}

type Ack struct {
	AckedSeq int64 `json:"acked_seq"`
}

type Error struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	Retryable       bool   `json:"retryable"`
	CurrentEpoch    *int64 `json:"current_epoch,omitempty"`
	CurrentRevision *int64 `json:"current_revision,omitempty"`
	EarliestSeq     *int64 `json:"earliest_seq,omitempty"`
}

type Request struct {
	Protocol  string          `json:"protocol"`
	RequestID string          `json:"request_id"`
	Method    string          `json:"method"`
	Target    Target          `json:"target"`
	Params    json.RawMessage `json:"params"`
}

type Response struct {
	Protocol  string          `json:"protocol"`
	RequestID string          `json:"request_id"`
	Method    string          `json:"method"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// ConfigSHA256 returns the RFC 8785 SHA-256 digest of a Config.
func ConfigSHA256(cfg Config) (string, error) {
	return CanonicalSHA256(cfg)
}

// JobDigest returns the RFC 8785 SHA-256 digest of a job submission's immutable content
// (excluding request_id and fence, per PROTOCOL.md Section 9).
func JobDigest(job JobParams) (string, error) {
	payload := map[string]any{
		"job_id":     job.JobID,
		"config":     job.Config,
		"service_id": job.ServiceID,
		"purpose":    job.Purpose,
		"phase":      job.Phase,
		"not_before": job.NotBefore,
		"expires_at": job.ExpiresAt,
		"timeout_ms": job.TimeoutMS,
	}
	return CanonicalSHA256(payload)
}

// RequestDigest returns the RFC 8785 SHA-256 digest of a write request's
// (method, target, params) tuple for request_id idempotency checking.
func RequestDigest(method string, target Target, params any) (string, error) {
	payload := map[string]any{
		"method": method,
		"target": target,
		"params": params,
	}
	return CanonicalSHA256(payload)
}
