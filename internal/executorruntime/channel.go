package executorruntime

import (
	"errors"
	"strings"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type Enrollment struct {
	ScopeSHA256 string            `json:"scope_sha256"`
	Target      ipsentinel.Target `json:"target"`
	Host        Host              `json:"host"`
}

type ChannelInput struct {
	Action         string `json:"action"`
	Version        string `json:"version"`
	StageRequestID string `json:"stage_request_id,omitempty"`
}

type ChannelRequest struct {
	ID string `json:"id"`
	ChannelInput
	Enrollment       Enrollment        `json:"enrollment"`
	StagePlanSHA256  string            `json:"stage_plan_sha256,omitempty"`
	State            string            `json:"state"`
	CreatedAt        int64             `json:"created_at"`
	UpdatedAt        int64             `json:"updated_at"`
	ApprovedAt       int64             `json:"approved_at,omitempty"`
	PlanSHA256       string            `json:"plan_sha256,omitempty"`
	Review           *Review           `json:"review,omitempty"`
	Operation        *OperationReview  `json:"operation,omitempty"`
	Receipt          *Receipt          `json:"receipt,omitempty"`
	OperationReceipt *OperationReceipt `json:"operation_receipt,omitempty"`
	ErrorCode        string            `json:"error_code,omitempty"`
}

type ChannelResult struct {
	RequestID        string            `json:"request_id"`
	State            string            `json:"state"`
	PlanSHA256       string            `json:"plan_sha256,omitempty"`
	Review           *Review           `json:"review,omitempty"`
	Operation        *OperationReview  `json:"operation,omitempty"`
	Receipt          *Receipt          `json:"receipt,omitempty"`
	OperationReceipt *OperationReceipt `json:"operation_receipt,omitempty"`
	ErrorCode        string            `json:"error_code,omitempty"`
}

type ChannelReport struct {
	Enrollment Enrollment     `json:"enrollment"`
	Result     *ChannelResult `json:"result,omitempty"`
}

type ChannelReply struct {
	Request *ChannelRequest `json:"request,omitempty"`
}

type ChannelView struct {
	NodeID     string           `json:"node_id"`
	Enrollment Enrollment       `json:"enrollment"`
	SeenAt     int64            `json:"seen_at"`
	Requests   []ChannelRequest `json:"requests"`
}

type ChannelApproval struct {
	PlanSHA256 string `json:"plan_sha256"`
	Approved   bool   `json:"approved"`
}

func (input ChannelInput) Validate() error {
	if !tzapkg.ValidVersion(input.Version) {
		return errors.New("an exact runtime version is required")
	}
	if input.Action == "stage" && input.StageRequestID == "" {
		return nil
	}
	if (input.Action == "install" || input.Action == "select") && ipsentinel.ValidUUID(input.StageRequestID) {
		return nil
	}
	return errors.New("only stage, install and select are supported; installation and selection require a staged request")
}

func (enrollment Enrollment) Validate() error {
	if !validDigest(enrollment.ScopeSHA256) || !ipsentinel.ValidUUID(enrollment.Target.ControllerID) || !ipsentinel.ValidUUID(enrollment.Target.NodeID) {
		return errors.New("runtime channel requires a local scope and provisioned target")
	}
	host := enrollment.Host
	if (host.Platform != "linux-amd64" && host.Platform != "linux-arm64") || !tzapkg.ValidVersion(host.ProbeVersion) || !tzapkg.ValidVersion(host.AgentVersion) {
		return errors.New("runtime channel requires a supported platform and exact host versions")
	}
	return nil
}

func ActiveChannelState(state string) bool {
	return state == "requested" || state == "awaiting_approval" || state == "approved" || state == "running"
}

func ValidateChannelPlan(request ChannelRequest, result ChannelResult) error {
	if request.ChannelInput.Validate() != nil || request.Enrollment.Validate() != nil || result.RequestID != request.ID || result.State != "awaiting_approval" || !validDigest(result.PlanSHA256) || result.Receipt != nil || result.OperationReceipt != nil || result.ErrorCode != "" {
		return errors.New("invalid runtime plan report")
	}
	if request.Action == "stage" {
		if result.Review == nil || result.Operation != nil {
			return errors.New("a stage review is required")
		}
		review := result.Review
		plan := review.Plan
		digest, err := ipsentinel.CanonicalSHA256(plan)
		if err != nil || digest != result.PlanSHA256 || review.PlanSHA256 != digest || review.ApprovalTemplate != (Approval{FormatVersion: 1, PlanSHA256: digest}) ||
			plan.FormatVersion != 1 || plan.Target != request.Enrollment.Target || plan.Host != request.Enrollment.Host || plan.Version != request.Version ||
			plan.Package != tzapkg.OfficialExecutorID || !validDigest(plan.ArchiveSHA256) || !validDigest(plan.DescriptorSHA256) || len(plan.Artifacts) == 0 || len(plan.Artifacts) > 16 || len(plan.Permissions) == 0 {
			return errors.New("stage review differs from the requested node and release")
		}
		permissions := map[string]bool{}
		for _, permission := range plan.Permissions {
			if permissions[permission] || (permission != "network.public_https" && permission != "browser.execute" && permission != "profile.persist" && permission != "geolocation.override") {
				return errors.New("invalid reviewed runtime permission")
			}
			permissions[permission] = true
		}
		if !permissions["network.public_https"] || !permissions["browser.execute"] {
			return errors.New("runtime review lacks its baseline permissions")
		}
		var total int64
		seen := map[string]bool{}
		for _, artifact := range plan.Artifacts {
			if !validDigest(artifact.SHA256) || artifact.SizeBytes <= 0 || artifact.SizeBytes > 1<<30 {
				return errors.New("invalid reviewed artifact")
			}
			if !seen[artifact.SHA256] {
				total += artifact.SizeBytes
				seen[artifact.SHA256] = true
			}
		}
		if total != plan.TotalBytes {
			return errors.New("invalid reviewed byte budget")
		}
		return nil
	}
	if result.Operation == nil || result.Review != nil {
		return errors.New("an operation review is required")
	}
	review := result.Operation
	plan := review.Plan
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil || digest != result.PlanSHA256 || review.PlanSHA256 != digest ||
		review.ApprovalTemplate != (OperationApproval{FormatVersion: 1, Action: request.Action, PlanSHA256: digest}) ||
		plan.FormatVersion != 1 || plan.Options.Action != request.Action || plan.Target != request.Enrollment.Target ||
		plan.Version != request.Version || plan.StagePlanSHA256 != request.StagePlanSHA256 ||
		plan.InstallerSHA256 != ReferenceInstallerSHA256 || !validDigest(plan.ArchiveSHA256) || !validDigest(plan.SigningKeySHA256) ||
		!absoluteClean(plan.Options.InstallRoot) || plan.Options.InstallRoot == "/" || !absoluteClean(plan.Options.ReferencePath) {
		return errors.New("operation review differs from the requested node and staged release")
	}
	if request.Action == "select" && (!validDigest(plan.ConfigSHA256) || !validDigest(plan.PolicySHA256) || !absoluteClean(plan.Options.StateDir) || !absoluteClean(plan.Options.ConfigPath) || !absoluteClean(plan.Options.PolicyPath) || !executorUnit.MatchString(plan.Options.Unit)) {
		return errors.New("selection requires reviewed local configuration")
	}
	if request.Action == "install" && (plan.Options.StateDir != "" || plan.Options.ConfigPath != "" || plan.Options.PolicyPath != "" || plan.Options.Unit != "" || plan.ConfigSHA256 != "" || plan.PolicySHA256 != "") {
		return errors.New("installation cannot include selection or service configuration")
	}
	if plan.ExpectedCurrent != "" && (!strings.HasPrefix(plan.ExpectedCurrent, "releases/") || !validDigest(strings.TrimPrefix(plan.ExpectedCurrent, "releases/"))) {
		return errors.New("reviewed current runtime points outside managed releases")
	}
	if plan.SelectionSHA256 != "" && !validDigest(plan.SelectionSHA256) {
		return errors.New("reviewed selection transaction has an invalid digest")
	}
	return nil
}

func ValidateChannelReceipt(request ChannelRequest, result ChannelResult) error {
	if result.RequestID != request.ID || result.PlanSHA256 != request.PlanSHA256 || result.Review != nil || result.Operation != nil || result.ErrorCode != "" {
		return errors.New("invalid runtime receipt")
	}
	if request.Action == "stage" {
		if result.State != "staged" || result.Receipt == nil || result.OperationReceipt != nil || request.Review == nil {
			return errors.New("a stage receipt is required")
		}
		receipt := result.Receipt
		digest, err := ipsentinel.CanonicalSHA256(receipt.Plan)
		if err != nil || receipt.FormatVersion != 1 || receipt.State != "staged" || receipt.Activated || receipt.PlanSHA256 != request.PlanSHA256 || digest != request.PlanSHA256 || receipt.StagedAt.IsZero() {
			return errors.New("stage receipt differs from approval")
		}
		return nil
	}
	if result.OperationReceipt == nil || result.Receipt != nil || request.Operation == nil {
		return errors.New("an operation receipt is required")
	}
	receipt := result.OperationReceipt
	digest, err := ipsentinel.CanonicalSHA256(receipt.Plan)
	expected := "installed"
	if request.Action == "select" {
		expected = "selected"
	}
	if err != nil || receipt.FormatVersion != 1 || result.State != expected || receipt.State != expected || receipt.Activated ||
		receipt.Selected != (expected == "selected") || receipt.PlanSHA256 != request.PlanSHA256 || digest != request.PlanSHA256 {
		return errors.New("operation receipt differs from approval")
	}
	if expected == "installed" && receipt.Release != receipt.Plan.Options.InstallRoot+"/releases/"+receipt.Plan.ArchiveSHA256 {
		return errors.New("installed release path differs from approval")
	}
	if expected == "selected" && (receipt.Release != receipt.Plan.ArchiveSHA256 || !backupPath(receipt.Plan.Options.InstallRoot, receipt.Backup)) {
		return errors.New("selected release or backup differs from approval")
	}
	return nil
}
