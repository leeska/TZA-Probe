package ipsentinel

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// MockExecutor is a stateful Unix domain socket HTTP/1.1 server implementing
// the IPSentinel Executor Contract 1.0.0 for testing and fault injection (A01-A24).
type MockExecutor struct {
	mu                 sync.Mutex
	SocketPath         string
	Target             Target
	ExecutorInstanceID string
	RuntimeVersion     string
	Platform           string
	DescriptorSHA256   string
	DatabaseVersion    int64
	Features           []string
	Profiles           []Profile
	LocalPolicy        LocalPolicy
	Health             string
	Control            Control
	Config             *Config
	ConfigRef          *ConfigRef
	Budget             BudgetStatus
	Assessments        []Assessment
	EarliestSeq        int64
	AckedSeq           int64
	Records            []Record
	StoragePressure    bool
	RejectPeer         bool
	RequireHelloFirst  bool
	helloSeen          bool
	Now                func() time.Time

	// Write request deduplication ledger: request_id -> {method, digest, response}
	requests map[string]recordedRequest
	// Logical job ledger: job_id -> {digest, run}
	jobs map[string]recordedJob

	listener net.Listener
	server   *http.Server
}

type recordedRequest struct {
	method string
	digest string
	result json.RawMessage
}

type recordedJob struct {
	digest string
	run    Run
	params JobParams
}

// DefaultMockProfiles returns the standard profile catalog from hello.response.json.
func DefaultMockProfiles() []Profile {
	return []Profile{
		{
			ProfileID:       "google-home-neutral-v1",
			AdapterID:       "google_home",
			AdapterVersion:  "1",
			Purpose:         PurposeObserve,
			Origins:         []string{"https://www.google.com", "https://www.google.com.hk", "https://www.gstatic.com", "https://api64.ipify.org"},
			RecoveryCapable: true,
		},
		{
			ProfileID:       "google-home-ncr-v1",
			AdapterID:       "google_home",
			AdapterVersion:  "1",
			Purpose:         PurposeExperiment,
			Origins:         []string{"https://www.google.com", "https://www.google.com.hk", "https://www.gstatic.com", "https://api64.ipify.org"},
			RecoveryCapable: true,
		},
		{
			ProfileID:       "ipify-public-v1",
			AdapterID:       "egress",
			AdapterVersion:  "1",
			Purpose:         PurposeEgressCheck,
			Origins:         []string{"https://api64.ipify.org"},
			RecoveryCapable: false,
		},
	}
}

// NewMockExecutor starts a stateful UDS mock server at socketPath.
func NewMockExecutor(socketPath string, target Target) (*MockExecutor, error) {
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	m := &MockExecutor{
		SocketPath:         socketPath,
		Target:             target,
		ExecutorInstanceID: "11111111-1111-4111-8111-111111111111",
		RuntimeVersion:     "0.4.0-mock",
		Platform:           "linux-amd64",
		DescriptorSHA256:   strings.Repeat("a", 64),
		DatabaseVersion:    2,
		Features:           []string{"config", "run", "cancel", "pause", "journal", "assessment"},
		Profiles:           DefaultMockProfiles(),
		LocalPolicy: LocalPolicy{
			Permissions: []string{"network.public_https", "browser.execute", "profile.persist", "geolocation.override"},
			MaxLimits: Limits{
				TimeoutMS:     120000,
				MemoryBytes:   461373440,
				CPUMillicores: 850,
				PIDs:          256,
				NetworkBytes:  31457280,
				HTTPRequests:  150,
			},
			MaxDailyBudget: DailyLimits{
				Runs:         8,
				Navigations:  32,
				NetworkBytes: 209715200,
			},
			HardLimitsAvailable: true,
		},
		Health: "ready",
		Control: Control{
			Mode:            ModeManaged,
			Epoch:           1,
			Paused:          false,
			PauseGeneration: 0,
			PauseReason:     nil,
			Draining:        false,
			ActiveRunID:     nil,
		},
		Budget: BudgetStatus{
			DayUTC:                "2026-11-04",
			UsedRuns:              0,
			UsedNavigations:       0,
			AccountedNetworkBytes: 0,
			Limits: DailyLimits{
				Runs:         8,
				Navigations:  32,
				NetworkBytes: 209715200,
			},
			NetworkAccounting: "sampled",
		},
		Assessments: []Assessment{},
		EarliestSeq: 1,
		AckedSeq:    0,
		Now:         func() time.Time { return time.Now().UTC() },
		requests:    map[string]recordedRequest{},
		jobs:        map[string]recordedJob{},
		listener:    ln,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.serveHTTP)
	m.server = &http.Server{Handler: mux}
	go func() { _ = m.server.Serve(ln) }()
	return m, nil
}

func (m *MockExecutor) Close() error {
	var err error
	if m.server != nil {
		err = m.server.Close()
	}
	_ = os.Remove(m.SocketPath)
	return err
}

func (m *MockExecutor) nowUTC() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *MockExecutor) nowStr() string {
	return m.nowUTC().Format(time.RFC3339)
}

func (m *MockExecutor) profileMapLocked() map[string]Profile {
	out := make(map[string]Profile, len(m.Profiles))
	for _, p := range m.Profiles {
		out[p.ProfileID] = p
	}
	return out
}

func (m *MockExecutor) appendRecordLocked(kind string, data any) Record {
	seq := int64(1)
	if len(m.Records) > 0 {
		seq = m.Records[len(m.Records)-1].Seq + 1
	} else if m.EarliestSeq > 1 {
		seq = m.EarliestSeq
	}
	raw, _ := json.Marshal(data)
	rec := Record{
		ExecutorInstanceID: m.ExecutorInstanceID,
		Seq:                seq,
		RecordID:           NewUUID(),
		RecordedAt:         m.nowStr(),
		Kind:               kind,
		Data:               raw,
	}
	m.Records = append(m.Records, rec)
	return rec
}

func (m *MockExecutor) statusLocked() Status {
	latestSeq := int64(0)
	if len(m.Records) > 0 {
		latestSeq = m.Records[len(m.Records)-1].Seq
	}
	var cfgRef *ConfigRef
	if m.ConfigRef != nil {
		cp := *m.ConfigRef
		cfgRef = &cp
	}
	budget := m.Budget
	assessments := append([]Assessment{}, m.Assessments...)
	return Status{
		ExecutorInstanceID: m.ExecutorInstanceID,
		ObservedAt:         m.nowStr(),
		Health:             m.Health,
		Control:            m.Control,
		Config:             cfgRef,
		Budget:             &budget,
		Assessments:        assessments,
		Journal: JournalStatus{
			EarliestSeq: m.EarliestSeq,
			LatestSeq:   latestSeq,
			AckedSeq:    m.AckedSeq,
		},
	}
}

// CompleteRun transitions an accepted/running job in the mock to a terminal RunResult
// and appends a run.terminal journal record.
func (m *MockExecutor) CompleteRun(jobID string, result RunResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.jobs[jobID]
	if !ok {
		return errors.New("unknown job_id")
	}
	switch entry.run.State {
	case StateCompleted, StateFailed, StateCancelled, StateInterrupted, StateExpired:
		// Terminal state is immutable.
		return nil
	}
	cp := result
	entry.run.State = result.Execution
	entry.run.Result = &cp
	m.jobs[jobID] = entry
	if m.Control.ActiveRunID != nil && *m.Control.ActiveRunID == entry.run.RunID {
		m.Control.ActiveRunID = nil
	}
	m.appendRecordLocked(RecordRunTerminal, cp)
	return nil
}

// TriggerAutoPause simulates an automatic challenge/429/sandbox pause event on the node.
func (m *MockExecutor) TriggerAutoPause(reason string) Control {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Control.Paused = true
	m.Control.PauseGeneration++
	r := reason
	m.Control.PauseReason = &r
	m.appendRecordLocked(RecordControlChanged, m.Control)
	return m.Control
}

func (m *MockExecutor) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/call" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(r.TransferEncoding) > 0 && slices.Contains(r.TransferEncoding, "chunked") {
		http.Error(w, "chunked requests not supported", http.StatusBadRequest)
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	m.mu.Lock()
	rejectPeer := m.RejectPeer
	m.mu.Unlock()
	if rejectPeer {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body) > MaxRequestBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	// Check if protocol version is unsupported while envelope is recognizable.
	parsed, err := ParseStrictJSON(body)
	if err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	root, err := asObject(parsed)
	if err != nil {
		http.Error(w, "invalid JSON root", http.StatusBadRequest)
		return
	}
	proto, _ := root["protocol"].(string)
	reqID, _ := root["request_id"].(string)
	method, _ := root["method"].(string)
	if proto != Protocol {
		if ValidUUID(reqID) && isKnownMethod(method) {
			m.writeError(w, reqID, method, Error{
				Code:      ErrCodeUnsupportedVersion,
				Message:   "Unsupported protocol version",
				Retryable: false,
			})
			return
		}
		http.Error(w, "unsupported protocol", http.StatusBadRequest)
		return
	}
	if !isKnownMethod(method) {
		http.Error(w, "unknown method", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	req, err := ValidateRequest(body, nil)
	if err != nil {
		// Check if it was a semantic config/job rejection vs malformed schema.
		if errors.Is(err, ErrSemantic) && ValidUUID(reqID) {
			code := ErrCodeInvalidRequest
			if method == MethodConfigValidate || method == MethodConfigApply {
				code = ErrCodeInvalidConfig
			}
			m.writeError(w, reqID, method, Error{
				Code:      code,
				Message:   "Semantic validation failed",
				Retryable: false,
			})
			return
		}
		http.Error(w, "schema validation failed", http.StatusBadRequest)
		return
	}

	// Step 1: Peer + Target check.
	if req.Target.ControllerID != m.Target.ControllerID || req.Target.NodeID != m.Target.NodeID {
		m.writeError(w, req.RequestID, req.Method, Error{
			Code:      ErrCodeBindingMismatch,
			Message:   "Target controller_id or node_id mismatch",
			Retryable: false,
		})
		return
	}
	if m.RequireHelloFirst && !m.helloSeen && req.Method != MethodHello {
		m.writeError(w, req.RequestID, req.Method, Error{
			Code:      ErrCodeInvalidRequest,
			Message:   "hello must be called first",
			Retryable: true,
		})
		return
	}
	if !slices.Contains(m.Features, requiredFeature(req.Method)) && requiredFeature(req.Method) != "" {
		m.writeError(w, req.RequestID, req.Method, Error{
			Code:      ErrCodeUnsupportedMethod,
			Message:   "Feature not supported by this runtime",
			Retryable: false,
		})
		return
	}

	// Step 2: Write request idempotency check (replay identical request_id without side effects).
	isWrite := isWriteMethod(req.Method)
	var reqDigest string
	if isWrite {
		var rawParams any
		_ = json.Unmarshal(req.Params, &rawParams)
		reqDigest, _ = RequestDigest(req.Method, req.Target, rawParams)
		if prev, ok := m.requests[req.RequestID]; ok {
			if prev.method != req.Method || prev.digest != reqDigest {
				m.writeError(w, req.RequestID, req.Method, Error{
					Code:      ErrCodeIdempotencyConflict,
					Message:   "request_id reused with different parameters",
					Retryable: false,
				})
				return
			}
			m.writeRawResult(w, req.RequestID, req.Method, prev.result)
			return
		}
	}

	// Step 3: Mode check.
	switch m.Control.Mode {
	case ModeStandalone:
		if isWrite {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeModeConflict,
				Message:   "Write operations are disabled in standalone mode",
				Retryable: false,
			})
			return
		}
	case ModeManagedReadonly:
		if isWrite && req.Method != MethodRecordsAck {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeModeConflict,
				Message:   "Only read-only operations and records.ack are allowed in managed_readonly mode",
				Retryable: false,
			})
			return
		}
	}

	// Step 4: Fence check for fenced methods.
	if fence, hasFence := extractFence(req); hasFence {
		if fence.ExecutorInstanceID != m.ExecutorInstanceID {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeStaleInstance,
				Message:   "executor_instance_id does not match",
				Retryable: false,
			})
			return
		}
		if fence.Epoch != m.Control.Epoch {
			ep := m.Control.Epoch
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:         ErrCodeStaleEpoch,
				Message:      "epoch does not match current control epoch",
				Retryable:    false,
				CurrentEpoch: &ep,
			})
			return
		}
	}

	// Step 5: Method semantics.
	switch req.Method {
	case MethodHello:
		m.helloSeen = true
		hello := Hello{
			ExecutorID:       ExecutorID,
			RuntimeVersion:   m.RuntimeVersion,
			Protocols:        []string{Protocol},
			ContractVersion:  ContractVersion,
			DescriptorSHA256: m.DescriptorSHA256,
			Platform:         m.Platform,
			DatabaseVersion:  m.DatabaseVersion,
			Features:         m.Features,
			Profiles:         m.Profiles,
			LocalPolicy:      m.LocalPolicy,
			Status:           m.statusLocked(),
		}
		m.writeResult(w, req.RequestID, req.Method, "", hello)

	case MethodStatusGet:
		m.writeResult(w, req.RequestID, req.Method, "", m.statusLocked())

	case MethodConfigValidate:
		var p struct {
			Config Config `json:"config"`
		}
		_ = DecodeStrict(req.Params, &p)
		sum, _ := ConfigSHA256(p.Config)
		issues := m.validateConfigAgainstPolicyLocked(p.Config)
		m.writeResult(w, req.RequestID, req.Method, "", Validation{
			Valid:        len(issues) == 0,
			ConfigSHA256: sum,
			Issues:       issues,
		})

	case MethodConfigApply:
		var p struct {
			Fence            Fence  `json:"fence"`
			ExpectedRevision int64  `json:"expected_revision"`
			Config           Config `json:"config"`
		}
		_ = DecodeStrict(req.Params, &p)
		if m.Control.ActiveRunID != nil || m.Control.Draining {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeBusy,
				Message:   "Cannot apply config while a run is active or draining",
				Retryable: true,
			})
			return
		}
		issues := m.validateConfigAgainstPolicyLocked(p.Config)
		if len(issues) > 0 {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      issues[0].Code,
				Message:   issues[0].Message,
				Retryable: false,
			})
			return
		}
		sum, _ := ConfigSHA256(p.Config)
		curRev := int64(0)
		curSum := ""
		if m.ConfigRef != nil {
			curRev = m.ConfigRef.Revision
			curSum = m.ConfigRef.SHA256
		}
		if curRev > 0 && curSum == sum {
			receipt := ConfigReceipt{
				Config:  *m.ConfigRef,
				Changed: false,
			}
			m.writeResult(w, req.RequestID, req.Method, reqDigest, receipt)
			return
		}
		if p.ExpectedRevision != curRev {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:            ErrCodeRevisionConflict,
				Message:         "expected_revision does not match current revision",
				Retryable:       false,
				CurrentRevision: &curRev,
			})
			return
		}
		nextRef := ConfigRef{Revision: curRev + 1, SHA256: sum}
		cfgCopy := p.Config
		m.Config = &cfgCopy
		m.ConfigRef = &nextRef
		m.Budget.Limits = p.Config.DailyBudget
		receipt := ConfigReceipt{Config: nextRef, Changed: true}
		m.appendRecordLocked(RecordConfigApplied, receipt)
		m.writeResult(w, req.RequestID, req.Method, reqDigest, receipt)

	case MethodRunSubmit:
		var job JobParams
		_ = DecodeStrict(req.Params, &job)
		jobDigest, _ := JobDigest(job)
		if existing, ok := m.jobs[job.JobID]; ok {
			if existing.digest != jobDigest {
				m.writeError(w, req.RequestID, req.Method, Error{
					Code:      ErrCodeIdempotencyConflict,
					Message:   "job_id already exists with different parameters",
					Retryable: false,
				})
				return
			}
			adm := Admission{Duplicate: true, Run: existing.run}
			m.writeResult(w, req.RequestID, req.Method, reqDigest, adm)
			return
		}
		if m.StoragePressure {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeStoragePressure,
				Message:   "Unacknowledged journal backlog exceeds quota",
				Retryable: false,
			})
			return
		}
		if m.Health != "ready" {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeUnhealthy,
				Message:   "Executor is not healthy",
				Retryable: false,
			})
			return
		}
		if m.Control.Paused || m.Control.Draining {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodePaused,
				Message:   "Executor is paused",
				Retryable: false,
			})
			return
		}
		if m.ConfigRef == nil || m.Config == nil ||
			job.Config.Revision != m.ConfigRef.Revision || job.Config.SHA256 != m.ConfigRef.SHA256 {
			var curRev *int64
			if m.ConfigRef != nil {
				rev := m.ConfigRef.Revision
				curRev = &rev
			}
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:            ErrCodeRevisionConflict,
				Message:         "Job config revision/sha256 does not match applied config",
				Retryable:       false,
				CurrentRevision: curRev,
			})
			return
		}
		if job.TimeoutMS > m.Config.Limits.TimeoutMS {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodePolicyDenied,
				Message:   "Job timeout_ms exceeds configured limit",
				Retryable: false,
			})
			return
		}
		// Check service & purpose enabled in applied Config.
		var svcPlan *ServicePlan
		for i := range m.Config.Services {
			if m.Config.Services[i].ServiceID == job.ServiceID && m.Config.Services[i].Enabled {
				svcPlan = &m.Config.Services[i]
				break
			}
		}
		if svcPlan == nil || (job.Purpose == PurposeExperiment && svcPlan.Experiment == nil) {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeInvalidRequest,
				Message:   "Service or purpose is not enabled in current config",
				Retryable: false,
			})
			return
		}
		if job.Purpose == PurposeExperiment {
			for _, a := range m.Assessments {
				if a.ServiceID == job.ServiceID && a.State == AssessmentChangeConfirmed {
					m.writeError(w, req.RequestID, req.Method, Error{
						Code:      ErrCodeAlreadySatisfied,
						Message:   "Service recovery change is already confirmed",
						Retryable: false,
					})
					return
				}
			}
		}
		now := m.nowUTC()
		nb, _ := ParseContractTime(job.NotBefore)
		exp, _ := ParseContractTime(job.ExpiresAt)
		if now.Before(nb) {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeNotDue,
				Message:   "Job not_before time has not arrived",
				Retryable: true,
			})
			return
		}
		if !now.Before(exp) {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeExpired,
				Message:   "Job expires_at window has passed",
				Retryable: false,
			})
			return
		}
		if m.Control.ActiveRunID != nil {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeBusy,
				Message:   "Another run is currently active",
				Retryable: true,
			})
			return
		}
		if m.Budget.UsedRuns >= m.Budget.Limits.Runs {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeBudgetExhausted,
				Message:   "Daily run budget exhausted",
				Retryable: false,
			})
			return
		}
		runID := NewUUID()
		run := Run{
			JobID:      job.JobID,
			RunID:      runID,
			State:      StateAccepted,
			AcceptedAt: m.nowStr(),
			Result:     nil,
		}
		m.Budget.UsedRuns++
		m.Control.ActiveRunID = &runID
		m.jobs[job.JobID] = recordedJob{digest: jobDigest, run: run, params: job}
		m.appendRecordLocked(RecordRunAccepted, run)
		adm := Admission{Duplicate: false, Run: run}
		m.writeResult(w, req.RequestID, req.Method, reqDigest, adm)

	case MethodRunGet:
		var p struct {
			JobID string `json:"job_id"`
		}
		_ = DecodeStrict(req.Params, &p)
		entry, ok := m.jobs[p.JobID]
		if !ok {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeUnknownJob,
				Message:   "Unknown job_id",
				Retryable: false,
			})
			return
		}
		m.writeResult(w, req.RequestID, req.Method, "", entry.run)

	case MethodRunCancel:
		var p struct {
			Fence  Fence  `json:"fence"`
			JobID  string `json:"job_id"`
			Reason string `json:"reason"`
		}
		_ = DecodeStrict(req.Params, &p)
		entry, ok := m.jobs[p.JobID]
		if !ok {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeUnknownJob,
				Message:   "Unknown job_id",
				Retryable: false,
			})
			return
		}
		// If already terminal, preserve terminal state unchanged (A10).
		if entry.run.State == StateAccepted || entry.run.State == StateRunning || entry.run.State == StateCancelling {
			reason := "operator_cancelled"
			envHash := strings.Repeat("a", 64)
			started := entry.run.AcceptedAt
			res := RunResult{
				ExecutorInstanceID: m.ExecutorInstanceID,
				Epoch:              m.Control.Epoch,
				JobID:              entry.run.JobID,
				RunID:              entry.run.RunID,
				Config:             entry.params.Config,
				ServiceID:          entry.params.ServiceID,
				Purpose:            entry.params.Purpose,
				Phase:              entry.params.Phase,
				Execution:          StateCancelled,
				Reason:             &reason,
				AcceptedAt:         entry.run.AcceptedAt,
				StartedAt:          &started,
				FinishedAt:         m.nowStr(),
				Provenance:         "native",
				RuntimeVersion:     m.RuntimeVersion,
				EnvironmentSHA256:  &envHash,
				Egress: EgressEvidence{
					ExpectedIP: "8.8.8.8",
					Family:     "ipv4",
				},
				Session: SessionInfo{
					Kind:                mapSessionKind(entry.params.Purpose),
					GeolocationOverride: false,
				},
				Pages: []Page{},
				Resources: Resources{
					WallMS:             100,
					MemoryMetric:       "cgroup",
					NetworkScope:       "host_interface",
					Cleanup:            "complete",
					HardLimitsVerified: true,
				},
				AssessmentEligible:          false,
				AssessmentIneligibleReasons: []string{"cancelled"},
			}
			entry.run.State = StateCancelled
			entry.run.Result = &res
			m.jobs[p.JobID] = entry
			if m.Control.ActiveRunID != nil && *m.Control.ActiveRunID == entry.run.RunID {
				m.Control.ActiveRunID = nil
			}
			m.appendRecordLocked(RecordRunTerminal, res)
		}
		m.writeResult(w, req.RequestID, req.Method, reqDigest, entry.run)

	case MethodControlPause:
		m.Control.Paused = true
		m.Control.PauseGeneration++
		reason := "operator"
		m.Control.PauseReason = &reason
		m.appendRecordLocked(RecordControlChanged, m.Control)
		m.writeResult(w, req.RequestID, req.Method, reqDigest, m.Control)

	case MethodControlResume:
		var p struct {
			Fence                   Fence `json:"fence"`
			ExpectedPauseGeneration int64 `json:"expected_pause_generation"`
		}
		_ = DecodeStrict(req.Params, &p)
		if m.Control.Draining || m.Control.ActiveRunID != nil {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeBusy,
				Message:   "Cannot resume while draining or active run is present",
				Retryable: true,
			})
			return
		}
		if p.ExpectedPauseGeneration != m.Control.PauseGeneration {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodePauseConflict,
				Message:   "expected_pause_generation does not match current pause_generation",
				Retryable: false,
			})
			return
		}
		m.Control.Paused = false
		m.Control.PauseReason = nil
		m.appendRecordLocked(RecordControlChanged, m.Control)
		m.writeResult(w, req.RequestID, req.Method, reqDigest, m.Control)

	case MethodRecordsList:
		var p struct {
			AfterSeq int64 `json:"after_seq"`
			Limit    int64 `json:"limit"`
		}
		_ = DecodeStrict(req.Params, &p)
		if p.AfterSeq+1 < m.EarliestSeq {
			es := m.EarliestSeq
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:        ErrCodeCursorExpired,
				Message:     "Requested after_seq has already been pruned",
				Retryable:   false,
				EarliestSeq: &es,
			})
			return
		}
		batch := []Record{}
		hasMore := false
		for _, rec := range m.Records {
			if rec.Seq <= p.AfterSeq {
				continue
			}
			if int64(len(batch)) >= p.Limit {
				hasMore = true
				break
			}
			batch = append(batch, rec)
		}
		nextSeq := p.AfterSeq
		if len(batch) > 0 {
			nextSeq = batch[len(batch)-1].Seq
		}
		page := RecordPage{
			ExecutorInstanceID: m.ExecutorInstanceID,
			Records:            batch,
			NextSeq:            nextSeq,
			HasMore:            hasMore,
		}
		m.writeResult(w, req.RequestID, req.Method, "", page)

	case MethodRecordsAck:
		var p struct {
			Fence      Fence `json:"fence"`
			ThroughSeq int64 `json:"through_seq"`
		}
		_ = DecodeStrict(req.Params, &p)
		latestSeq := int64(0)
		if len(m.Records) > 0 {
			latestSeq = m.Records[len(m.Records)-1].Seq
		}
		if p.ThroughSeq > latestSeq {
			m.writeError(w, req.RequestID, req.Method, Error{
				Code:      ErrCodeInvalidRequest,
				Message:   "through_seq exceeds latest journal seq",
				Retryable: false,
			})
			return
		}
		if p.ThroughSeq > m.AckedSeq {
			m.AckedSeq = p.ThroughSeq
		}
		m.writeResult(w, req.RequestID, req.Method, reqDigest, Ack{AckedSeq: m.AckedSeq})
	}
}

func mapSessionKind(purpose string) string {
	if purpose == PurposeExperiment {
		return "persistent"
	}
	return "ephemeral"
}

func (m *MockExecutor) validateConfigAgainstPolicyLocked(cfg Config) []ValidationIssue {
	issues := []ValidationIssue{}
	if err := ValidateConfigSemantic(cfg, m.profileMapLocked()); err != nil {
		code := ErrCodeInvalidConfig
		if strings.Contains(err.Error(), "profile") {
			code = ErrCodeUnknownProfile
		}
		issues = append(issues, ValidationIssue{
			Pointer: "/services",
			Code:    code,
			Message: err.Error(),
		})
	}
	maxL := m.LocalPolicy.MaxLimits
	if cfg.Limits.TimeoutMS > maxL.TimeoutMS || cfg.Limits.MemoryBytes > maxL.MemoryBytes ||
		cfg.Limits.CPUMillicores > maxL.CPUMillicores || cfg.Limits.PIDs > maxL.PIDs ||
		cfg.Limits.NetworkBytes > maxL.NetworkBytes || cfg.Limits.HTTPRequests > maxL.HTTPRequests {
		issues = append(issues, ValidationIssue{
			Pointer: "/limits",
			Code:    ErrCodePolicyDenied,
			Message: "Requested limits exceed local_policy.max_limits",
		})
	}
	maxB := m.LocalPolicy.MaxDailyBudget
	if cfg.DailyBudget.Runs > maxB.Runs || cfg.DailyBudget.Navigations > maxB.Navigations || cfg.DailyBudget.NetworkBytes > maxB.NetworkBytes {
		issues = append(issues, ValidationIssue{
			Pointer: "/daily_budget",
			Code:    ErrCodePolicyDenied,
			Message: "Requested daily_budget exceeds local_policy.max_daily_budget",
		})
	}
	for _, s := range cfg.Services {
		if s.Experiment != nil {
			if !slices.Contains(m.LocalPolicy.Permissions, "profile.persist") {
				issues = append(issues, ValidationIssue{
					Pointer: "/services",
					Code:    ErrCodePermissionDenied,
					Message: "Experiment requires profile.persist permission",
				})
			}
			if s.Experiment.Geolocation != nil && !slices.Contains(m.LocalPolicy.Permissions, "geolocation.override") {
				issues = append(issues, ValidationIssue{
					Pointer: "/services",
					Code:    ErrCodePermissionDenied,
					Message: "Geolocation override requires geolocation.override permission",
				})
			}
		}
	}
	return issues
}

func isKnownMethod(method string) bool {
	switch method {
	case MethodHello, MethodStatusGet, MethodConfigValidate, MethodConfigApply,
		MethodRunSubmit, MethodRunGet, MethodRunCancel,
		MethodControlPause, MethodControlResume, MethodRecordsList, MethodRecordsAck:
		return true
	default:
		return false
	}
}

func isWriteMethod(method string) bool {
	switch method {
	case MethodConfigApply, MethodRunSubmit, MethodRunCancel,
		MethodControlPause, MethodControlResume, MethodRecordsAck:
		return true
	default:
		return false
	}
}

func requiredFeature(method string) string {
	switch method {
	case MethodConfigValidate, MethodConfigApply:
		return "config"
	case MethodRunSubmit, MethodRunGet:
		return "run"
	case MethodRunCancel:
		return "cancel"
	case MethodControlPause, MethodControlResume:
		return "pause"
	case MethodRecordsList, MethodRecordsAck:
		return "journal"
	default:
		return ""
	}
}

func extractFence(req Request) (Fence, bool) {
	switch req.Method {
	case MethodConfigApply, MethodRunSubmit, MethodRunCancel, MethodControlPause, MethodControlResume, MethodRecordsAck:
		var holder struct {
			Fence Fence `json:"fence"`
		}
		if err := json.Unmarshal(req.Params, &holder); err == nil {
			return holder.Fence, true
		}
	}
	return Fence{}, false
}

func (m *MockExecutor) writeResult(w http.ResponseWriter, reqID, method, reqDigest string, result any) {
	raw, _ := json.Marshal(result)
	if reqDigest != "" {
		m.requests[reqID] = recordedRequest{
			method: method,
			digest: reqDigest,
			result: raw,
		}
	}
	m.writeRawResult(w, reqID, method, raw)
}

func (m *MockExecutor) writeRawResult(w http.ResponseWriter, reqID, method string, raw json.RawMessage) {
	resp := Response{
		Protocol:  Protocol,
		RequestID: reqID,
		Method:    method,
		OK:        true,
		Result:    raw,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *MockExecutor) writeError(w http.ResponseWriter, reqID, method string, rpcErr Error) {
	resp := Response{
		Protocol:  Protocol,
		RequestID: reqID,
		Method:    method,
		OK:        false,
		Error:     &rpcErr,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
