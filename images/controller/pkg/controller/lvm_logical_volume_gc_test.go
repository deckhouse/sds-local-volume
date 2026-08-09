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

package controller

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	testGracePeriod = 30 * time.Second
	testVGName      = "test-vg"
)

// The phase-and-type keys the sweep aggregates under. Everything newLLV builds is
// Thick unless a spec says otherwise.
var (
	thickCreated = monitoring.PhaseAndType{Phase: CreatedStatusPhase, Type: LVMThickType}
	thickFailed  = monitoring.PhaseAndType{Phase: FailedStatusPhase, Type: LVMThickType}
	thickUnknown = monitoring.PhaseAndType{Phase: monitoring.PhaseUnknown, Type: LVMThickType}
)

// sweepNow is the instant every test pretends "now" is. Deletion timestamps are
// derived from it so that being inside or outside the grace period is exact rather
// than dependent on how fast the test runs.
var sweepNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

// volumeSnapshotContentGVK is the singular of volumeSnapshotContentListGVK, needed
// to put an object into a fake client.
var volumeSnapshotContentGVK = schema.GroupVersionKind{
	Group:   volumeSnapshotContentListGVK.Group,
	Version: volumeSnapshotContentListGVK.Version,
	Kind:    "VolumeSnapshotContent",
}

// testScheme knows everything the collector reads, including the
// VolumeSnapshotContent it reads unstructured. A scheme per collector:
// clientgoscheme.Scheme is a process-wide singleton, and registering onto it from a
// test couples every test in the binary together.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, snc.AddToScheme(s))
	s.AddKnownTypeWithName(volumeSnapshotContentGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(volumeSnapshotContentListGVK, &unstructured.UnstructuredList{})

	return s
}

// newGC returns a collector reading and writing the given objects. cacheObjs
// backs the cached client, liveObjs backs the uncached reader; passing different
// sets is how the specs reproduce a stale cache.
func newGC(t *testing.T, cacheObjs, liveObjs []client.Object) (*llvGarbageCollector, client.Client) {
	t.Helper()

	s := testScheme(t)

	cached := fake.NewClientBuilder().WithScheme(s).WithObjects(cacheObjs...).Build()
	live := fake.NewClientBuilder().WithScheme(s).WithObjects(liveObjs...).Build()

	return newGCWithClients(cached, live), cached
}

// newGCSharedStore returns a collector whose cached and live views are one store,
// which is what a real cluster has. A spec that plays out a sequence of writes
// needs this; only the ones about a stale cache want the two stores newGC builds.
func newGCSharedStore(t *testing.T, objs ...client.Object) (*llvGarbageCollector, client.Client) {
	t.Helper()

	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()

	return newGCWithClients(cl, cl), cl
}

// newGCWithClients is newGC for a spec that has to build its own clients, for
// instance to make one of them fail.
func newGCWithClients(cached client.Client, live client.Reader) *llvGarbageCollector {
	return &llvGarbageCollector{
		cl:          cached,
		liveReader:  live,
		log:         logger.NewNop(),
		gracePeriod: testGracePeriod,
		// Every spec sweeps once, and the ones that sweep twice want both sweeps to
		// run, so the floor is out of the way.
		minInterval: 0,
		// The production budget, so that a spec has to opt in to being truncated.
		maxReclaims: maxReclaimsPerSweep,
		now:         func() time.Time { return sweepNow },
	}
}

// newRecordingGC is newGC for a spec that asserts on the metrics rather than only on
// the returned picture, which is what the outcome of a finalizer write needs: the
// distinction between a refusal and a failure only exists in the counter.
func newRecordingGC(t *testing.T, cacheObjs, liveObjs []client.Object) (*llvGarbageCollector, func() map[string]float64) {
	t.Helper()

	st := metricsstorage.NewMetricStorage(metricsstorage.WithNewRegistry())
	require.NoError(t, monitoring.Register(st))

	gc, _ := newGC(t, cacheObjs, liveObjs)
	gc.metrics = monitoring.NewRecorder(st)

	return gc, func() map[string]float64 {
		families, err := st.Gather()
		require.NoError(t, err)

		counts := map[string]float64{}
		for _, f := range families {
			if f.GetName() != monitoring.CSIFinalizerRemovalsTotal {
				continue
			}
			for _, m := range f.GetMetric() {
				var kind, result string
				for _, l := range m.GetLabel() {
					switch l.GetName() {
					case monitoring.LabelKind:
						kind = l.GetValue()
					case monitoring.LabelResult:
						result = l.GetValue()
					}
				}
				counts[kind+"/"+result] = m.GetCounter().GetValue()
			}
		}
		return counts
	}
}

// llvOption customises the LVMLogicalVolume a spec starts from.
type llvOption func(*snc.LVMLogicalVolume)

// deletedAgo marks the volume as deleted the given time before sweepNow.
func deletedAgo(d time.Duration) llvOption {
	return func(llv *snc.LVMLogicalVolume) {
		ts := metav1.NewTime(sweepNow.Add(-d))
		llv.DeletionTimestamp = &ts
	}
}

// withFinalizers replaces the whole finalizer list.
func withFinalizers(finalizers ...string) llvOption {
	return func(llv *snc.LVMLogicalVolume) {
		llv.Finalizers = finalizers
	}
}

// withStatus sets the phase and the actual size the node agent would have
// reported.
func withStatus(phase, actualSize string) llvOption {
	return func(llv *snc.LVMLogicalVolume) {
		llv.Status = &snc.LVMLogicalVolumeStatus{
			Phase:      phase,
			ActualSize: resource.MustParse(actualSize),
		}
	}
}

// withoutStatus drops the status the node agent has not written yet.
func withoutStatus() llvOption {
	return func(llv *snc.LVMLogicalVolume) {
		llv.Status = nil
	}
}

// withType sets the LVM type, which decides how the reported size has to be read.
func withType(lvmType string) llvOption {
	return func(llv *snc.LVMLogicalVolume) {
		llv.Spec.Type = lvmType
	}
}

// retainAcknowledgedOption marks the volume as one an administrator means to keep
// beyond its PersistentVolume.
func retainAcknowledgedOption() llvOption {
	return func(llv *snc.LVMLogicalVolume) {
		llv.Annotations = map[string]string{RetainAcknowledgedAnnotation: "true"}
	}
}

// withUID pins the identity the write path compares against, so that a spec can
// make the live object a different one under the same name.
func withUID(uid string) llvOption {
	return func(llv *snc.LVMLogicalVolume) {
		llv.UID = types.UID(uid)
	}
}

// newLLV builds an LVMLogicalVolume as the CSI driver would have created it: both
// finalizers present, Created, 1Gi allocated.
func newLLV(name string, opts ...llvOption) *snc.LVMLogicalVolume {
	llv := &snc.LVMLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Finalizers: []string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		},
		Spec: snc.LVMLogicalVolumeSpec{
			ActualLVNameOnTheNode: name,
			Type:                  LVMThickType,
			LVMVolumeGroupName:    testVGName,
		},
		Status: &snc.LVMLogicalVolumeStatus{
			Phase:      CreatedStatusPhase,
			ActualSize: resource.MustParse("1Gi"),
		},
	}
	for _, opt := range opts {
		opt(llv)
	}
	return llv
}

// newCSIPV builds the PersistentVolume the provisioner creates for an
// LVMLogicalVolume. name is the resource name, handle the CSI volume handle,
// which for a dynamically provisioned volume are the same string.
func newCSIPV(name, handle string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       LocalStorageClassProvisioner,
					VolumeHandle: handle,
				},
			},
		},
	}
}

// llvsOption customises the LVMLogicalVolumeSnapshot a spec starts from.
type llvsOption func(*snc.LVMLogicalVolumeSnapshot)

// snapshotDeletedAgo marks the snapshot as deleted the given time before sweepNow.
func snapshotDeletedAgo(d time.Duration) llvsOption {
	return func(llvs *snc.LVMLogicalVolumeSnapshot) {
		ts := metav1.NewTime(sweepNow.Add(-d))
		llvs.DeletionTimestamp = &ts
	}
}

// snapshotFinalizers replaces the whole finalizer list.
func snapshotFinalizers(finalizers ...string) llvsOption {
	return func(llvs *snc.LVMLogicalVolumeSnapshot) {
		llvs.Finalizers = finalizers
	}
}

// snapshotUID is withUID for a snapshot: it pins the identity the write path
// compares against, so that a spec can make the live object a different one under
// the same name.
func snapshotUID(uid string) llvsOption {
	return func(llvs *snc.LVMLogicalVolumeSnapshot) {
		llvs.UID = types.UID(uid)
	}
}

// snapshotState returns the state reported for one snapshot, or "" when the sweep
// did not consider it.
func snapshotState(sweep monitoring.LVMLogicalVolumeSweep, name string) string {
	for _, snapshot := range sweep.OrphanedSnapshots {
		if snapshot.Name == name {
			return snapshot.State
		}
	}
	return ""
}

// snapshotReason returns the block reason reported for one snapshot, which is empty
// both for a snapshot that is not blocked and for one the sweep did not see.
func snapshotReason(sweep monitoring.LVMLogicalVolumeSweep, name string) string {
	for _, snapshot := range sweep.OrphanedSnapshots {
		if snapshot.Name == name {
			return snapshot.Reason
		}
	}
	return ""
}

// snapshotFinalizersOf is finalizersOf for a snapshot.
func snapshotFinalizersOf(t *testing.T, cl client.Client, name string) []string {
	t.Helper()

	llvs := &snc.LVMLogicalVolumeSnapshot{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: name}, llvs))
	return llvs.Finalizers
}

// newLLVS builds an LVMLogicalVolumeSnapshot as the CSI driver would have created
// it, with both finalizers present.
func newLLVS(name, sourceLLV string, opts ...llvsOption) *snc.LVMLogicalVolumeSnapshot {
	llvs := &snc.LVMLogicalVolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Finalizers: []string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		},
		Spec: snc.LVMLogicalVolumeSnapshotSpec{LVMLogicalVolumeName: sourceLLV},
		Status: &snc.LVMLogicalVolumeSnapshotStatus{
			UsedSize: resource.MustParse("128Mi"),
		},
	}
	for _, opt := range opts {
		opt(llvs)
	}
	return llvs
}

// newVolumeSnapshotContent builds the VolumeSnapshotContent the snapshot-controller
// keeps for a live snapshot. handle is the CSI snapshot ID, which is the
// LVMLogicalVolumeSnapshot name.
func newVolumeSnapshotContent(name, handle string) *unstructured.Unstructured {
	content := &unstructured.Unstructured{}
	content.SetGroupVersionKind(volumeSnapshotContentGVK)
	content.SetName(name)
	_ = unstructured.SetNestedField(content.Object, LocalStorageClassProvisioner, "spec", "driver")
	_ = unstructured.SetNestedField(content.Object, handle, "status", "snapshotHandle")
	return content
}

// finalizersOf reads the finalizers currently stored for an LVMLogicalVolume. An
// object that is gone reports nil, which a spec asserting on removal has to tell
// apart from an object that kept its finalizers.
func finalizersOf(t *testing.T, cl client.Client, name string) []string {
	t.Helper()

	llv := &snc.LVMLogicalVolume{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: name}, llv))
	return llv.Finalizers
}

// agentFinishesSnapshot plays out what sds-node-configurator does once nothing
// blocks it: the snapshot is gone from the node, so it drops its finalizer, and
// the resource — already carrying a deletion timestamp — disappears with it.
func agentFinishesSnapshot(t *testing.T, cl client.Client, name string) {
	t.Helper()

	llvs := &snc.LVMLogicalVolumeSnapshot{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: name}, llvs))
	require.Equal(t, []string{SDSNodeConfiguratorFinalizer}, llvs.Finalizers,
		"the agent only acts once it holds the only finalizer")

	llvs.Finalizers = nil
	require.NoError(t, cl.Update(context.Background(), llvs))

	err := cl.Get(context.Background(), client.ObjectKey{Name: name}, &snc.LVMLogicalVolumeSnapshot{})
	require.True(t, apierrors.IsNotFound(err), "dropping the last finalizer deletes the resource")
}

// orphanState returns the state reported for one volume, or "" when the sweep did
// not consider it an orphan.
func orphanState(sweep monitoring.LVMLogicalVolumeSweep, name string) string {
	for _, orphan := range sweep.Orphans {
		if orphan.Name == name {
			return orphan.State
		}
	}
	return ""
}

// orphanReason returns the block reason reported for one volume, which is empty
// both for an orphan that is not blocked and for one the sweep did not see.
func orphanReason(sweep monitoring.LVMLogicalVolumeSweep, name string) string {
	for _, orphan := range sweep.Orphans {
		if orphan.Name == name {
			return orphan.Reason
		}
	}
	return ""
}

// TestSweepReclaimsRetainOrphan is the issue this controller exists for: a
// PersistentVolume with reclaimPolicy: Retain is deleted, so DeleteVolume is never
// called and the CSI finalizer is never removed. Deleting the LVMLogicalVolume by
// hand must not hang on it.
func TestSweepReclaimsRetainOrphan(t *testing.T) {
	llv := newLLV("pvc-retained", deletedAgo(time.Minute))
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})

	sweep, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-retained"))
	assert.Zero(t, requeueAfter, "nothing is waiting on a grace period any more")

	// Only the CSI finalizer goes: sds-node-configurator keeps its own until it has
	// removed the logical volume, which is what actually frees the space.
	assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, finalizersOf(t, cl, "pvc-retained"))
}

// TestSweepKeepsFinalizerWhileThePersistentVolumeExists guards the dangerous
// direction: a volume that is still backing a PersistentVolume must never be
// unblocked, however it was asked to be deleted.
func TestSweepKeepsFinalizerWhileThePersistentVolumeExists(t *testing.T) {
	tests := []struct {
		name    string
		pv      *corev1.PersistentVolume
		llvName string
	}{
		{
			name:    "a dynamically provisioned PersistentVolume shares the name",
			pv:      newCSIPV("pvc-bound", "pvc-bound"),
			llvName: "pvc-bound",
		},
		{
			name: "a hand-written PersistentVolume refers to the volume only by " +
				"its CSI handle",
			pv:      newCSIPV("imported-pv", "pvc-bound"),
			llvName: "pvc-bound",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llv := newLLV(tt.llvName, deletedAgo(time.Hour))
			objs := []client.Object{llv, tt.pv}
			gc, cl := newGC(t, objs, objs)

			sweep, _, err := gc.sweep(context.Background())
			require.NoError(t, err)

			assert.Empty(t, sweep.Orphans, "a referenced volume is not an orphan")
			assert.Equal(t,
				[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
				finalizersOf(t, cl, tt.llvName),
			)
		})
	}
}

// TestSweepConfirmsAgainstTheAPIServer covers the informer lag right after
// provisioning: the cache has not seen the PersistentVolume yet, so the volume
// looks orphaned, but the live read finds it and the finalizer stays.
func TestSweepConfirmsAgainstTheAPIServer(t *testing.T) {
	llv := newLLV("pvc-fresh", deletedAgo(time.Hour))
	pv := newCSIPV("pvc-fresh", "pvc-fresh")

	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv, pv})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-fresh"))
	assert.Equal(t, monitoring.BlockReasonPersistentVolumeExists, orphanReason(sweep, "pvc-fresh"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-fresh"),
	)
}

// TestSweepConfirmsTheCSIHandleAgainstTheAPIServer is the asymmetry a Get by name
// cannot close: a hand-written PersistentVolume refers to the volume only through
// its CSI handle, so confirming by name alone would find nothing and reclaim a
// volume that is still in use. This is exactly the case the handle matching exists
// for, so the live confirmation has to cover it too.
func TestSweepConfirmsTheCSIHandleAgainstTheAPIServer(t *testing.T) {
	llv := newLLV("pvc-by-handle", deletedAgo(time.Hour))
	pv := newCSIPV("imported-pv", "pvc-by-handle")

	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv, pv})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-by-handle"))
	assert.Equal(t, monitoring.BlockReasonPersistentVolumeExists, orphanReason(sweep, "pvc-by-handle"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-by-handle"),
	)
}

// TestSweepConfirmsSnapshotsAgainstTheAPIServer covers a snapshot created while the
// sweep was running: it is in the API server but not in the cache, and unblocking
// the volume would remove the origin the snapshot came from.
func TestSweepConfirmsSnapshotsAgainstTheAPIServer(t *testing.T) {
	llv := newLLV("pvc-fresh-snapshot", deletedAgo(time.Hour))
	llvs := newLLVS("snapshot-1", "pvc-fresh-snapshot")

	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv, llvs})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-fresh-snapshot"))
	assert.Equal(t, monitoring.BlockReasonSnapshotsPresent, orphanReason(sweep, "pvc-fresh-snapshot"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-fresh-snapshot"),
	)
}

// TestSweepRefusesWhenTheLiveObjectHasLostTheAgentFinalizer is the same class as
// the two above, on the object being written rather than on what blocks it: the
// cached object says the agent owns the volume, the live one says it does not.
// Removing the CSI finalizer would remove the last one and delete the only record
// of a logical volume that may still exist on the node.
func TestSweepRefusesWhenTheLiveObjectHasLostTheAgentFinalizer(t *testing.T) {
	cached := newLLV("pvc-agent-gone", deletedAgo(time.Hour))
	live := newLLV("pvc-agent-gone",
		deletedAgo(time.Hour),
		withFinalizers(SDSLocalVolumeCSIFinalizer),
	)

	gc, _ := newGC(t, []client.Object{cached}, []client.Object{live})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-agent-gone"))
	// A property of the volume, not a failed write: reporting it as removal_failed
	// would send an operator to check RBAC and grep for write errors that are not
	// there, and would raise the alert that counts faults in this module.
	assert.Equal(t, monitoring.BlockReasonAgentFinalizerAbsent, orphanReason(sweep, "pvc-agent-gone"))

	// The live object still exists, which it would not if the finalizer had gone.
	assert.Equal(t, []string{SDSLocalVolumeCSIFinalizer}, finalizersOf(t, gc.liveReader.(client.Client), "pvc-agent-gone"))
}

// TestSweepRefusesToTouchASameNamedSuccessor is the identity check at the write.
// An LVMLogicalVolume is named after the CSI volume ID, which external-provisioner
// reuses when it retries a creation, so the name alone does not say that the live
// object is the one the sweep classified. Reclaiming the successor would destroy a
// volume nobody asked to delete.
//
// It is reported as its own reason rather than as a failed write. The situation is
// benign and self-correcting — the successor is classified on its own terms by the
// next sweep — whereas the write-failure counter is what
// D8SdsLocalVolumeCSIFinalizerRemovalErrors reads, and that alert asserts a fault in
// this module.
func TestSweepRefusesToTouchASameNamedSuccessor(t *testing.T) {
	cached := newLLV("pvc-reused", deletedAgo(time.Hour), withUID("old-uid"))
	live := newLLV("pvc-reused", deletedAgo(time.Hour), withUID("new-uid"))

	gc, removals := newRecordingGC(t, []client.Object{cached}, []client.Object{live})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-reused"))
	assert.Equal(t, monitoring.BlockReasonSuccessorInPlace, orphanReason(sweep, "pvc-reused"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, gc.liveReader.(client.Client), "pvc-reused"),
	)

	assert.Empty(t, removals(), "a refusal is not an attempted write and is counted nowhere")
}

// TestSweepRefusesToTouchASameNamedSuccessorSnapshot is the spec above on the
// snapshot half. The name of an LVMLogicalVolumeSnapshot is the CSI snapshot ID, so
// it is reused on a retried creation exactly as a volume ID is, and the two halves
// have to report the refusal the same way.
func TestSweepRefusesToTouchASameNamedSuccessorSnapshot(t *testing.T) {
	cached := newLLVS("snapshot-reused", "pvc-a", snapshotDeletedAgo(time.Hour), snapshotUID("old-uid"))
	live := newLLVS("snapshot-reused", "pvc-a", snapshotDeletedAgo(time.Hour), snapshotUID("new-uid"))

	gc, removals := newRecordingGC(t, []client.Object{cached}, []client.Object{live})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, snapshotState(sweep, "snapshot-reused"))
	assert.Equal(t, monitoring.BlockReasonSuccessorInPlace, snapshotReason(sweep, "snapshot-reused"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		snapshotFinalizersOf(t, gc.liveReader.(client.Client), "snapshot-reused"),
	)

	assert.Empty(t, removals(), "a refusal is not an attempted write and is counted nowhere")
}

// TestSweepRefusesWhenTheLiveSnapshotHasLostTheAgentFinalizer is the last-finalizer
// guard on the snapshot half: the cached object said the agent owns the snapshot and
// the live one says it does not, so writing would delete the only record of a
// snapshot that may still exist on the node.
func TestSweepRefusesWhenTheLiveSnapshotHasLostTheAgentFinalizer(t *testing.T) {
	cached := newLLVS("snapshot-1", "pvc-a", snapshotDeletedAgo(time.Hour))
	live := newLLVS("snapshot-1", "pvc-a", snapshotDeletedAgo(time.Hour),
		snapshotFinalizers(SDSLocalVolumeCSIFinalizer))

	gc, removals := newRecordingGC(t, []client.Object{cached}, []client.Object{live})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, snapshotState(sweep, "snapshot-1"))
	assert.Equal(t, monitoring.BlockReasonAgentFinalizerAbsent, snapshotReason(sweep, "snapshot-1"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer},
		snapshotFinalizersOf(t, gc.liveReader.(client.Client), "snapshot-1"),
	)

	assert.Empty(t, removals(), "a refusal is not an attempted write and is counted nowhere")
}

// TestSweepCountsAFailedWriteAsAnError is the other side of the spec above: a write
// that really failed — no permission to patch, for instance — has to reach the
// counter the alert reads, under the kind it happened to.
func TestSweepCountsAFailedWriteAsAnError(t *testing.T) {
	llv := newLLV("pvc-unwritable", deletedAgo(time.Hour))

	gc, removals := newRecordingGC(t, []client.Object{llv}, []client.Object{llv})
	gc.cl = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(llv).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
				return apierrors.NewForbidden(
					schema.GroupResource{Group: "storage.deckhouse.io", Resource: "lvmlogicalvolumes"},
					"pvc-unwritable", errors.New("no patch permission"),
				)
			},
		}).Build()

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err, "one volume that cannot be written does not fail the whole sweep")

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-unwritable"))
	assert.Equal(t, monitoring.BlockReasonRemovalFailed, orphanReason(sweep, "pvc-unwritable"))
	assert.Equal(t,
		map[string]float64{monitoring.KindLVMLogicalVolume + "/" + monitoring.ResultError: 1},
		removals(),
	)
}

// TestSweepCountsAFailedSnapshotWriteAsAnError is the spec above on the snapshot
// half, so that the counter the alert reads is proven to carry the right kind for
// the failure that really happened rather than only for the one that succeeded.
func TestSweepCountsAFailedSnapshotWriteAsAnError(t *testing.T) {
	llvs := newLLVS("snapshot-unwritable", "pvc-a", snapshotDeletedAgo(time.Hour))

	gc, removals := newRecordingGC(t, []client.Object{llvs}, []client.Object{llvs})
	gc.cl = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(llvs).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
				return apierrors.NewForbidden(
					schema.GroupResource{Group: "storage.deckhouse.io", Resource: "lvmlogicalvolumesnapshots"},
					"snapshot-unwritable", errors.New("no patch permission"),
				)
			},
		}).Build()

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err, "one snapshot that cannot be written does not fail the whole sweep")

	assert.Equal(t, monitoring.OrphanStateBlocked, snapshotState(sweep, "snapshot-unwritable"))
	assert.Equal(t, monitoring.BlockReasonRemovalFailed, snapshotReason(sweep, "snapshot-unwritable"))
	assert.Equal(t,
		map[string]float64{monitoring.KindLVMLogicalVolumeSnapshot + "/" + monitoring.ResultError: 1},
		removals(),
	)
}

// TestSweepRetriesARetriableWriteFailure separates a failure that the next attempt
// fixes from one an operator has to. retry.RetryOnConflict retried a 409 and nothing
// else, so an API server being rolled or an etcd leader election came out as a failed
// write — and D8SdsLocalVolumeCSIFinalizerRemovalErrors, which has no `for:` on
// purpose, pages on a single sample asserting a fault in this module.
func TestSweepRetriesARetriableWriteFailure(t *testing.T) {
	llv := newLLV("pvc-flaky", deletedAgo(time.Hour))

	gc, removals := newRecordingGC(t, []client.Object{llv}, []client.Object{llv})

	var attempts int
	gc.cl = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(llv).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				attempts++
				if attempts == 1 {
					return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 2, attempts, "a retriable failure has to be retried, not reported")
	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-flaky"))
	assert.Empty(t, orphanReason(sweep, "pvc-flaky"))
	assert.Equal(t,
		map[string]float64{monitoring.KindLVMLogicalVolume + "/" + monitoring.ResultSuccess: 1},
		removals(),
		"the write succeeded, so nothing may reach the failure counter",
	)
}

// TestSweepDoesNotCountACancelledWriteAsAFault covers the other way a write ends
// without an answer: sweepTimeout elapsing, or the manager cancelling an in-flight
// reconcile at shutdown. Neither is a refusal and neither is a fault — a routine
// restart during a backlog reclaim must not raise an alert about this module.
func TestSweepDoesNotCountACancelledWriteAsAFault(t *testing.T) {
	llv := newLLV("pvc-shutdown", deletedAgo(time.Hour))

	gc, removals := newRecordingGC(t, []client.Object{llv}, []client.Object{llv})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gc.cl = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(llv).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(patchCtx context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
				// What the manager does to a reconcile that is mid-write when the
				// process is asked to stop.
				cancel()
				return patchCtx.Err()
			},
		}).Build()

	sweep, _, err := gc.sweep(ctx)
	require.NoError(t, err, "a cancelled write does not fail the whole sweep")

	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-shutdown"))
	assert.Empty(t, orphanReason(sweep, "pvc-shutdown"),
		"nothing was refused: the write never got an answer")
	assert.Empty(t, removals(), "a cancelled write is neither a success nor a fault")
}

// TestSweepSeparatesTheKindsInTheRemovalCounter pins the label the error alert needs
// to name the half it is about: the two halves have different runbooks.
func TestSweepSeparatesTheKindsInTheRemovalCounter(t *testing.T) {
	objs := []client.Object{
		newLLV("pvc-a", deletedAgo(time.Hour)),
		newLLVS("snapshot-1", "pvc-b", snapshotDeletedAgo(time.Hour)),
	}

	gc, removals := newRecordingGC(t, objs, objs)

	_, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, map[string]float64{
		monitoring.KindLVMLogicalVolume + "/" + monitoring.ResultSuccess:         1,
		monitoring.KindLVMLogicalVolumeSnapshot + "/" + monitoring.ResultSuccess: 1,
	}, removals())
}

// TestSweepReportsAnAPIErrorAsItsOwnFault separates a failure of this module from a
// property of the volume. Without the distinction an operator reads "blocked" and
// goes looking for a snapshot or a PersistentVolume that is not the problem.
func TestSweepReportsAnAPIErrorAsItsOwnFault(t *testing.T) {
	llv := newLLV("pvc-unconfirmable", deletedAgo(time.Hour))
	s := testScheme(t)

	cached := fake.NewClientBuilder().WithScheme(s).WithObjects(llv).Build()
	live := fake.NewClientBuilder().WithScheme(s).WithObjects(llv).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.PersistentVolumeList); ok {
					return errors.New("the API server is unreachable")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()

	gc := newGCWithClients(cached, live)

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err, "one volume that cannot be confirmed does not fail the whole sweep")

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-unconfirmable"))
	assert.Equal(t, monitoring.BlockReasonAPIError, orphanReason(sweep, "pvc-unconfirmable"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cached, "pvc-unconfirmable"),
	)
}

// TestRemoveCSIFinalizerReportsWhetherItWrote covers the two "nothing to do" paths.
// Both are the cache being behind, and counting either as a removal would make
// sds_local_volume_csi_finalizer_removals_total report sweeps rather than reclaims.
func TestRemoveCSIFinalizerReportsWhetherItWrote(t *testing.T) {
	tests := []struct {
		name string
		live []client.Object
	}{
		{
			name: "the object is already gone",
			live: nil,
		},
		{
			name: "the finalizer is already off",
			live: []client.Object{newLLV("pvc-a", deletedAgo(time.Hour), withFinalizers(SDSNodeConfiguratorFinalizer))},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cached := newLLV("pvc-a", deletedAgo(time.Hour))
			gc, _ := newGC(t, []client.Object{cached}, tt.live)

			removed, err := gc.removeCSIFinalizer(
				context.Background(),
				monitoring.KindLVMLogicalVolume,
				cached,
				func() client.Object { return &snc.LVMLogicalVolume{} },
			)
			require.NoError(t, err, "there is nothing wrong with having nothing to do")
			assert.False(t, removed, "nothing was written, so nothing is counted as a removal")
		})
	}
}

// TestSweepOnlyReportsAnOrphanNobodyDeleted is the Retain contract: losing the
// PersistentVolume must not by itself destroy the data.
func TestSweepOnlyReportsAnOrphanNobodyDeleted(t *testing.T) {
	llv := newLLV("pvc-released")
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})

	sweep, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateActive, orphanState(sweep, "pvc-released"))
	assert.Zero(t, requeueAfter)
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-released"),
	)
}

// TestSweepWaitsOutTheGracePeriod checks that a fresh deletion is left alone for a
// while — an in-flight DeleteVolume gets to finish on its own — and that the next
// sweep is scheduled for the moment the period ends rather than for the periodic
// interval.
func TestSweepWaitsOutTheGracePeriod(t *testing.T) {
	llv := newLLV("pvc-just-deleted", deletedAgo(10*time.Second))
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})

	sweep, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-just-deleted"))
	assert.Equal(t, testGracePeriod-10*time.Second, requeueAfter)
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-just-deleted"),
	)
}

// TestSweepRefusesToBreakASnapshotChain keeps the collector from unblocking a
// logical volume that snapshots still depend on.
func TestSweepRefusesToBreakASnapshotChain(t *testing.T) {
	llv := newLLV("pvc-snapshotted", deletedAgo(time.Hour))
	llvs := newLLVS("snapshot-1", "pvc-snapshotted")

	gc, cl := newGC(t, []client.Object{llv, llvs}, []client.Object{llv, llvs})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-snapshotted"))
	assert.Equal(t, monitoring.BlockReasonSnapshotsPresent, orphanReason(sweep, "pvc-snapshotted"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-snapshotted"),
	)
}

// TestSweepRefusesWhenNoAgentOwnsTheVolume covers the case where removing the CSI
// finalizer would delete the last finalizer, and with it the only record of a
// logical volume that may still exist on the node.
func TestSweepRefusesWhenNoAgentOwnsTheVolume(t *testing.T) {
	llv := newLLV("pvc-agentless",
		deletedAgo(time.Hour),
		withFinalizers(SDSLocalVolumeCSIFinalizer),
	)
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-agentless"))
	assert.Equal(t, monitoring.BlockReasonAgentFinalizerAbsent, orphanReason(sweep, "pvc-agentless"))
	assert.Equal(t, []string{SDSLocalVolumeCSIFinalizer}, finalizersOf(t, cl, "pvc-agentless"))
}

// TestSweepReclaimsASnapshot is the reason the snapshot half exists: the CSI driver
// puts the same finalizer on an LVMLogicalVolumeSnapshot and removes it only from
// DeleteSnapshot, so `d8 k delete lvmlogicalvolumesnapshot` hangs exactly the way a
// volume deletion used to — and a snapshot stuck like that keeps its source volume
// from ever being reclaimed.
func TestSweepReclaimsASnapshot(t *testing.T) {
	llvs := newLLVS("snapshot-1", "pvc-a", snapshotDeletedAgo(time.Hour))
	gc, cl := newGC(t, []client.Object{llvs}, []client.Object{llvs})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	require.Len(t, sweep.OrphanedSnapshots, 1)
	assert.Equal(t, monitoring.OrphanStateTerminating, sweep.OrphanedSnapshots[0].State)
	assert.Equal(t, "pvc-a", sweep.OrphanedSnapshots[0].LVMLogicalVolume)

	llvsAfter := &snc.LVMLogicalVolumeSnapshot{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "snapshot-1"}, llvsAfter))
	assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, llvsAfter.Finalizers)
}

// TestSweepKeepsASnapshotItsVolumeSnapshotContentStillRefersTo is the snapshot
// counterpart of the PersistentVolume check: the resource underneath a live
// snapshot was deleted by hand, and unblocking it would destroy a snapshot nobody
// asked to be rid of.
func TestSweepKeepsASnapshotItsVolumeSnapshotContentStillRefersTo(t *testing.T) {
	llvs := newLLVS("snapshot-1", "pvc-a", snapshotDeletedAgo(time.Hour))
	content := newVolumeSnapshotContent("snapcontent-1", "snapshot-1")

	gc, cl := newGC(t, []client.Object{llvs}, []client.Object{llvs, content})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	require.Len(t, sweep.OrphanedSnapshots, 1)
	assert.Equal(t, monitoring.OrphanStateBlocked, sweep.OrphanedSnapshots[0].State)
	assert.Equal(t, monitoring.BlockReasonVolumeSnapshotContentExists, sweep.OrphanedSnapshots[0].Reason)

	llvsAfter := &snc.LVMLogicalVolumeSnapshot{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "snapshot-1"}, llvsAfter))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		llvsAfter.Finalizers,
	)
}

// TestSweepLeavesSnapshotsNobodyDeletedAlone keeps the snapshot half off everything
// that is merely there. A snapshot has no PersistentVolume, so there is no orphan
// state for one to be in.
func TestSweepLeavesSnapshotsNobodyDeletedAlone(t *testing.T) {
	llvs := newLLVS("snapshot-1", "pvc-a")
	gc, cl := newGC(t, []client.Object{llvs}, []client.Object{llvs})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Empty(t, sweep.OrphanedSnapshots)

	llvsAfter := &snc.LVMLogicalVolumeSnapshot{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "snapshot-1"}, llvsAfter))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		llvsAfter.Finalizers,
	)
}

// TestSweepReclaimsASnapshotChainInOrder is the whole point of collecting snapshots
// here rather than leaving it as a known gap: a snapshot stuck on its own finalizer
// blocks its source, and the runbook step "delete the snapshots first" has to
// actually complete. The source unblocks on the pass after the snapshot is gone.
func TestSweepReclaimsASnapshotChainInOrder(t *testing.T) {
	llv := newLLV("pvc-a", deletedAgo(time.Hour))
	llvs := newLLVS("snapshot-1", "pvc-a", snapshotDeletedAgo(time.Hour))

	gc, cl := newGCSharedStore(t, llv, llvs)

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	// The snapshot still exists as far as this pass is concerned, so the source is
	// held back rather than reclaimed underneath it.
	assert.Equal(t, monitoring.OrphanStateBlocked, orphanState(sweep, "pvc-a"))
	assert.Equal(t, monitoring.BlockReasonSnapshotsPresent, orphanReason(sweep, "pvc-a"))
	require.Len(t, sweep.OrphanedSnapshots, 1)
	assert.Equal(t, monitoring.OrphanStateTerminating, sweep.OrphanedSnapshots[0].State)

	// The agent removes the snapshot on the node and drops its finalizer, which is
	// what makes the resource go away.
	agentFinishesSnapshot(t, cl, "snapshot-1")

	sweep, _, err = gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-a"))
	assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, finalizersOf(t, cl, "pvc-a"))
}

// TestSweepReportsAnAcknowledgedRetainSeparately covers the annotation an operator
// uses to say that a volume outliving its PersistentVolume is the intent. Without
// it a cluster that keeps Retain volumes on purpose can only silence the alert, and
// then sees nothing when a real leak appears.
func TestSweepReportsAnAcknowledgedRetainSeparately(t *testing.T) {
	llv := newLLV("pvc-kept", retainAcknowledgedOption())
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateRetained, orphanState(sweep, "pvc-kept"))
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-kept"),
	)
}

// TestSweepStillReclaimsAnAcknowledgedVolume pins what the annotation does not do:
// it answers the "is this a leak" question, it does not protect anything. A
// deletion an administrator asks for is still unblocked.
func TestSweepStillReclaimsAnAcknowledgedVolume(t *testing.T) {
	llv := newLLV("pvc-kept", retainAcknowledgedOption(), deletedAgo(time.Hour))
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-kept"))
	assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, finalizersOf(t, cl, "pvc-kept"))
}

// TestSweepCountsWhatIsWaitingOnTheAgent is the leak one step further down the
// chain: this module has already let go, so the resource is in no orphan series,
// and if sds-node-configurator never finishes the space is held with nothing
// reporting it. This counter is what D8SdsLocalVolumeLVMLogicalVolumeDeletionStuck
// reads.
func TestSweepCountsWhatIsWaitingOnTheAgent(t *testing.T) {
	objs := []client.Object{
		newLLV("pvc-handed-over", deletedAgo(time.Hour), withFinalizers(SDSNodeConfiguratorFinalizer)),
		newLLVS("snapshot-handed-over", "pvc-a",
			snapshotDeletedAgo(time.Hour),
			snapshotFinalizers(SDSNodeConfiguratorFinalizer),
		),
		// Neither of these is waiting on the agent: one is not being deleted, the
		// other never had an agent take ownership.
		newLLV("pvc-live", withFinalizers(SDSNodeConfiguratorFinalizer)),
		newLLV("pvc-agentless", deletedAgo(time.Hour), withFinalizers(SDSLocalVolumeCSIFinalizer)),
	}
	gc, _ := newGC(t, objs, objs)

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, map[string]int{
		monitoring.KindLVMLogicalVolume:         1,
		monitoring.KindLVMLogicalVolumeSnapshot: 1,
	}, sweep.AwaitingAgentByKind)
	assert.Empty(t, orphanState(sweep, "pvc-handed-over"), "it carries no finalizer of this module any more")
}

// TestSweepIgnoresVolumesItDoesNotOwn keeps the collector off LVMLogicalVolumes it
// has no finalizer on: it must not touch them and must not call them orphans,
// because a hand-made LVMLogicalVolume legitimately has no PersistentVolume and
// would otherwise be reported as a leak on every cluster using
// sds-node-configurator directly.
//
// It is still counted. The CSI driver removes its finalizer before issuing the
// delete, so a restart in between leaves a volume this module created and can no
// longer recognise — space that nothing else would report at all.
func TestSweepIgnoresVolumesItDoesNotOwn(t *testing.T) {
	llv := newLLV("hand-made", withFinalizers(SDSNodeConfiguratorFinalizer))
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Empty(t, sweep.Orphans, "a volume the module has no finalizer on is not its orphan")
	assert.Equal(t, map[monitoring.PhaseAndType]int{thickCreated: 1}, sweep.CountBy,
		"the space it holds is still reported")
	assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, finalizersOf(t, cl, "hand-made"))
}

// TestSweepAggregatesByPhaseAndType checks the numbers the dashboard and the alerts
// read.
//
// The type is part of the key because the node agent reports LV_SIZE as the actual
// size for both kinds of volume: for a Thick one that is space held in the volume
// group, for a Thin one it is the virtual size and unrelated to what the thin pool
// consumes. Summing the two would present overprovisioning as allocated space and
// send an operator deleting data to reclaim a number that was never held.
func TestSweepAggregatesByPhaseAndType(t *testing.T) {
	thinCreated := monitoring.PhaseAndType{Phase: CreatedStatusPhase, Type: LVMThinType}

	objs := []client.Object{
		newLLV("pvc-a", withStatus(CreatedStatusPhase, "1Gi")),
		newLLV("pvc-b", withStatus(CreatedStatusPhase, "2Gi")),
		newLLV("pvc-c", withStatus(FailedStatusPhase, "0")),
		newLLV("pvc-d", withoutStatus()),
		newLLV("pvc-e", withType(LVMThinType), withStatus(CreatedStatusPhase, "100Gi")),
		newCSIPV("pvc-a", "pvc-a"),
		newCSIPV("pvc-b", "pvc-b"),
		newCSIPV("pvc-c", "pvc-c"),
		newCSIPV("pvc-d", "pvc-d"),
		newCSIPV("pvc-e", "pvc-e"),
	}
	gc, _ := newGC(t, objs, objs)

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, map[monitoring.PhaseAndType]int{
		thickCreated: 2,
		thickFailed:  1,
		thickUnknown: 1,
		thinCreated:  1,
	}, sweep.CountBy)

	assert.Equal(t, map[monitoring.PhaseAndType]float64{
		thickCreated: 3 << 30,
		thickFailed:  0,
		thickUnknown: 0,
		thinCreated:  100 << 30,
	}, sweep.AllocatedBytesBy)
}

// TestSweepReportsTheTypeOfAnOrphan carries the same distinction onto the per-orphan
// series, which is where an operator decides what to delete.
func TestSweepReportsTheTypeOfAnOrphan(t *testing.T) {
	objs := []client.Object{
		newLLV("pvc-thick"),
		newLLV("pvc-thin", withType(LVMThinType)),
	}
	gc, _ := newGC(t, objs, objs)

	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)

	byName := map[string]string{}
	for _, orphan := range sweep.Orphans {
		byName[orphan.Name] = orphan.Type
	}
	assert.Equal(t, map[string]string{"pvc-thick": LVMThickType, "pvc-thin": LVMThinType}, byName)
}

// TestSweepReportsTheShortestGracePeriod makes sure the requeue is driven by the
// orphan that becomes reclaimable first, not by whichever one is listed first.
func TestSweepReportsTheShortestGracePeriod(t *testing.T) {
	objs := []client.Object{
		newLLV("pvc-early", deletedAgo(5*time.Second)),
		newLLV("pvc-late", deletedAgo(25*time.Second)),
	}
	gc, _ := newGC(t, objs, objs)

	_, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, testGracePeriod-25*time.Second, requeueAfter)
}

// TestSweepToleratesAZeroRecorder documents that the collector works without
// metrics wired up, which is how the specs above run.
func TestSweepToleratesAZeroRecorder(t *testing.T) {
	llv := newLLV("pvc-retained", deletedAgo(time.Minute))
	gc, _ := newGC(t, []client.Object{llv}, []client.Object{llv})

	assert.NotPanics(t, func() {
		_, _, err := gc.sweep(context.Background())
		require.NoError(t, err)
	})
}

// TestSweepHonoursItsMinimumInterval keeps PersistentVolume churn from driving the
// collector back to back: a burst only coalesces while a sweep is in flight, and
// every sweep lists and deep-copies every LVMLogicalVolume and PersistentVolume.
func TestSweepHonoursItsMinimumInterval(t *testing.T) {
	llv := newLLV("pvc-retained", deletedAgo(time.Minute))
	gc, cl := newGC(t, []client.Object{llv}, []client.Object{llv})
	gc.minInterval = 5 * time.Second

	// The first sweep always runs, whatever the floor: a freshly started collector
	// has to publish a picture rather than wait out an interval.
	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, sweep.Orphans)
	require.Equal(t, []string{SDSNodeConfiguratorFinalizer}, finalizersOf(t, cl, "pvc-retained"))

	// "now" is frozen, so the second sweep is asked for at the same instant.
	sweep, requeueAfter, err := gc.sweep(context.Background())

	// The sentinel is what keeps a throttled pass out of the reconcile metrics: it
	// did no work, so counting it would make them report passes rather than sweeps.
	require.ErrorIs(t, err, errSweepThrottled)
	assert.Empty(t, sweep.Orphans, "a throttled sweep reports nothing and leaves the last picture published")
	assert.Equal(t, 5*time.Second, requeueAfter, "and asks to be run again when the floor elapses")

	// Past the floor it runs again.
	gc.now = func() time.Time { return sweepNow.Add(6 * time.Second) }
	sweep, _, err = gc.sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[monitoring.PhaseAndType]int{thickCreated: 1}, sweep.CountBy)
}

// TestSweepBoundsHowManyReclaimsOnePassPerforms is the backlog case: the reported
// cluster carried 189 orphans, and reclaiming all of them in one pass would hold the
// sweep lock — and freeze every gauge at its pre-sweep value — for the minutes that
// takes.
//
// A truncated pass still classifies and publishes everything, so the picture stays
// complete; only the writes are deferred, and the volumes not reached are reported as
// what they are rather than as refused.
func TestSweepBoundsHowManyReclaimsOnePassPerforms(t *testing.T) {
	objs := []client.Object{
		newLLV("pvc-a", deletedAgo(time.Hour)),
		newLLV("pvc-b", deletedAgo(time.Hour)),
		newLLV("pvc-c", deletedAgo(time.Hour)),
	}
	gc, cl := newGC(t, objs, objs)
	gc.maxReclaims = 2

	sweep, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	// Every volume is in the published picture, and none of them is blocked: nothing
	// was refused, the work was deferred.
	require.Len(t, sweep.Orphans, 3)
	for _, orphan := range sweep.Orphans {
		assert.Equal(t, monitoring.OrphanStateTerminating, orphan.State, orphan.Name)
		assert.Empty(t, orphan.Reason, orphan.Name)
	}

	// Exactly two finalizers came off, whichever two the list order reached first.
	reclaimed := 0
	for _, name := range []string{"pvc-a", "pvc-b", "pvc-c"} {
		if !slices.Contains(finalizersOf(t, cl, name), SDSLocalVolumeCSIFinalizer) {
			reclaimed++
		}
	}
	assert.Equal(t, 2, reclaimed)

	assert.Equal(t, reclaimBacklogRequeue, requeueAfter,
		"a truncated pass has to ask to be run again, or the backlog waits for the periodic sweep")

	// The rest is picked up next time round.
	gc.maxReclaims = maxReclaimsPerSweep
	_, requeueAfter, err = gc.sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, requeueAfter, "nothing is left to defer")

	for _, name := range []string{"pvc-a", "pvc-b", "pvc-c"} {
		assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, finalizersOf(t, cl, name), name)
	}
}

// TestSweepSpendsNoBudgetOnRefusals keeps a cluster full of volumes the collector
// deliberately will not unblock from starving the ones it would: a refusal is decided
// from the cache and costs no round-trip, so it must not consume the write budget.
func TestSweepSpendsNoBudgetOnRefusals(t *testing.T) {
	objs := []client.Object{
		// Two volumes that will be refused: neither is worth a write.
		newLLV("pvc-agentless-1", deletedAgo(time.Hour), withFinalizers(SDSLocalVolumeCSIFinalizer)),
		newLLV("pvc-agentless-2", deletedAgo(time.Hour), withFinalizers(SDSLocalVolumeCSIFinalizer)),
		newLLV("pvc-reclaimable", deletedAgo(time.Hour)),
	}
	gc, cl := newGC(t, objs, objs)
	gc.maxReclaims = 1

	sweep, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t, monitoring.BlockReasonAgentFinalizerAbsent, orphanReason(sweep, "pvc-agentless-1"))
	assert.Equal(t, monitoring.BlockReasonAgentFinalizerAbsent, orphanReason(sweep, "pvc-agentless-2"))
	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-reclaimable"))
	assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, finalizersOf(t, cl, "pvc-reclaimable"))
	assert.Zero(t, requeueAfter, "the budget was enough for everything that needed a write")
}

// TestSweepBoundsHowManySnapshotReclaimsOnePassPerforms is the budget on the snapshot
// half. The bound is on the writes one pass performs and both halves spend it, so a
// truncated snapshot pass has to behave like a truncated volume one: a complete
// picture, terminating rather than refused, and a requeue for the rest.
func TestSweepBoundsHowManySnapshotReclaimsOnePassPerforms(t *testing.T) {
	objs := []client.Object{
		newLLVS("snapshot-a", "pvc-a", snapshotDeletedAgo(time.Hour)),
		newLLVS("snapshot-b", "pvc-b", snapshotDeletedAgo(time.Hour)),
	}
	gc, cl := newGC(t, objs, objs)
	gc.maxReclaims = 1

	sweep, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	require.Len(t, sweep.OrphanedSnapshots, 2)
	for _, snapshot := range sweep.OrphanedSnapshots {
		assert.Equal(t, monitoring.OrphanStateTerminating, snapshot.State, snapshot.Name)
		assert.Empty(t, snapshot.Reason, snapshot.Name)
	}

	reclaimed := 0
	for _, name := range []string{"snapshot-a", "snapshot-b"} {
		if !slices.Contains(snapshotFinalizersOf(t, cl, name), SDSLocalVolumeCSIFinalizer) {
			reclaimed++
		}
	}
	assert.Equal(t, 1, reclaimed, "exactly one finalizer came off, whichever the list order reached first")

	assert.Equal(t, reclaimBacklogRequeue, requeueAfter,
		"a truncated pass has to ask to be run again, or the backlog waits for the periodic sweep")

	// The rest is picked up next time round.
	gc.maxReclaims = maxReclaimsPerSweep
	_, requeueAfter, err = gc.sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, requeueAfter, "nothing is left to defer")

	for _, name := range []string{"snapshot-a", "snapshot-b"} {
		assert.Equal(t, []string{SDSNodeConfiguratorFinalizer}, snapshotFinalizersOf(t, cl, name), name)
	}
}

// TestSweepSpendsItsBudgetOnSnapshotsFirst pins the order the two halves run in.
//
// A snapshot is what keeps its source volume blocked, so the half that removes a
// blocker must not be scheduled behind the half it unblocks: a volume backlog large
// enough to exhaust the budget every pass would otherwise keep the snapshots from
// ever being reclaimed, and each of those snapshots is holding another volume.
func TestSweepSpendsItsBudgetOnSnapshotsFirst(t *testing.T) {
	objs := []client.Object{
		newLLV("pvc-deletable", deletedAgo(time.Hour)),
		newLLVS("snapshot-of-something-else", "pvc-other", snapshotDeletedAgo(time.Hour)),
	}
	gc, cl := newGC(t, objs, objs)
	gc.maxReclaims = 1

	sweep, requeueAfter, err := gc.sweep(context.Background())
	require.NoError(t, err)

	assert.Equal(t,
		[]string{SDSNodeConfiguratorFinalizer},
		snapshotFinalizersOf(t, cl, "snapshot-of-something-else"),
		"the single write of this pass has to go to the snapshot",
	)
	assert.Equal(t,
		[]string{SDSLocalVolumeCSIFinalizer, SDSNodeConfiguratorFinalizer},
		finalizersOf(t, cl, "pvc-deletable"),
		"the volume is deferred, not refused",
	)

	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-deletable"))
	assert.Empty(t, orphanReason(sweep, "pvc-deletable"))
	assert.Equal(t, reclaimBacklogRequeue, requeueAfter)
}

// TestReferencingPersistentVolumesReportsARealReferrer covers the overlap between the
// two key spaces: a hand-written PersistentVolume's CSI handle can equal another
// PersistentVolume's name. Either way the volume is referenced, which is all the
// reclaim decision reads — but the entry is also what the log line and the runbook's
// driver check name, so it has to be one of the PersistentVolumes that really refer to
// the volume, and it has to be the same one every time.
func TestReferencingPersistentVolumesReportsARealReferrer(t *testing.T) {
	// "pvc-a" is the name of one PersistentVolume and the CSI handle of another.
	byName := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pvc-a"}}
	byHandle := newCSIPV("imported-pv", "pvc-a")

	gc, _ := newGC(t, []client.Object{byName, byHandle}, nil)

	referenced, err := gc.referencingPersistentVolumes(context.Background(), gc.cl)
	require.NoError(t, err)

	ref, inUse := referenced["pvc-a"]
	require.True(t, inUse, "both PersistentVolumes refer to the volume, so it is in use")
	assert.Contains(t, []string{"pvc-a", "imported-pv"}, ref.Name,
		"the reported referrer has to be one of the PersistentVolumes that refer to the volume")

	// Whichever of the two the list order reaches first is kept, so a second pass over
	// an unchanged cluster names the same object rather than alternating.
	again, err := gc.referencingPersistentVolumes(context.Background(), gc.cl)
	require.NoError(t, err)
	assert.Equal(t, ref, again["pvc-a"])
}

// TestStripPersistentVolumeKeepsWhatASweepReads pins the contract between the cache
// transform and the sweep. Both the unit specs and the envtest suite bypass the
// transform, so without this an edit that dropped a field the sweep depends on
// would break the reclaim with every other test still green.
func TestStripPersistentVolumeKeepsWhatASweepReads(t *testing.T) {
	deletedAt := metav1.NewTime(sweepNow)
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pvc-a",
			DeletionTimestamp: &deletedAt,
			Finalizers:        []string{"kubernetes.io/pv-protection"},
			Annotations:       map[string]string{"pv.kubernetes.io/provisioned-by": LocalStorageClassProvisioner},
			Labels:            map[string]string{"some": "label"},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       LocalStorageClassProvisioner,
					VolumeHandle: "pvc-handle",
					FSType:       "ext4",
				},
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}

	out, err := StripPersistentVolume(pv)
	require.NoError(t, err)

	got, ok := out.(*corev1.PersistentVolume)
	require.True(t, ok)

	// The two ways a PersistentVolume refers to an LVMLogicalVolume.
	assert.Equal(t, "pvc-a", got.Name)
	require.NotNil(t, got.Spec.CSI)
	assert.Equal(t, LocalStorageClassProvisioner, got.Spec.CSI.Driver)
	assert.Equal(t, "pvc-handle", got.Spec.CSI.VolumeHandle)

	// A PersistentVolume in Terminating still counts as a reference, so what marks
	// it as terminating has to survive.
	assert.NotNil(t, got.DeletionTimestamp)
	assert.Equal(t, []string{"kubernetes.io/pv-protection"}, got.Finalizers)

	assert.Empty(t, got.Annotations)
	assert.Empty(t, got.Labels)
	assert.Empty(t, got.Spec.Capacity)
	assert.Empty(t, got.Spec.CSI.FSType)
	assert.Empty(t, got.Status.Phase)
}

// TestStripPersistentVolumeIsIdempotent covers what client-go requires of a
// transform: it is called again when the same object is re-added to the store.
func TestStripPersistentVolumeIsIdempotent(t *testing.T) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-a"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       LocalStorageClassProvisioner,
					VolumeHandle: "pvc-a",
				},
			},
		},
	}

	once, err := StripPersistentVolume(pv)
	require.NoError(t, err)
	twice, err := StripPersistentVolume(once)
	require.NoError(t, err)

	assert.Equal(t, once, twice)
}

// TestStripPersistentVolumePassesThroughAnythingElse is about the tombstone: the
// cache hands the transform a cache.DeletedFinalStateUnknown, which carries no
// PersistentVolume to strip and must not be replaced by nil.
func TestStripPersistentVolumePassesThroughAnythingElse(t *testing.T) {
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "pvc-a",
		Obj: &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pvc-a"}},
	}

	out, err := StripPersistentVolume(tombstone)
	require.NoError(t, err)
	assert.Equal(t, tombstone, out)
}

func TestLLVSweepInputChanged(t *testing.T) {
	tests := []struct {
		name string
		old  *snc.LVMLogicalVolume
		new  *snc.LVMLogicalVolume
		want bool
	}{
		{
			name: "an unchanged object is not worth a sweep",
			old:  newLLV("pvc-a"),
			new:  newLLV("pvc-a"),
			want: false,
		},
		{
			name: "a deletion timestamp appearing is the event the reclaim waits for",
			old:  newLLV("pvc-a"),
			new:  newLLV("pvc-a", deletedAgo(time.Second)),
			want: true,
		},
		{
			name: "a finalizer being dropped changes who owns the volume",
			old:  newLLV("pvc-a"),
			new:  newLLV("pvc-a", withFinalizers(SDSNodeConfiguratorFinalizer)),
			want: true,
		},
		{
			name: "a phase transition changes the metrics",
			old:  newLLV("pvc-a", withStatus(FailedStatusPhase, "1Gi")),
			new:  newLLV("pvc-a", withStatus(CreatedStatusPhase, "1Gi")),
			want: true,
		},
		{
			name: "a resize changes the metrics",
			old:  newLLV("pvc-a", withStatus(CreatedStatusPhase, "1Gi")),
			new:  newLLV("pvc-a", withStatus(CreatedStatusPhase, "2Gi")),
			want: true,
		},
		{
			name: "a status appearing for the first time changes the metrics",
			old:  newLLV("pvc-a", withoutStatus()),
			new:  newLLV("pvc-a", withStatus(CreatedStatusPhase, "1Gi")),
			want: true,
		},
		{
			name: "the retain acknowledgement moves the volume to another state",
			old:  newLLV("pvc-a"),
			new:  newLLV("pvc-a", retainAcknowledgedOption()),
			want: true,
		},
		{
			name: "the volume group is a label on the orphan series",
			old:  newLLV("pvc-a"),
			new: newLLV("pvc-a", func(llv *snc.LVMLogicalVolume) {
				llv.Spec.LVMVolumeGroupName = "another-vg"
			}),
			want: true,
		},
		{
			name: "the LVM type decides how the reported size is read",
			old:  newLLV("pvc-a"),
			new:  newLLV("pvc-a", withType(LVMThinType)),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, llvSweepInputChanged(tt.old, tt.new))
		})
	}
}

func TestLLVSSweepInputChanged(t *testing.T) {
	tests := []struct {
		name string
		old  *snc.LVMLogicalVolumeSnapshot
		new  *snc.LVMLogicalVolumeSnapshot
		want bool
	}{
		{
			name: "an unchanged object is not worth a sweep",
			old:  newLLVS("snapshot-1", "pvc-a"),
			new:  newLLVS("snapshot-1", "pvc-a"),
			want: false,
		},
		{
			name: "a deletion timestamp appearing is the event the reclaim waits for",
			old:  newLLVS("snapshot-1", "pvc-a"),
			new:  newLLVS("snapshot-1", "pvc-a", snapshotDeletedAgo(time.Second)),
			want: true,
		},
		{
			name: "a finalizer being dropped changes who owns the snapshot",
			old:  newLLVS("snapshot-1", "pvc-a"),
			new:  newLLVS("snapshot-1", "pvc-a", snapshotFinalizers(SDSNodeConfiguratorFinalizer)),
			want: true,
		},
		{
			name: "the source volume is what the snapshot blocks, and a label",
			old:  newLLVS("snapshot-1", "pvc-a"),
			new:  newLLVS("snapshot-1", "pvc-b"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, llvsSweepInputChanged(tt.old, tt.new))
		})
	}
}

// TestSweepDoesNotThrottleAfterAFailure pins which sweep gets to spend the throttle
// window. A sweep that failed to list did no work, so charging it the interval
// would turn a listing error into a pass that was silently skipped: the work queue
// retries within milliseconds and the retry would be refused.
func TestSweepDoesNotThrottleAfterAFailure(t *testing.T) {
	llv := newLLV("pvc-retained", deletedAgo(time.Minute))
	s := testScheme(t)

	failing := true
	cached := fake.NewClientBuilder().WithScheme(s).WithObjects(llv).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*snc.LVMLogicalVolumeList); ok && failing {
					return errors.New("the API server is unreachable")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	live := fake.NewClientBuilder().WithScheme(s).WithObjects(llv).Build()

	gc := newGCWithClients(cached, live)
	gc.minInterval = 5 * time.Second

	_, _, err := gc.sweep(context.Background())
	require.Error(t, err)

	// "now" is frozen, so a throttle would refuse the retry outright.
	failing = false
	sweep, _, err := gc.sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, monitoring.OrphanStateTerminating, orphanState(sweep, "pvc-retained"))
}
