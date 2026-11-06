package executorruntime

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

type RuntimePeer struct {
	SocketPath string `json:"socket_path"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
}

type RuntimeIdentity struct {
	InstanceID       string               `json:"instance_id"`
	Epoch            int64                `json:"epoch"`
	DescriptorSHA256 string               `json:"descriptor_sha256"`
	RuntimeVersion   string               `json:"runtime_version"`
	DatabaseVersion  int64                `json:"database_version"`
	Config           ipsentinel.ConfigRef `json:"config"`
}

type LifecyclePlan struct {
	Operation      OperationPlan             `json:"operation"`
	Peer           RuntimePeer               `json:"peer"`
	Before         RuntimeIdentity           `json:"before"`
	UnitSHA256     string                    `json:"unit_sha256"`
	CheckpointHash string                    `json:"checkpoint_sha256,omitempty"`
	Journal        *ipsentinel.JournalStatus `json:"journal,omitempty"`
}

type LifecycleReview struct {
	Plan             LifecyclePlan     `json:"plan"`
	PlanSHA256       string            `json:"plan_sha256"`
	ApprovalTemplate OperationApproval `json:"approval_template"`
}

type LifecycleReceipt struct {
	FormatVersion int              `json:"format_version"`
	State         string           `json:"state"`
	PlanSHA256    string           `json:"plan_sha256"`
	Plan          LifecyclePlan    `json:"plan"`
	Hello         ipsentinel.Hello `json:"hello"`
}

type drainCheckpoint struct {
	Target ipsentinel.Target `json:"target"`
	Peer   RuntimePeer       `json:"peer"`
	Unit   string            `json:"unit"`
	State  string            `json:"state_dir"`
	Hello  ipsentinel.Hello  `json:"hello"`
}

type LifecycleIO interface {
	Hello(context.Context) (ipsentinel.Hello, error)
	Pause(context.Context, string, ipsentinel.Fence) error
	Service(context.Context, string, string) (string, error)
	Verify(context.Context, OperationPlan) error
}

func runtimeIdentity(hello ipsentinel.Hello) (RuntimeIdentity, error) {
	status := hello.Status
	if status.Config == nil || status.Control.Mode != ipsentinel.ModeManaged || !ipsentinel.ValidUUID(status.ExecutorInstanceID) ||
		status.Control.Epoch < 1 || !validDigest(hello.DescriptorSHA256) {
		return RuntimeIdentity{}, errors.New("a configured managed executor is required; lifecycle actions cannot adopt an executor")
	}
	observed, err := ipsentinel.ParseContractTime(status.ObservedAt)
	if err != nil || time.Since(observed) > 45*time.Second || observed.After(time.Now().Add(5*time.Second)) {
		return RuntimeIdentity{}, errors.New("executor observation is stale")
	}
	return RuntimeIdentity{status.ExecutorInstanceID, status.Control.Epoch, hello.DescriptorSHA256, hello.RuntimeVersion, hello.DatabaseVersion, *status.Config}, nil
}

func PrepareLifecycle(ctx context.Context, store Store, stageDigest string, keys map[string]ed25519.PublicKey, host Host, target ipsentinel.Target, options OperationOptions, peer RuntimePeer, local LifecycleIO) (LifecycleReview, error) {
	action := options.Action
	if (action != "drain" && action != "activate") || !absoluteClean(peer.SocketPath) || peer.UID == 0 || peer.GID == 0 || local == nil {
		return LifecycleReview{}, errors.New("lifecycle actions require an explicit non-root socket peer and fixed service")
	}
	options.Action = "select"
	review, err := PrepareOperation(ctx, store, stageDigest, keys, host, target, options)
	if err != nil {
		return LifecycleReview{}, err
	}
	review.Plan.Options.Action = action
	plan := LifecyclePlan{Operation: review.Plan, Peer: peer}
	unit, err := local.Service(ctx, "definition", options.Unit)
	if err != nil || unit == "" {
		return LifecycleReview{}, errors.New("cannot verify the local service definition")
	}
	plan.UnitSHA256 = hashBytes([]byte(unit))
	if action == "drain" {
		state, err := local.Service(ctx, "state", options.Unit)
		if err != nil || state != "active" || plan.Operation.ExpectedCurrent == "" {
			return LifecycleReview{}, errors.New("drain requires a running selected executor")
		}
		hello, err := local.Hello(ctx)
		if err != nil {
			return LifecycleReview{}, err
		}
		plan.Before, err = runtimeIdentity(hello)
		if err != nil {
			return LifecycleReview{}, err
		}
	} else {
		state, err := local.Service(ctx, "state", options.Unit)
		if err != nil || (state != "inactive" && state != "failed") || plan.Operation.ExpectedCurrent != "releases/"+plan.Operation.ArchiveSHA256 {
			return LifecycleReview{}, errors.New("activation requires the approved release to be selected and the service stopped")
		}
		checkpoint, raw, err := store.loadDrain()
		if err != nil || checkpoint.Target != target || checkpoint.Peer != peer || checkpoint.Unit != options.Unit || checkpoint.State != options.StateDir {
			return LifecycleReview{}, errors.New("activation requires a matching durable drain checkpoint; first adoption remains local")
		}
		plan.Before, err = runtimeIdentityAtCheckpoint(checkpoint.Hello)
		if err != nil {
			return LifecycleReview{}, err
		}
		plan.CheckpointHash = hashBytes(raw)
		plan.Journal = &checkpoint.Hello.Status.Journal
	}
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil {
		return LifecycleReview{}, err
	}
	return LifecycleReview{Plan: plan, PlanSHA256: digest, ApprovalTemplate: OperationApproval{FormatVersion: 1, Action: action, PlanSHA256: digest}}, nil
}

func runtimeIdentityAtCheckpoint(hello ipsentinel.Hello) (RuntimeIdentity, error) {
	hello.Status.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return runtimeIdentity(hello)
}

func ApplyLifecycle(ctx context.Context, store Store, review LifecycleReview, approved OperationApproval, local LifecycleIO) (LifecycleReceipt, error) {
	if local == nil {
		return LifecycleReceipt{}, errors.New("a locally provisioned lifecycle worker is required")
	}
	if err := validateLifecycleReview(review); err != nil {
		return LifecycleReceipt{}, err
	}
	if !approved.Approved || approved.FormatVersion != 1 || approved.Action != review.Plan.Operation.Options.Action || approved.PlanSHA256 != review.PlanSHA256 {
		return LifecycleReceipt{}, errors.New("explicit approval of this exact lifecycle plan is required")
	}
	plan := review.Plan
	options := plan.Operation.Options
	lock, err := os.OpenFile(filepath.Join(options.InstallRoot, ".install.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return LifecycleReceipt{}, err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return LifecycleReceipt{}, errors.New("runtime installation lock is unsafe")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return LifecycleReceipt{}, errors.New("another local runtime operation is active")
	}
	if err := checkLifecycleInputs(ctx, plan, local); err != nil {
		return LifecycleReceipt{}, err
	}
	receipt := LifecycleReceipt{FormatVersion: 1, PlanSHA256: review.PlanSHA256, Plan: plan}
	if options.Action == "drain" {
		hello, err := local.Hello(ctx)
		if err != nil {
			return receipt, err
		}
		identity, err := runtimeIdentity(hello)
		if err != nil || identity != plan.Before {
			return receipt, errors.New("executor identity changed before drain")
		}
		fence := ipsentinel.Fence{ExecutorInstanceID: identity.InstanceID, Epoch: identity.Epoch}
		if err := local.Pause(ctx, ipsentinel.NewUUID(), fence); err != nil {
			return receipt, err
		}
		for {
			hello, err = local.Hello(ctx)
			if err != nil {
				return receipt, err
			}
			identity, err = runtimeIdentity(hello)
			if err != nil || identity != plan.Before || !hello.Status.Control.Paused {
				return receipt, errors.New("executor changed or resumed during drain")
			}
			if drainedHello(hello) {
				break
			}
			if err := waitLifecycle(ctx); err != nil {
				return receipt, err
			}
		}
		if err := checkLifecycleInputs(ctx, plan, local); err != nil {
			return receipt, err
		}
		if _, err := local.Service(ctx, "stop", options.Unit); err != nil {
			return receipt, err
		}
		state, err := local.Service(ctx, "state", options.Unit)
		if err != nil || state != "inactive" {
			return receipt, errors.New("executor stop could not be confirmed")
		}
		checkpoint := drainCheckpoint{plan.Operation.Target, plan.Peer, options.Unit, options.StateDir, hello}
		if err := store.saveDrain(checkpoint); err != nil {
			return receipt, err
		}
		receipt.State, receipt.Hello = "drained", hello
		return receipt, nil
	}
	checkpoint, raw, err := store.loadDrain()
	if err != nil || hashBytes(raw) != plan.CheckpointHash || checkpoint.Target != plan.Operation.Target ||
		checkpoint.Peer != plan.Peer || checkpoint.Unit != options.Unit || checkpoint.State != options.StateDir {
		return receipt, errors.New("drain checkpoint changed before activation")
	}
	state, err := local.Service(ctx, "state", options.Unit)
	if err != nil || (state != "inactive" && state != "failed") {
		return receipt, errors.New("executor is no longer stopped")
	}
	if err := local.Verify(ctx, plan.Operation); err != nil {
		return receipt, err
	}
	if err := checkLifecycleInputs(ctx, plan, local); err != nil {
		return receipt, err
	}
	if _, err := local.Service(ctx, "start", options.Unit); err != nil {
		stopFailedActivation(local, options.Unit)
		return receipt, err
	}
	for {
		hello, err := local.Hello(ctx)
		_, freshErr := runtimeIdentity(hello)
		if err == nil && freshErr == nil && validateStarted(plan, hello) == nil {
			state, err := local.Service(ctx, "state", options.Unit)
			if err == nil && state == "active" {
				if err := checkLifecycleInputs(ctx, plan, local); err != nil {
					stopFailedActivation(local, options.Unit)
					return receipt, err
				}
				receipt.State, receipt.Hello = "started_paused", hello
				return receipt, nil
			}
		}
		if err := waitLifecycle(ctx); err != nil {
			stopFailedActivation(local, options.Unit)
			return receipt, errors.New("startup verification failed; dispatch remains paused and no database or runtime rollback was attempted")
		}
	}
}

func checkLifecycleInputs(ctx context.Context, plan LifecyclePlan, local LifecycleIO) error {
	options := plan.Operation.Options
	unit, err := local.Service(ctx, "definition", options.Unit)
	if err != nil || hashBytes([]byte(unit)) != plan.UnitSHA256 {
		return errors.New("service definition changed during lifecycle operation")
	}
	current, selection, err := selectionSnapshot(options.InstallRoot)
	if err != nil || current != plan.Operation.ExpectedCurrent || selection != plan.Operation.SelectionSHA256 {
		return errors.New("runtime selection changed during lifecycle operation")
	}
	config, configErr := readLocalFile(options.ConfigPath, 1<<20)
	policy, policyErr := readLocalFile(options.PolicyPath, 16384)
	if configErr != nil || policyErr != nil || hashBytes(config) != plan.Operation.ConfigSHA256 || hashBytes(policy) != plan.Operation.PolicySHA256 {
		return errors.New("runtime configuration changed during lifecycle operation")
	}
	return ctx.Err()
}

func drainedHello(hello ipsentinel.Hello) bool {
	status := hello.Status
	return status.Control.Paused && status.Control.ActiveRunID == nil && !status.Control.Draining && status.Journal.AckedSeq == status.Journal.LatestSeq
}

func validateStarted(plan LifecyclePlan, after ipsentinel.Hello) error {
	identity, err := runtimeIdentityAtCheckpoint(after)
	status := after.Status
	changed := identity.RuntimeVersion != plan.Before.RuntimeVersion || identity.DescriptorSHA256 != plan.Before.DescriptorSHA256
	if err != nil || identity.InstanceID != plan.Before.InstanceID || identity.Config != plan.Before.Config ||
		identity.RuntimeVersion != plan.Operation.Version || identity.DatabaseVersion < plan.Before.DatabaseVersion ||
		identity.Epoch < plan.Before.Epoch || (changed && identity.Epoch <= plan.Before.Epoch) ||
		!status.Control.Paused || status.Control.ActiveRunID != nil || status.Control.Draining ||
		(status.Health != "ready" && status.Health != "degraded") ||
		!after.LocalPolicy.HardLimitsAvailable || !ipsentinel.HasFeature(after, "run") || !ipsentinel.HasFeature(after, "journal") ||
		status.Budget == nil || plan.Journal == nil || status.Journal.LatestSeq < plan.Journal.LatestSeq ||
		status.Journal.AckedSeq < plan.Journal.AckedSeq || status.Journal.EarliestSeq > plan.Journal.AckedSeq+1 {
		return errors.New("started executor failed identity, epoch, isolation, configuration or journal continuity checks")
	}
	return nil
}

func waitLifecycle(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func stopFailedActivation(local LifecycleIO, unit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = local.Service(ctx, "stop", unit)
}

func (store Store) loadDrain() (drainCheckpoint, []byte, error) {
	var checkpoint drainCheckpoint
	root, err := store.open(false)
	if err != nil {
		return checkpoint, nil, err
	}
	defer root.Close()
	raw, err := readMetadata(root, "drained.json", 64<<10)
	if err == nil {
		err = ipsentinel.DecodeStrict(raw, &checkpoint)
	}
	return checkpoint, raw, err
}

func (store Store) saveDrain(checkpoint drainCheckpoint) error {
	root, err := store.open(false)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	name := ".drain-" + ipsentinel.NewUUID()
	if err := writeNew(root, name, raw); err != nil {
		return err
	}
	defer root.Remove(name)
	if err := root.Rename(name, "drained.json"); err != nil {
		return err
	}
	return syncDirectory(root, ".")
}

func validateLifecycleReview(review LifecycleReview) error {
	plan := review.Plan
	operation := plan.Operation
	options := operation.Options
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil || review.PlanSHA256 != digest || review.ApprovalTemplate != (OperationApproval{FormatVersion: 1, Action: options.Action, PlanSHA256: digest}) ||
		(options.Action != "drain" && options.Action != "activate") || !validDigest(plan.UnitSHA256) ||
		!absoluteClean(plan.Peer.SocketPath) || plan.Peer.UID == 0 || plan.Peer.GID == 0 ||
		!ipsentinel.ValidUUID(plan.Before.InstanceID) || plan.Before.Epoch < 1 || !validDigest(plan.Before.DescriptorSHA256) ||
		!validDigest(plan.Before.Config.SHA256) || plan.Before.Config.Revision < 1 {
		return errors.New("invalid lifecycle review")
	}
	if operation.FormatVersion != 1 || !absoluteClean(options.InstallRoot) || options.InstallRoot == "/" ||
		!absoluteClean(options.ReferencePath) || operation.InstallerSHA256 != ReferenceInstallerSHA256 ||
		!validDigest(operation.StagePlanSHA256) || !validDigest(operation.ArchiveSHA256) || !validDigest(operation.SigningKeySHA256) ||
		!absoluteClean(options.StateDir) || !absoluteClean(options.ConfigPath) || !absoluteClean(options.PolicyPath) ||
		!executorUnit.MatchString(options.Unit) || !validDigest(operation.ConfigSHA256) || !validDigest(operation.PolicySHA256) {
		return errors.New("lifecycle operation requires a verified release and fixed local configuration")
	}
	if options.Action == "drain" && (plan.CheckpointHash != "" || plan.Journal != nil || operation.ExpectedCurrent == "") {
		return errors.New("drain requires a selected runtime without an activation checkpoint")
	}
	if options.Action == "activate" && (!validDigest(plan.CheckpointHash) || plan.Journal == nil || plan.Journal.AckedSeq != plan.Journal.LatestSeq || operation.ExpectedCurrent != filepath.Join("releases", operation.ArchiveSHA256)) {
		return errors.New("activation requires its selected release and drain checkpoint")
	}
	return nil
}
