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

package monitoring

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
)

// newTestRecorder returns a Recorder backed by an isolated registry plus a
// scrape function returning the current exposition text.
func newTestRecorder(t *testing.T) (Recorder, func() string) {
	t.Helper()

	st := metricsstorage.NewMetricStorage(metricsstorage.WithNewRegistry())
	require.NoError(t, Register(st))

	scrape := func() string {
		families, err := st.Gather()
		require.NoError(t, err)

		var sb strings.Builder
		for _, f := range families {
			for _, m := range f.GetMetric() {
				sb.WriteString(f.GetName())
				sb.WriteString("{")
				for _, l := range m.GetLabel() {
					sb.WriteString(l.GetName() + "=" + l.GetValue() + ",")
				}
				sb.WriteString("}")
				switch {
				case m.GetCounter() != nil:
					sb.WriteString(formatValue(m.GetCounter().GetValue()))
				case m.GetGauge() != nil:
					sb.WriteString(formatValue(m.GetGauge().GetValue()))
				case m.GetHistogram() != nil:
					sb.WriteString(strconv.FormatUint(m.GetHistogram().GetSampleCount(), 10))
				}
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}

	return NewRecorder(st), scrape
}

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func TestRegisterIsIdempotent(t *testing.T) {
	st := metricsstorage.NewMetricStorage(metricsstorage.WithNewRegistry())

	// Registering the same metric set twice must not fail. This is the property
	// that makes a hand-written MustRegister init() unnecessary, and the reason a
	// duplicate declaration cannot take the process down.
	require.NoError(t, Register(st))
	require.NoError(t, Register(st))
}

func TestObserveReconcileClassifiesOutcome(t *testing.T) {
	tests := []struct {
		name       string
		res        reconcile.Result
		err        error
		wantResult string
	}{
		{
			name:       "no error and empty result is a success",
			wantResult: ResultSuccess,
		},
		{
			name:       "RequeueAfter is a requeue",
			res:        reconcile.Result{RequeueAfter: time.Second},
			wantResult: ResultRequeue,
		},
		{
			name:       "Requeue is a requeue",
			res:        reconcile.Result{Requeue: true},
			wantResult: ResultRequeue,
		},
		{
			name:       "an error is an error",
			err:        errors.New("boom"),
			wantResult: ResultError,
		},
		{
			name: "an error wins over a requeue, because controller-runtime " +
				"requeues on error regardless of the returned Result",
			res:        reconcile.Result{RequeueAfter: time.Second},
			err:        errors.New("boom"),
			wantResult: ResultError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, scrape := newTestRecorder(t)

			res, err := tt.res, tt.err
			rec.ObserveReconcile("test-controller", time.Now(), &res, &err)

			out := scrape()
			assert.Contains(t, out, "controller=test-controller,result="+tt.wantResult)
			// The duration histogram must be observed on every outcome, not only
			// on success.
			assert.Contains(t, out, ReconcileDurationSeconds+"{controller=test-controller,}1")
		})
	}
}

func TestObserveReconcileToleratesNilPointers(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.ObserveReconcile("test-controller", time.Now(), nil, nil)

	assert.Contains(t, scrape(), "controller=test-controller,result="+ResultSuccess)
}

func TestSetLocalStorageClassPhaseKeepsOneSeriesPerResource(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.SetLocalStorageClassPhase("sc-a", "Created")
	require.Contains(t, scrape(), LocalStorageClassPhase+"{name=sc-a,phase=Created,}1")

	// A phase transition must replace the previous series rather than leave two
	// series both reporting 1, which would double-count the resource.
	rec.SetLocalStorageClassPhase("sc-a", "Failed")

	out := scrape()
	assert.Contains(t, out, LocalStorageClassPhase+"{name=sc-a,phase=Failed,}1")
	assert.NotContains(t, out, "phase=Created")
}

func TestSetLocalStorageClassPhaseKeepsResourcesIndependent(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.SetLocalStorageClassPhase("sc-a", "Created")
	rec.SetLocalStorageClassPhase("sc-b", "Failed")

	out := scrape()
	assert.Contains(t, out, "name=sc-a,phase=Created,")
	assert.Contains(t, out, "name=sc-b,phase=Failed,")
}

// The state this gauge exists for is the one the phase cannot name: a
// LocalStorageClass wedged in Terminating keeps reporting phase=Created, while
// Ready goes to 0. An alert can only be written against the latter.
func TestSetLocalStorageClassReadyTracksTheCondition(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.SetLocalStorageClassPhase("sc-a", "Created")
	rec.SetLocalStorageClassReady("sc-a", true)
	require.Contains(t, scrape(), LocalStorageClassReady+"{name=sc-a,}1")

	rec.SetLocalStorageClassReady("sc-a", false)

	out := scrape()
	assert.Contains(t, out, LocalStorageClassReady+"{name=sc-a,}0")
	// The label set is just the name, so the previous value is overwritten
	// rather than leaving two series behind.
	assert.NotContains(t, out, LocalStorageClassReady+"{name=sc-a,}1")
	// The phase is deliberately untouched by a teardown, which is exactly why
	// the readiness gauge had to be added next to it.
	assert.Contains(t, out, LocalStorageClassPhase+"{name=sc-a,phase=Created,}1")
}

func TestForgetLocalStorageClassDropsSeries(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.SetLocalStorageClassPhase("sc-a", "Created")
	rec.SetLocalStorageClassReady("sc-a", true)
	rec.SetLocalStorageClassPhase("sc-b", "Created")
	rec.ForgetLocalStorageClass("sc-a")

	out := scrape()
	// A deleted resource must stop being exported, otherwise its gauge would
	// survive until the process restarts.
	assert.NotContains(t, out, "name=sc-a")
	assert.Contains(t, out, "name=sc-b")
}

func TestZeroRecorderRecordsNothing(t *testing.T) {
	var rec Recorder

	// The zero value is what a test or an un-wired code path gets; it must not
	// panic.
	res, err := reconcile.Result{}, error(nil)
	assert.NotPanics(t, func() {
		rec.ObserveReconcile("test-controller", time.Now(), &res, &err)
		rec.SetLocalStorageClassPhase("sc-a", "Created")
		rec.SetLocalStorageClassReady("sc-a", true)
		rec.ForgetLocalStorageClass("sc-a")
		rec.SetLVMLogicalVolumeSweep(LVMLogicalVolumeSweep{})
		rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolume, ResultSuccess)
	})
}

func TestSetLVMLogicalVolumeSweepPublishesTheWholePicture(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.SetLVMLogicalVolumeSweep(LVMLogicalVolumeSweep{
		CountBy: map[PhaseAndType]int{
			{Phase: "Created", Type: "Thick"}:   2,
			{Phase: PhaseUnknown, Type: "Thin"}: 1,
		},
		AllocatedBytesBy: map[PhaseAndType]float64{
			{Phase: "Created", Type: "Thick"}:   3221225472,
			{Phase: PhaseUnknown, Type: "Thin"}: 0,
		},
		Orphans: []OrphanedLVMLogicalVolume{
			{Name: "pvc-a", LVMVolumeGroup: "vg-0", Type: "Thick", State: OrphanStateActive, AllocatedBytes: 1073741824},
			{Name: "pvc-b", LVMVolumeGroup: "vg-1", Type: "Thick", State: OrphanStateTerminating, AllocatedBytes: 2147483648},
			{
				Name:           "pvc-c",
				LVMVolumeGroup: "vg-1",
				Type:           "Thick",
				State:          OrphanStateBlocked,
				Reason:         BlockReasonSnapshotsPresent,
				AllocatedBytes: 4294967296,
			},
		},
		OrphanedSnapshots: []OrphanedLVMLogicalVolumeSnapshot{
			{
				Name:             "snapshot-1",
				LVMLogicalVolume: "pvc-c",
				State:            OrphanStateBlocked,
				Reason:           BlockReasonVolumeSnapshotContentExists,
				UsedBytes:        134217728,
			},
		},
		AwaitingAgentByKind: map[string]int{KindLVMLogicalVolume: 4},
	})

	out := scrape()
	assert.Contains(t, out, LVMLogicalVolumeCount+"{phase=Created,type=Thick,}2")
	assert.Contains(t, out, LVMLogicalVolumeCount+"{phase="+PhaseUnknown+",type=Thin,}1")
	assert.Contains(t, out, LVMLogicalVolumeAllocatedBytes+"{phase=Created,type=Thick,}3221225472")

	// An orphan that is not blocked has nothing to explain, so the reason label is
	// empty rather than carrying a value a rule would have to know to ignore.
	assert.Contains(t, out, OrphanedLVMLogicalVolumeAllocatedBytes+"{lvm_volume_group=vg-0,name=pvc-a,reason=,state="+OrphanStateActive+",type=Thick,}1073741824")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeAllocatedBytes+"{lvm_volume_group=vg-1,name=pvc-b,reason=,state="+OrphanStateTerminating+",type=Thick,}2147483648")

	// A blocked orphan names the refusal, so that neither a dashboard nor an
	// on-call engineer has to grep the controller log for it.
	assert.Contains(t, out, OrphanedLVMLogicalVolumeAllocatedBytes+"{lvm_volume_group=vg-1,name=pvc-c,reason="+BlockReasonSnapshotsPresent+",state="+OrphanStateBlocked+",type=Thick,}4294967296")

	// A blocked snapshot is reported the same way, and names the volume it is in
	// turn keeping blocked.
	assert.Contains(t, out, OrphanedLVMLogicalVolumeSnapshotUsedBytes+"{lvm_logical_volume=pvc-c,name=snapshot-1,reason="+BlockReasonVolumeSnapshotContentExists+",state="+OrphanStateBlocked+",}134217728")

	// Every state is published, including the ones with nothing in them, so that
	// an alerting rule reads a zero instead of a missing series.
	assert.Contains(t, out, OrphanedLVMLogicalVolumeCount+"{state="+OrphanStateActive+",}1")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeCount+"{state="+OrphanStateRetained+",}0")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeCount+"{state="+OrphanStateTerminating+",}1")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeCount+"{state="+OrphanStateBlocked+",}1")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeSnapshotCount+"{state="+OrphanStateBlocked+",}1")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeSnapshotCount+"{state="+OrphanStateTerminating+",}0")

	// The same holds for the kinds of the awaiting-agent counter: the alert reading
	// it must see a zero for the kind that has nothing waiting.
	assert.Contains(t, out, AwaitingAgentCount+"{kind="+KindLVMLogicalVolume+",}4")
	assert.Contains(t, out, AwaitingAgentCount+"{kind="+KindLVMLogicalVolumeSnapshot+",}0")
}

func TestSetLVMLogicalVolumeSweepDropsWhatTheSweepNoLongerSees(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.SetLVMLogicalVolumeSweep(LVMLogicalVolumeSweep{
		CountBy: map[PhaseAndType]int{
			{Phase: "Created", Type: "Thick"}: 1,
			{Phase: "Failed", Type: "Thick"}:  1,
		},
		Orphans: []OrphanedLVMLogicalVolume{
			{Name: "pvc-a", LVMVolumeGroup: "vg-0", Type: "Thick", State: OrphanStateTerminating, AllocatedBytes: 1073741824},
		},
		OrphanedSnapshots: []OrphanedLVMLogicalVolumeSnapshot{
			{Name: "snapshot-1", LVMLogicalVolume: "pvc-a", State: OrphanStateTerminating, UsedBytes: 134217728},
		},
	})
	out := scrape()
	require.Contains(t, out, "name=pvc-a")
	require.Contains(t, out, "name=snapshot-1")

	// The orphan and the snapshot have been reclaimed and the Failed volume is gone.
	// Every one of those series has to disappear: a gauge that keeps its last value
	// would report leaked space that no longer exists, and would keep an alert
	// firing forever.
	rec.SetLVMLogicalVolumeSweep(LVMLogicalVolumeSweep{
		CountBy: map[PhaseAndType]int{{Phase: "Created", Type: "Thick"}: 1},
	})

	out = scrape()
	assert.NotContains(t, out, "name=pvc-a")
	assert.NotContains(t, out, "name=snapshot-1")
	assert.NotContains(t, out, "phase=Failed")
	assert.Contains(t, out, LVMLogicalVolumeCount+"{phase=Created,type=Thick,}1")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeCount+"{state="+OrphanStateTerminating+",}0")
	assert.Contains(t, out, OrphanedLVMLogicalVolumeSnapshotCount+"{state="+OrphanStateTerminating+",}0")
}

func TestSetLVMLogicalVolumeSweepLeavesOtherMetricsAlone(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	res, err := reconcile.Result{}, error(nil)
	rec.ObserveReconcile("test-controller", time.Now(), &res, &err)
	rec.SetLocalStorageClassPhase("sc-a", "Created")
	rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolume, ResultSuccess)

	// A sweep expires its own group across every collector, so anything published
	// outside that group has to survive it.
	rec.SetLVMLogicalVolumeSweep(LVMLogicalVolumeSweep{})

	out := scrape()
	assert.Contains(t, out, "controller=test-controller,result="+ResultSuccess)
	assert.Contains(t, out, LocalStorageClassPhase+"{name=sc-a,phase=Created,}1")
	assert.Contains(t, out, CSIFinalizerRemovalsTotal+"{kind="+KindLVMLogicalVolume+",result="+ResultSuccess+",}1")
}

func TestObserveCSIFinalizerRemovalCountsByOutcome(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolume, ResultSuccess)
	rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolume, ResultSuccess)
	rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolume, ResultError)
	rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolume, ResultNoop)

	out := scrape()
	assert.Contains(t, out, CSIFinalizerRemovalsTotal+"{kind="+KindLVMLogicalVolume+",result="+ResultSuccess+",}2")
	assert.Contains(t, out, CSIFinalizerRemovalsTotal+"{kind="+KindLVMLogicalVolume+",result="+ResultError+",}1")

	// A pass that found nothing left to do is counted apart from one that wrote:
	// folding it into success would make the counter report sweeps rather than
	// reclaims, and the "CSI finalizer removals" panel would never read zero.
	assert.Contains(t, out, CSIFinalizerRemovalsTotal+"{kind="+KindLVMLogicalVolume+",result="+ResultNoop+",}1")
}

// TestObserveCSIFinalizerRemovalSeparatesTheKinds is why the counter carries the
// kind: the two halves have different runbooks, and an alert on the error outcome
// has to be able to say which one it is about.
func TestObserveCSIFinalizerRemovalSeparatesTheKinds(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolume, ResultError)
	rec.ObserveCSIFinalizerRemoval(KindLVMLogicalVolumeSnapshot, ResultSuccess)

	out := scrape()
	assert.Contains(t, out, CSIFinalizerRemovalsTotal+"{kind="+KindLVMLogicalVolume+",result="+ResultError+",}1")
	assert.Contains(t, out, CSIFinalizerRemovalsTotal+"{kind="+KindLVMLogicalVolumeSnapshot+",result="+ResultSuccess+",}1")
	assert.NotContains(t, out, CSIFinalizerRemovalsTotal+"{kind="+KindLVMLogicalVolumeSnapshot+",result="+ResultError+",}")
}
