/*
Copyright 2026 Flant JSC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package monitoring declares every metric the controller exposes and provides
// the helpers used to record them.
//
// All metric names, label sets and bucket layouts live in this file so that the
// exposed surface can be reviewed in one place. Register must be called once
// during start-up; the recording helpers are safe to call from any goroutine.
package monitoring

import (
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
	"github.com/deckhouse/deckhouse/pkg/metrics-storage/options"
)

// Metric names. The sds_local_volume_ prefix is spelled out rather than being
// injected at runtime so that the names below are greppable from a dashboard or
// an alerting rule.
const (
	ReconcilesTotal          = "sds_local_volume_reconciles_total"
	ReconcileDurationSeconds = "sds_local_volume_reconcile_duration_seconds"
	LocalStorageClassPhase   = "sds_local_volume_local_storage_class_phase"
	LocalStorageClassReady   = "sds_local_volume_local_storage_class_ready"

	// LVMLogicalVolumeCount and LVMLogicalVolumeAllocatedBytes describe every
	// LVMLogicalVolume in the cluster, so that the space held in the volume groups
	// is visible without inspecting the nodes.
	//
	// Every volume is counted, not only the ones this module owns: a volume the CSI
	// driver created and then lost its finalizer on — the driver removes the
	// finalizer before issuing the delete, so a restart in between leaves exactly
	// that — is space nothing else would report.
	//
	// Both carry the LVM type, because the size means different things per type:
	// see LabelLVMType.
	LVMLogicalVolumeCount          = "sds_local_volume_lvm_logical_volume_count"
	LVMLogicalVolumeAllocatedBytes = "sds_local_volume_lvm_logical_volume_allocated_bytes"

	// OrphanedLVMLogicalVolumeCount is the aggregate orphan count. It is
	// published for every known state on every sweep, including a zero, so that
	// an alerting rule and a dashboard panel always have a series to read.
	OrphanedLVMLogicalVolumeCount = "sds_local_volume_orphaned_lvm_logical_volume_count"

	// OrphanedLVMLogicalVolumeAllocatedBytes carries one series per orphan, so
	// that the leaked volumes can be enumerated (which volume group, how much
	// space, and why a reclaim is refused) instead of only counted. Its cardinality
	// is the orphan count, which is zero on a healthy cluster.
	OrphanedLVMLogicalVolumeAllocatedBytes = "sds_local_volume_orphaned_lvm_logical_volume_allocated_bytes"

	// OrphanedLVMLogicalVolumeSnapshotCount and
	// OrphanedLVMLogicalVolumeSnapshotUsedBytes are the same pair for the
	// LVMLogicalVolumeSnapshots whose deletion the module is holding up. A stuck
	// snapshot is worth reporting on its own and also keeps the volume it was taken
	// from from being reclaimed.
	OrphanedLVMLogicalVolumeSnapshotCount     = "sds_local_volume_orphaned_lvm_logical_volume_snapshot_count"
	OrphanedLVMLogicalVolumeSnapshotUsedBytes = "sds_local_volume_orphaned_lvm_logical_volume_snapshot_used_bytes"

	// AwaitingAgentCount is how many resources are being deleted, carry no
	// finalizer of this module any more, and are waiting for sds-node-configurator
	// to remove the logical volume.
	//
	// Nothing else reports them: being outside this module's finalizer puts them
	// outside every orphan series above too, and a deletion that stops making
	// progress here leaks exactly as much space as one that never started.
	//
	// The condition is the absence of this module's finalizer, which cannot tell
	// "already unblocked by the collector" apart from "never owned by this module":
	// a hand-written LVMLogicalVolume, or one belonging to another consumer of
	// sds-node-configurator, is counted here as well. Both are the same fact —
	// a deletion now waiting entirely on the agent — which is what the series says
	// and all that a rule reading it may assume.
	//
	// It is a count of deletions in the agent's hands, not of stuck ones: every
	// deletion passes through this state, and how long it legitimately stays there
	// is decided by spec.volumeCleanup. Any rule reading it has to wait out the
	// slowest wipe the module can ask for.
	AwaitingAgentCount = "sds_local_volume_awaiting_agent_count"

	// CSIFinalizerRemovalsTotal counts the finalizers the garbage collector took
	// off orphaned LVMLogicalVolumes and LVMLogicalVolumeSnapshots.
	//
	// It carries the kind, because the two halves have different runbooks and an
	// alert on the error outcome has to be able to say which one it is about.
	CSIFinalizerRemovalsTotal = "sds_local_volume_csi_finalizer_removals_total"
)

// Label names.
const (
	LabelController       = "controller"
	LabelResult           = "result"
	LabelName             = "name"
	LabelPhase            = "phase"
	LabelState            = "state"
	LabelReason           = "reason"
	LabelKind             = "kind"
	LabelLVMVolumeGroup   = "lvm_volume_group"
	LabelLVMLogicalVolume = "lvm_logical_volume"

	// LabelLVMType is the LVMLogicalVolume's spec.type, and it is what makes the
	// byte counts readable. The node agent reports LV_SIZE as the actual size, which
	// for a Thick volume is the space held in the volume group and for a Thin one is
	// the virtual size, unrelated to the thin pool's consumption. Summing across
	// types would present overprovisioning as allocated space.
	LabelLVMType = "type"
)

// Values of the LabelKind label.
const (
	KindLVMLogicalVolume         = "LVMLogicalVolume"
	KindLVMLogicalVolumeSnapshot = "LVMLogicalVolumeSnapshot"
)

// Values of the LabelResult label.
const (
	ResultSuccess = "success"
	ResultError   = "error"
	ResultRequeue = "requeue"

	// ResultNoop is an attempt that found nothing left to do — the live object had
	// already lost the finalizer. Counting those as successes would make
	// CSIFinalizerRemovalsTotal report sweeps rather than reclaims.
	ResultNoop = "noop"
)

// Values of the LabelState label on the orphan metrics.
const (
	// OrphanStateActive is an orphan nobody asked to delete yet: its
	// PersistentVolume is gone, the LVMLogicalVolume and its logical volume are
	// still there. This is the state that leaks space silently.
	OrphanStateActive = "active"

	// OrphanStateRetained is an active orphan an administrator has marked as one
	// that is meant to outlive its PersistentVolume. It is the same situation as
	// OrphanStateActive with the question already answered, which is what keeps a
	// cluster that uses Retain on purpose from having to silence the alert.
	OrphanStateRetained = "retained"

	// OrphanStateTerminating is an orphan with a deletion timestamp whose
	// finalizer the garbage collector has not removed yet — normally only for
	// the length of the grace period.
	OrphanStateTerminating = "terminating"

	// OrphanStateBlocked is a terminating orphan the garbage collector will not
	// unblock. Which of the several refusals applies is in the LabelReason label;
	// see the BlockReason* values.
	OrphanStateBlocked = "blocked"
)

// Values of the LabelReason label on OrphanedLVMLogicalVolumeAllocatedBytes.
//
// The label is empty for an orphan that is not blocked, which is how a dashboard
// or a rule tells "no reason to report" apart from a reason it does not know.
const (
	// BlockReasonSnapshotsPresent is a volume that still has
	// LVMLogicalVolumeSnapshots taken from it. Removing the finalizer would
	// destroy the volume those snapshots came from.
	BlockReasonSnapshotsPresent = "snapshots_present"

	// BlockReasonAgentFinalizerAbsent is a volume no sds-node-configurator agent
	// has taken ownership of. Removing the CSI finalizer would remove the last
	// one, deleting the only record of a logical volume that may still exist on
	// the node.
	BlockReasonAgentFinalizerAbsent = "agent_finalizer_absent"

	// BlockReasonPersistentVolumeExists is a volume a PersistentVolume still
	// refers to, by name or by CSI volume handle, according to the API server.
	BlockReasonPersistentVolumeExists = "persistent_volume_exists"

	// BlockReasonVolumeSnapshotContentExists is the same refusal for a snapshot: a
	// VolumeSnapshotContent still refers to it, so only the resource underneath a
	// live snapshot was deleted.
	BlockReasonVolumeSnapshotContentExists = "volume_snapshot_content_exists"

	// BlockReasonSuccessorInPlace is a volume whose name is now held by a different
	// object than the one the sweep classified: external-provisioner reuses a volume
	// ID when it retries a creation, so a same-named successor can exist. Like the
	// reasons above it is a property of the world rather than a fault in this
	// module, and unlike them it needs no operator action — the successor is
	// classified on its own terms by the next sweep.
	BlockReasonSuccessorInPlace = "successor_in_place"

	// BlockReasonAPIError is a volume whose reclaim could not be confirmed
	// against the API server. Unlike the reasons above this one is a fault in
	// this module, not a property of the volume.
	BlockReasonAPIError = "api_error"

	// BlockReasonRemovalFailed is a volume whose reclaim was allowed but whose
	// finalizer could not be written off. Also a fault in this module.
	BlockReasonRemovalFailed = "removal_failed"
)

// PhaseUnknown is reported for an LVMLogicalVolume the node agent has not
// written a status onto yet, and TypeUnknown for one whose spec carries no LVM
// type. An explicit value keeps the label from being empty, which reads as a
// missing label rather than as a known state.
const (
	PhaseUnknown = "Unknown"
	TypeUnknown  = "Unknown"
)

// reconcileDurationBuckets covers a fast cache-only reconcile (single-digit
// milliseconds) up to a reconcile that waits on several API round-trips.
var reconcileDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// Register declares every metric on the given storage.
//
// Declaring metrics up front means a freshly started controller exposes them
// with a zero value instead of having them appear only once the first event is
// recorded, which keeps rate() over a restart from producing gaps.
func Register(st metricsstorage.Storage) error {
	if _, err := st.RegisterCounter(
		ReconcilesTotal,
		[]string{LabelController, LabelResult},
		options.WithHelp("Total number of reconcile invocations, partitioned by outcome."),
	); err != nil {
		return fmt.Errorf("register %s: %w", ReconcilesTotal, err)
	}

	if _, err := st.RegisterHistogram(
		ReconcileDurationSeconds,
		[]string{LabelController},
		reconcileDurationBuckets,
		options.WithHelp("How long a single reconcile invocation took, in seconds."),
	); err != nil {
		return fmt.Errorf("register %s: %w", ReconcileDurationSeconds, err)
	}

	if _, err := st.RegisterGauge(
		LocalStorageClassPhase,
		[]string{LabelName, LabelPhase},
		options.WithHelp("Current phase of a LocalStorageClass, 1 for the active phase."),
	); err != nil {
		return fmt.Errorf("register %s: %w", LocalStorageClassPhase, err)
	}

	if _, err := st.RegisterGauge(
		LocalStorageClassReady,
		[]string{LabelName},
		options.WithHelp("Whether the Ready condition of a LocalStorageClass is True, 1 or 0."),
	); err != nil {
		return fmt.Errorf("register %s: %w", LocalStorageClassReady, err)
	}

	if _, err := st.RegisterGauge(
		LVMLogicalVolumeCount,
		[]string{LabelPhase, LabelLVMType},
		options.WithHelp("Number of LVMLogicalVolumes in the cluster, by phase and LVM type."),
	); err != nil {
		return fmt.Errorf("register %s: %w", LVMLogicalVolumeCount, err)
	}

	if _, err := st.RegisterGauge(
		LVMLogicalVolumeAllocatedBytes,
		[]string{LabelPhase, LabelLVMType},
		options.WithHelp("Size of the LVMLogicalVolumes in the cluster, by phase and LVM type, in bytes. For type=\"Thick\" this is the space held in the volume group; for type=\"Thin\" it is the virtual size, which says nothing about the thin pool's consumption."),
	); err != nil {
		return fmt.Errorf("register %s: %w", LVMLogicalVolumeAllocatedBytes, err)
	}

	if _, err := st.RegisterGauge(
		OrphanedLVMLogicalVolumeCount,
		[]string{LabelState},
		options.WithHelp("Number of LVMLogicalVolumes whose PersistentVolume no longer exists, by state."),
	); err != nil {
		return fmt.Errorf("register %s: %w", OrphanedLVMLogicalVolumeCount, err)
	}

	if _, err := st.RegisterGauge(
		OrphanedLVMLogicalVolumeAllocatedBytes,
		[]string{LabelName, LabelLVMVolumeGroup, LabelLVMType, LabelState, LabelReason},
		options.WithHelp("Size of a single LVMLogicalVolume whose PersistentVolume no longer exists, in bytes, read per the type label as in "+LVMLogicalVolumeAllocatedBytes+". The reason label says why a reclaim is refused, and is empty when it is not."),
	); err != nil {
		return fmt.Errorf("register %s: %w", OrphanedLVMLogicalVolumeAllocatedBytes, err)
	}

	if _, err := st.RegisterGauge(
		OrphanedLVMLogicalVolumeSnapshotCount,
		[]string{LabelState},
		options.WithHelp("Number of LVMLogicalVolumeSnapshots that have been asked to be deleted and still carry this module's finalizer, by state."),
	); err != nil {
		return fmt.Errorf("register %s: %w", OrphanedLVMLogicalVolumeSnapshotCount, err)
	}

	if _, err := st.RegisterGauge(
		OrphanedLVMLogicalVolumeSnapshotUsedBytes,
		[]string{LabelName, LabelLVMLogicalVolume, LabelState, LabelReason},
		options.WithHelp("Space consumed by a single LVMLogicalVolumeSnapshot that has been asked to be deleted and still carries this module's finalizer, in bytes. The reason label says why a reclaim is refused, and is empty when it is not."),
	); err != nil {
		return fmt.Errorf("register %s: %w", OrphanedLVMLogicalVolumeSnapshotUsedBytes, err)
	}

	if _, err := st.RegisterGauge(
		AwaitingAgentCount,
		[]string{LabelKind},
		options.WithHelp("Number of resources whose deletion this module has already unblocked and which are waiting for sds-node-configurator to remove the logical volume, by kind."),
	); err != nil {
		return fmt.Errorf("register %s: %w", AwaitingAgentCount, err)
	}

	if _, err := st.RegisterCounter(
		CSIFinalizerRemovalsTotal,
		[]string{LabelKind, LabelResult},
		options.WithHelp("Total number of attempts to remove a CSI finalizer from an orphaned LVMLogicalVolume or LVMLogicalVolumeSnapshot, partitioned by kind and outcome."),
	); err != nil {
		return fmt.Errorf("register %s: %w", CSIFinalizerRemovalsTotal, err)
	}

	return nil
}

// Recorder records the metrics declared above.
//
// The zero value is usable and records nothing, so tests and any code path that
// has no storage wired up do not need a nil check.
type Recorder struct {
	st metricsstorage.Storage
}

// NewRecorder returns a Recorder writing into st.
func NewRecorder(st metricsstorage.Storage) Recorder {
	return Recorder{st: st}
}

// ObserveReconcile records the duration and the outcome of a single reconcile.
//
// It is meant to be deferred from a reconciler that uses named return values,
// so that every return path — including a panic unwinding through it — is
// counted exactly once:
//
//	func(ctx context.Context, req reconcile.Request) (res reconcile.Result, err error) {
//	    defer metrics.ObserveReconcile(CtrlName, time.Now(), &res, &err)
//
// start is evaluated when the defer statement runs, res and err are read when
// the deferred call fires.
func (r Recorder) ObserveReconcile(controller string, start time.Time, res *reconcile.Result, err *error) {
	if r.st == nil {
		return
	}

	r.st.HistogramObserve(
		ReconcileDurationSeconds,
		time.Since(start).Seconds(),
		map[string]string{LabelController: controller},
		reconcileDurationBuckets,
	)

	r.st.CounterAdd(ReconcilesTotal, 1, map[string]string{
		LabelController: controller,
		LabelResult:     reconcileResult(res, err),
	})
}

// reconcileResult classifies a reconcile outcome. An error wins over a requeue,
// because controller-runtime requeues on error regardless of the returned Result.
func reconcileResult(res *reconcile.Result, err *error) string {
	switch {
	case err != nil && *err != nil:
		return ResultError
	case res != nil && (res.Requeue || res.RequeueAfter > 0):
		return ResultRequeue
	default:
		return ResultSuccess
	}
}

// SetLocalStorageClassPhase publishes the phase of a single LocalStorageClass.
//
// The gauge is grouped per resource name and the group is expired before the new
// value is set, so a resource that moves from Created to Failed leaves exactly
// one series behind instead of two competing ones.
func (r Recorder) SetLocalStorageClassPhase(name, phase string) {
	if r.st == nil {
		return
	}

	grouped := r.st.Grouped()
	grouped.ExpireGroupMetricByName(localStorageClassGroup(name), LocalStorageClassPhase)
	grouped.GaugeSet(localStorageClassGroup(name), LocalStorageClassPhase, 1, map[string]string{
		LabelName:  name,
		LabelPhase: phase,
	})
}

// SetLocalStorageClassReady publishes whether the Ready condition of a single
// LocalStorageClass is True.
//
// It exists because the phase gauge cannot express the state that matters most.
// The phase vocabulary is exactly Created and Failed and has no word for a
// teardown, so a LocalStorageClass wedged in Terminating — held by a finalizer
// that never gets removed — goes on reporting phase=Created indefinitely, which
// is the one thing an operator needs to be told about and the one thing the
// phase cannot say. Ready goes to 0 there.
//
// The label set is just the resource name, so successive calls overwrite the
// same series and no expiry is needed between them; ForgetLocalStorageClass
// drops it along with the phase series when the resource goes away.
func (r Recorder) SetLocalStorageClassReady(name string, ready bool) {
	if r.st == nil {
		return
	}

	value := 0.0
	if ready {
		value = 1
	}

	r.st.Grouped().GaugeSet(localStorageClassGroup(name), LocalStorageClassReady, value, map[string]string{
		LabelName: name,
	})
}

// ForgetLocalStorageClass drops every series published for a LocalStorageClass.
// Without it a deleted resource would keep being exported until the process
// restarts.
func (r Recorder) ForgetLocalStorageClass(name string) {
	if r.st == nil {
		return
	}

	r.st.Grouped().ExpireGroupMetrics(localStorageClassGroup(name))
}

// localStorageClassGroup returns the expiry group holding the series of a single
// LocalStorageClass. The prefix keeps the group namespaced against any other
// grouped metric added later.
func localStorageClassGroup(name string) string {
	return "local-storage-class/" + name
}

// lvmLogicalVolumeSweepGroup holds every series derived from a sweep over the
// LVMLogicalVolumes. A sweep republishes all of them at once, so the whole group
// is expired first and a volume, a phase or an orphan that disappeared leaves no
// stale series behind.
const lvmLogicalVolumeSweepGroup = "lvm-logical-volume-sweep"

// OrphanedLVMLogicalVolume is one LVMLogicalVolume owned by the CSI driver whose
// PersistentVolume no longer exists.
type OrphanedLVMLogicalVolume struct {
	Name           string
	LVMVolumeGroup string
	// Type is the LVM type; see LabelLVMType for why it travels with the size.
	Type string
	// State is one of OrphanStateActive, OrphanStateRetained,
	// OrphanStateTerminating or OrphanStateBlocked.
	State string
	// Reason is one of the BlockReason* values when State is OrphanStateBlocked,
	// and empty otherwise.
	Reason         string
	AllocatedBytes float64
}

// OrphanedLVMLogicalVolumeSnapshot is one LVMLogicalVolumeSnapshot owned by the
// CSI driver whose deletion this module has been asked for and is holding up.
type OrphanedLVMLogicalVolumeSnapshot struct {
	Name string
	// LVMLogicalVolume is the volume the snapshot was taken from, which is also the
	// volume it keeps from being reclaimed.
	LVMLogicalVolume string
	// State is OrphanStateTerminating or OrphanStateBlocked. A snapshot has no
	// PersistentVolume, so there is nothing for it to be an active orphan of.
	State string
	// Reason is one of the BlockReason* values when State is OrphanStateBlocked,
	// and empty otherwise.
	Reason    string
	UsedBytes float64
}

// PhaseAndType keys the cluster-wide LVMLogicalVolume counts. Both parts are
// labels on the published series, and the type is what says how to read the bytes.
type PhaseAndType struct {
	Phase string
	Type  string
}

// LVMLogicalVolumeSweep is the full result of one pass over the
// LVMLogicalVolumes and LVMLogicalVolumeSnapshots.
//
// The counts cover every LVMLogicalVolume in the cluster (PhaseUnknown when the
// node agent has written no status yet). Orphans lists the volumes this module
// owns whose PersistentVolume is gone, OrphanedSnapshots the snapshots whose
// deletion it is holding up, and AwaitingAgentByKind counts what it has already
// let go of and sds-node-configurator has not finished removing. Passing the whole
// picture in one call is what allows the previous picture to be dropped
// atomically.
type LVMLogicalVolumeSweep struct {
	CountBy             map[PhaseAndType]int
	AllocatedBytesBy    map[PhaseAndType]float64
	Orphans             []OrphanedLVMLogicalVolume
	OrphanedSnapshots   []OrphanedLVMLogicalVolumeSnapshot
	AwaitingAgentByKind map[string]int
}

// SetLVMLogicalVolumeSweep publishes the result of one sweep.
//
// Every series from the previous sweep is expired first, so a phase that no
// longer occurs, and an orphan that has been reclaimed, stop being exported
// instead of freezing at their last value. The aggregate counters are published
// for every known state and kind even when they are zero, so that alerting rules
// and dashboards read a real zero rather than a missing series.
func (r Recorder) SetLVMLogicalVolumeSweep(sweep LVMLogicalVolumeSweep) {
	if r.st == nil {
		return
	}

	grouped := r.st.Grouped()
	grouped.ExpireGroupMetrics(lvmLogicalVolumeSweepGroup)

	for key, count := range sweep.CountBy {
		grouped.GaugeSet(lvmLogicalVolumeSweepGroup, LVMLogicalVolumeCount, float64(count), map[string]string{
			LabelPhase:   key.Phase,
			LabelLVMType: key.Type,
		})
	}

	for key, bytes := range sweep.AllocatedBytesBy {
		grouped.GaugeSet(lvmLogicalVolumeSweepGroup, LVMLogicalVolumeAllocatedBytes, bytes, map[string]string{
			LabelPhase:   key.Phase,
			LabelLVMType: key.Type,
		})
	}

	orphansByState := map[string]int{
		OrphanStateActive:      0,
		OrphanStateRetained:    0,
		OrphanStateTerminating: 0,
		OrphanStateBlocked:     0,
	}
	for _, orphan := range sweep.Orphans {
		orphansByState[orphan.State]++

		grouped.GaugeSet(lvmLogicalVolumeSweepGroup, OrphanedLVMLogicalVolumeAllocatedBytes, orphan.AllocatedBytes, map[string]string{
			LabelName:           orphan.Name,
			LabelLVMVolumeGroup: orphan.LVMVolumeGroup,
			LabelLVMType:        orphan.Type,
			LabelState:          orphan.State,
			LabelReason:         orphan.Reason,
		})
	}

	for state, count := range orphansByState {
		grouped.GaugeSet(lvmLogicalVolumeSweepGroup, OrphanedLVMLogicalVolumeCount, float64(count), map[string]string{
			LabelState: state,
		})
	}

	snapshotsByState := map[string]int{
		OrphanStateTerminating: 0,
		OrphanStateBlocked:     0,
	}
	for _, snapshot := range sweep.OrphanedSnapshots {
		snapshotsByState[snapshot.State]++

		grouped.GaugeSet(lvmLogicalVolumeSweepGroup, OrphanedLVMLogicalVolumeSnapshotUsedBytes, snapshot.UsedBytes, map[string]string{
			LabelName:             snapshot.Name,
			LabelLVMLogicalVolume: snapshot.LVMLogicalVolume,
			LabelState:            snapshot.State,
			LabelReason:           snapshot.Reason,
		})
	}

	for state, count := range snapshotsByState {
		grouped.GaugeSet(lvmLogicalVolumeSweepGroup, OrphanedLVMLogicalVolumeSnapshotCount, float64(count), map[string]string{
			LabelState: state,
		})
	}

	for _, kind := range []string{KindLVMLogicalVolume, KindLVMLogicalVolumeSnapshot} {
		grouped.GaugeSet(lvmLogicalVolumeSweepGroup, AwaitingAgentCount, float64(sweep.AwaitingAgentByKind[kind]), map[string]string{
			LabelKind: kind,
		})
	}
}

// ObserveCSIFinalizerRemoval counts one attempt to take the CSI finalizer off an
// orphaned LVMLogicalVolume or LVMLogicalVolumeSnapshot. kind is
// KindLVMLogicalVolume or KindLVMLogicalVolumeSnapshot, result is ResultSuccess,
// ResultNoop or ResultError.
func (r Recorder) ObserveCSIFinalizerRemoval(kind, result string) {
	if r.st == nil {
		return
	}

	r.st.CounterAdd(CSIFinalizerRemovalsTotal, 1, map[string]string{
		LabelKind:   kind,
		LabelResult: result,
	})
}
