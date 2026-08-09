/*
Copyright 2025 Flant JSC

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
	"strings"
	"testing"
	"unicode/utf8"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/deckhouse/sds-common-lib/conditions"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
)

const testLSCName = "lsc-1"

func newStatusTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return newStatusTestClientWithHooks(t, interceptor.Funcs{}, objs...)
}

func newStatusTestClientWithHooks(t *testing.T, hooks interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()

	scheme := apiruntime.NewScheme()
	if err := slv.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}
	if err := storagev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding the storage scheme: %v", err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&slv.LocalStorageClass{}).
		WithObjects(objs...).
		WithInterceptorFuncs(hooks).
		Build()
}

// recordWrites returns interceptors appending a label per write to *into, so a
// test can assert the order the controller issued them in.
//
// The fake client has no informer cache behind it, so it cannot reproduce the
// conflict a badly ordered pass causes on a real cluster; recording the calls is
// the only way to hold the ordering at this test level.
func recordWrites(into *[]string) interceptor.Funcs {
	return interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			*into = append(*into, "update")
			return c.Update(ctx, obj, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			*into = append(*into, "status")
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	}
}

func readLSC(t *testing.T, cl client.Client) *slv.LocalStorageClass {
	t.Helper()
	got := &slv.LocalStorageClass{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: testLSCName}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	return got
}

// LocalStorageClass declares the condition types it publishes; this checks that
// the writer delivers exactly those.
//
// A condition type that is declared but never written is worse than one that
// does not exist: an absent condition is indistinguishable from "not yet
// evaluated", so an operator waits for a verdict that never comes and an alert
// on it never fires. The writer was already covered; what this adds is the link
// to the declared set, which nothing but review enforced. With one condition
// type today the check is cheap; its value is the second type someone adds and
// forgets to write.
//
// Both phases are driven, because a condition written on only one of them would
// leave the other reporting nothing.
func TestEveryDeclaredConditionTypeIsWritten(t *testing.T) {
	for _, tc := range []struct {
		phase  string
		reason string
	}{
		{CreatedStatusPhase, ""},
		{FailedStatusPhase, "no LVMVolumeGroup matched"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			lsc := &slv.LocalStorageClass{
				ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
			}
			cl := newStatusTestClient(t, lsc)

			if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, tc.phase, tc.reason); err != nil {
				t.Fatalf("updateLocalStorageClassPhase: %v", err)
			}

			got := readLSC(t, cl)

			present := map[string]bool{}
			for _, c := range got.Status.Conditions {
				present[c.Type] = true

				if c.Status == "" {
					t.Errorf("condition %s needs a status", c.Type)
				}
				if c.Reason == "" {
					t.Errorf("condition %s needs a machine-readable reason", c.Type)
				}
				if c.Message == "" {
					t.Errorf("condition %s needs a message", c.Type)
				}
			}

			for _, condType := range slv.LocalStorageClassConditionTypes {
				if !present[condType] {
					t.Errorf("LocalStorageClass declares %s but the writer did not set it", condType)
				}
				delete(present, condType)
			}
			for stray := range present {
				t.Errorf("LocalStorageClass writes %s, which it does not declare", stray)
			}
		})
	}
}

func TestUpdateLocalStorageClassPhase_Created(t *testing.T) {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 3},
	}
	cl := newStatusTestClient(t, lsc)

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, CreatedStatusPhase, ""); err != nil {
		t.Fatalf("updateLocalStorageClassPhase: %v", err)
	}

	got := readLSC(t, cl)
	ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady)
	if ready == nil {
		t.Fatal("Ready condition was not published")
	}
	if ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %q, want True", ready.Status)
	}
	if ready.Reason != conditions.ReasonReconciled {
		t.Errorf("reason = %q, want %q", ready.Reason, conditions.ReasonReconciled)
	}
	if got.Status.Phase != CreatedStatusPhase {
		t.Errorf("phase = %q, want %q", got.Status.Phase, CreatedStatusPhase)
	}
	if got.Status.ObservedGeneration != 3 {
		t.Errorf("observedGeneration = %d, want 3", got.Status.ObservedGeneration)
	}

	// The finalizer used to be appended by the same full-object Update that
	// carried the status. Status now lives on its own subresource, so the two
	// are separate calls and the finalizer must still end up in place.
	if !slices.Contains(got.Finalizers, LocalStorageClassFinalizerName) {
		t.Errorf("the finalizer was not added: %v", got.Finalizers)
	}
}

func TestUpdateLocalStorageClassPhase_Failed(t *testing.T) {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
	}
	cl := newStatusTestClient(t, lsc)

	const reason = "unable to create the StorageClass"
	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, FailedStatusPhase, reason); err != nil {
		t.Fatalf("updateLocalStorageClassPhase: %v", err)
	}

	got := readLSC(t, cl)
	ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %+v", ready)
	}
	if ready.Reason != conditions.ReasonReconcileFailed {
		t.Errorf("reason = %q, want %q", ready.Reason, conditions.ReasonReconcileFailed)
	}
	if ready.Message != reason {
		t.Errorf("message = %q, want %q", ready.Message, reason)
	}
	if got.Status.Phase != FailedStatusPhase {
		t.Errorf("phase = %q, want %q", got.Status.Phase, FailedStatusPhase)
	}
}

func TestUpdateLocalStorageClassPhase_SkipsWriteWhenNothingChanges(t *testing.T) {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
	}
	cl := newStatusTestClient(t, lsc)

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, CreatedStatusPhase, ""); err != nil {
		t.Fatalf("first update: %v", err)
	}
	first := readLSC(t, cl)

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, CreatedStatusPhase, ""); err != nil {
		t.Fatalf("second update: %v", err)
	}
	second := readLSC(t, cl)

	// A resync that changes nothing must not write: otherwise every requeue
	// produces an etcd write and a watch event for every object.
	if second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("expected no write, resourceVersion moved %s -> %s",
			first.ResourceVersion, second.ResourceVersion)
	}
}

// lastTransitionTime marks when the state changed, not when the controller last
// looked. A pass that only produces a different message must not move it, or
// "Ready=False for longer than 10 minutes" can never fire.
func TestUpdateLocalStorageClassPhase_KeepsTransitionTimeAcrossMessageChange(t *testing.T) {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
	}
	cl := newStatusTestClient(t, lsc)

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, FailedStatusPhase, "first failure"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	first := conditions.Get(readLSC(t, cl).Status.Conditions, slv.ConditionTypeReady)

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, FailedStatusPhase, "second failure"); err != nil {
		t.Fatalf("second update: %v", err)
	}
	second := conditions.Get(readLSC(t, cl).Status.Conditions, slv.ConditionTypeReady)

	if second.Message != "second failure" {
		t.Errorf("message was not updated: %q", second.Message)
	}
	if !second.LastTransitionTime.Equal(&first.LastTransitionTime) {
		t.Errorf("lastTransitionTime moved %v -> %v without a status change",
			first.LastTransitionTime, second.LastTransitionTime)
	}
}

// observedGeneration must name the generation that was actually reconciled, so
// a reader can tell "reconciled and healthy" from "has not seen your edit yet".
func TestUpdateLocalStorageClassPhase_ObservedGenerationIsTheReconciledOne(t *testing.T) {
	const (
		reconciledGeneration = 4
		currentGeneration    = 5
	)

	// The fake client does not maintain metadata.generation, so both
	// generations are set explicitly rather than produced by an update.
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: currentGeneration},
	}
	cl := newStatusTestClient(t, lsc)

	reconciled := lsc.DeepCopy()
	reconciled.Generation = reconciledGeneration

	if err := updateLocalStorageClassPhase(context.Background(), cl, reconciled, CreatedStatusPhase, ""); err != nil {
		t.Fatalf("updateLocalStorageClassPhase: %v", err)
	}

	got := readLSC(t, cl)
	if got.Status.ObservedGeneration != reconciledGeneration {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, reconciledGeneration)
	}
	if !conditions.IsStale(got.Status.Conditions, slv.ConditionTypeReady, currentGeneration) {
		t.Error("the Ready condition should read as stale against the current generation")
	}
}

// The reconcile loop reads lsc.Status.Phase straight after the status write to
// publish the phase metric, so the caller's object has to carry what was
// written. conditions.UpdateStatus leaves the object it is handed untouched,
// which would leave Status nil on a resource that has never been written — and
// the metric site dereferences it.
func TestUpdateLocalStorageClassPhase_MirrorsStatusOntoTheCallersObject(t *testing.T) {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
	}
	cl := newStatusTestClient(t, lsc)

	if lsc.Status != nil {
		t.Fatal("precondition: the status should start out unset")
	}

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, CreatedStatusPhase, ""); err != nil {
		t.Fatalf("updateLocalStorageClassPhase: %v", err)
	}

	if lsc.Status == nil {
		t.Fatal("the caller's object has no status, the phase metric would panic")
	}
	if lsc.Status.Phase != CreatedStatusPhase {
		t.Errorf("caller's phase = %q, want %q", lsc.Status.Phase, CreatedStatusPhase)
	}
}

// The mirror is what the gauges read, so it must not run ahead of the write.
// The case this guards is the localstorageclasses/status RBAC rule going
// missing: every status write comes back Forbidden and the resource reports
// nothing, while metrics mirrored beforehand would report a healthy module.
func TestUpdateLocalStorageClassPhase_DoesNotMirrorARejectedWrite(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "storage.deckhouse.io", Resource: "localstorageclasses"},
		testLSCName,
		errors.New(`no RBAC for "localstorageclasses/status"`),
	)

	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
	}
	cl := newStatusTestClientWithHooks(t, interceptor.Funcs{
		SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return forbidden
		},
	}, lsc)

	err := updateLocalStorageClassPhase(context.Background(), cl, lsc, CreatedStatusPhase, "")
	if !apierrors.IsForbidden(err) {
		t.Fatalf("expected the rejection to be reported, got %v", err)
	}

	if got := readLSC(t, cl); got.Status != nil && got.Status.Phase != "" {
		t.Fatalf("precondition: nothing should have been persisted, got %+v", got.Status)
	}

	// Both gauge inputs. Either one claiming success would be a module that
	// looks healthy on the dashboard and reports nothing to kubectl.
	if lsc.Status != nil && lsc.Status.Phase != "" {
		t.Errorf("the phase gauge would report %q for a status that was never written", lsc.Status.Phase)
	}
	if lsc.Status != nil && conditions.IsTrue(lsc.Status.Conditions, slv.ConditionTypeReady) {
		t.Error("the readiness gauge would report 1 for a status that was never written")
	}
}

// The API server rejects a new finalizer on an object that already carries a
// deletionTimestamp. The delete path strips the finalizer from the in-memory
// object before its own Update, so a failed removal lands here with the
// finalizer gone — and putting it back would fail the whole call, taking with it
// the Failed verdict that explains why the deletion is stuck.
func TestUpdateLocalStorageClassPhase_DoesNotReviveTheFinalizerWhileDeleting(t *testing.T) {
	const foreignFinalizer = "example.com/keep-me"

	deletedAt := metav1.Now()
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testLSCName,
			Generation:        1,
			DeletionTimestamp: &deletedAt,
			// The fake client refuses an object that is being deleted and has
			// nothing holding it; ours is deliberately not in the list.
			Finalizers: []string{foreignFinalizer},
		},
	}
	cl := newStatusTestClient(t, lsc)

	const reason = "Unable to remove a finalizer"
	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, FailedStatusPhase, reason); err != nil {
		t.Fatalf("updateLocalStorageClassPhase: %v", err)
	}

	got := readLSC(t, cl)
	if slices.Contains(got.Finalizers, LocalStorageClassFinalizerName) {
		t.Errorf("the finalizer was put back on a terminating object: %v", got.Finalizers)
	}

	ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %+v", ready)
	}
	if ready.Message != reason {
		t.Errorf("message = %q, want %q", ready.Message, reason)
	}
}

// The failure paths pass err.Error() through as the condition message, and the
// schema caps it at 32768. Over the cap the API server rejects the whole status
// write, so the resource keeps reporting its previous verdict and the reconcile
// fails on the write instead of on what actually went wrong.
func TestUpdateLocalStorageClassPhase_TruncatesAnOversizedMessage(t *testing.T) {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
	}
	cl := newStatusTestClient(t, lsc)

	// Multi-byte on purpose. The schema's maxLength is an OpenAPI string
	// length, counted in runes, and TruncateMessage counts the same way — a
	// byte-counting assertion here would fail on a message that is in fact
	// within the limit.
	//
	// Written as an escape rather than the character itself: the module linter
	// rejects non-ASCII bytes in Go sources.
	huge := strings.Repeat("\u044f", conditions.MaxMessageLen+100)
	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, FailedStatusPhase, huge); err != nil {
		t.Fatalf("updateLocalStorageClassPhase: %v", err)
	}

	ready := conditions.Get(readLSC(t, cl).Status.Conditions, slv.ConditionTypeReady)
	if ready == nil {
		t.Fatal("Ready condition was not published")
	}
	// Counted in runes, not bytes: the schema's maxLength is an OpenAPI string
	// length, which the API server validates with utf8.RuneCountInString, and
	// TruncateMessage counts the same way. Measuring len() would fail this test
	// on any non-ASCII message that is in fact within the limit.
	if utf8.RuneCountInString(ready.Message) > conditions.MaxMessageLen {
		t.Errorf("message is %d runes, over the %d the schema allows",
			utf8.RuneCountInString(ready.Message), conditions.MaxMessageLen)
	}

	// The caller's copy is mirrored from the same condition, so it must not
	// carry the untruncated message either.
	mirrored := conditions.Get(lsc.Status.Conditions, slv.ConditionTypeReady)
	if mirrored == nil || utf8.RuneCountInString(mirrored.Message) > conditions.MaxMessageLen {
		t.Errorf("the mirrored condition was not truncated: %+v", mirrored)
	}
}

// A LocalStorageClass whose teardown is blocked used to keep advertising
// Ready=True from its last successful pass, which is precisely the state an
// alert on "Ready=False for longer than N minutes" is meant to catch.
func TestSetLocalStorageClassDeleting(t *testing.T) {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 2},
	}
	cl := newStatusTestClient(t, lsc)

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, CreatedStatusPhase, ""); err != nil {
		t.Fatalf("seeding the Created phase: %v", err)
	}

	if err := setLocalStorageClassDeleting(context.Background(), cl, lsc); err != nil {
		t.Fatalf("setLocalStorageClassDeleting: %v", err)
	}

	got := readLSC(t, cl)
	ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %+v", ready)
	}
	if ready.Reason != conditions.ReasonDeleting {
		t.Errorf("reason = %q, want %q", ready.Reason, conditions.ReasonDeleting)
	}

	// The phase vocabulary is Created/Failed and has no word for a teardown, so
	// the deletion verdict must not overwrite what the last reconcile settled on.
	if got.Status.Phase != CreatedStatusPhase {
		t.Errorf("phase = %q, want it left at %q", got.Status.Phase, CreatedStatusPhase)
	}
}

// shouldReconcileByUpdateFunc used to gate the "retry a resource whose last pass
// did not succeed" branch on status.phase == Failed. It now gates on the Ready
// condition, which covers two states the phase could not name: an object the
// controller has never written a status for, and one carried over from a release
// that predates the conditions — the latter is every object in every cluster
// being upgraded, and it is what backfills their conditions.
func TestShouldReconcileByUpdateFunc_GatesOnTheReadyCondition(t *testing.T) {
	lvgs := []slv.LocalStorageClassLVG{{Name: "lvg-1"}}

	base := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1},
		Spec: slv.LocalStorageClassSpec{
			ReclaimPolicy:     "Delete",
			VolumeBindingMode: "WaitForFirstConsumer",
			LVM: &slv.LocalStorageClassLVMSpec{
				Type:            LVMThickType,
				LVMVolumeGroups: lvgs,
			},
		},
	}

	// Built from the LocalStorageClass itself, so hasSCDiff reports no
	// difference and the Ready condition is the only thing left to decide on.
	sc, err := configureStorageClass(base, lvgs, nil)
	if err != nil {
		t.Fatalf("configureStorageClass: %v", err)
	}
	scList := &storagev1.StorageClassList{Items: []storagev1.StorageClass{*sc}}

	withReady := func(status metav1.ConditionStatus) *slv.LocalStorageClassStatus {
		return &slv.LocalStorageClassStatus{
			Phase: CreatedStatusPhase,
			Conditions: []metav1.Condition{{
				Type:               slv.ConditionTypeReady,
				Status:             status,
				Reason:             conditions.ReasonReconciled,
				ObservedGeneration: 1,
			}},
		}
	}

	for _, tc := range []struct {
		name string
		// generation defaults to the 1 the conditions above were recorded for.
		generation int64
		status     *slv.LocalStorageClassStatus
		want       bool
	}{
		{name: "never reconciled", status: nil, want: true},
		{name: "upgraded from a release without conditions", status: &slv.LocalStorageClassStatus{Phase: CreatedStatusPhase}, want: true},
		{name: "last pass failed", status: withReady(metav1.ConditionFalse), want: true},
		{name: "not yet observed", status: withReady(metav1.ConditionUnknown), want: true},
		{name: "healthy", status: withReady(metav1.ConditionTrue), want: false},
		// The edit that gets here is one hasSCDiff cannot see — reordering
		// lvmVolumeGroups, or an equivalent rewrite of a labelSelector. Ready is
		// True, so gating on the status alone would answer "nothing to do" and
		// leave status.observedGeneration behind metadata.generation forever.
		{name: "ready, but recorded for an older generation", generation: 2, status: withReady(metav1.ConditionTrue), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lsc := base.DeepCopy()
			lsc.Status = tc.status
			if tc.generation != 0 {
				lsc.Generation = tc.generation
			}

			got, err := shouldReconcileByUpdateFunc(scList, lsc, lvgs, nil)
			if err != nil {
				t.Fatalf("shouldReconcileByUpdateFunc: %v", err)
			}
			if got != tc.want {
				t.Errorf("shouldReconcileByUpdateFunc = %v, want %v", got, tc.want)
			}
		})
	}
}

// testLogger writes into t.Log, so the output shows up only for a failing test.
func testLogger(t *testing.T) logger.Logger {
	t.Helper()
	log, err := logger.NewLoggerToWriter(testWriter{t}, logger.TraceLevel)
	if err != nil {
		t.Fatalf("building the logger: %v", err)
	}
	return log
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// The Deleting condition is published at the end of the delete pass, not at the
// start, and the ordering is the whole point rather than a detail. A status
// write bumps resourceVersion while the caller's copy and the informer cache
// still hold the old one, so publishing first leaves the finalizer Update below
// landing on a stale revision — a 409 that the retry cannot clear, because the
// re-read is served from the same cache. The deletion stalls and the resource
// is marked Failed for a teardown that was going fine.
//
// A foreign finalizer stands in for the case the condition exists for: the
// resource survives our removal, stays Terminating, and has to stop advertising
// the Ready=True its last successful pass left behind.
func TestReconcileLSCDeleteFunc_PublishesDeletingAfterRemovingTheFinalizer(t *testing.T) {
	const foreignFinalizer = "example.com/keep-me"

	deletedAt := metav1.Now()
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testLSCName,
			Generation:        2,
			DeletionTimestamp: &deletedAt,
			Finalizers:        []string{LocalStorageClassFinalizerName, foreignFinalizer},
		},
		Status: &slv.LocalStorageClassStatus{
			Phase: CreatedStatusPhase,
			Conditions: []metav1.Condition{{
				Type:               slv.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             conditions.ReasonReconciled,
				Message:            "the LocalStorageClass is in the Created phase",
				ObservedGeneration: 1,
			}},
		},
	}
	var writes []string
	cl := newStatusTestClientWithHooks(t, recordWrites(&writes), lsc)

	requeue, err := reconcileLSCDeleteFunc(context.Background(), cl, testLogger(t), &storagev1.StorageClassList{}, lsc)
	if err != nil {
		t.Fatalf("reconcileLSCDeleteFunc: %v", err)
	}
	if requeue {
		t.Error("a completed delete pass should not ask for a requeue")
	}

	// The finalizer removal first, the condition after it. The reverse order is
	// what stalls the deletion on a real cluster.
	if !slices.Equal(writes, []string{"update", "status"}) {
		t.Errorf("writes = %v, want the finalizer update before the status write", writes)
	}

	got := readLSC(t, cl)
	if slices.Contains(got.Finalizers, LocalStorageClassFinalizerName) {
		t.Errorf("our finalizer survived the delete pass: %v", got.Finalizers)
	}
	if !slices.Contains(got.Finalizers, foreignFinalizer) {
		t.Errorf("the foreign finalizer was dropped: %v", got.Finalizers)
	}

	ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False on a resource still terminating, got %+v", ready)
	}
	if ready.Reason != conditions.ReasonDeleting {
		t.Errorf("reason = %q, want %q", ready.Reason, conditions.ReasonDeleting)
	}

	// The phase has no word for a teardown, so it must be left where the last
	// reconcile put it rather than being overwritten with something invented.
	if got.Status.Phase != CreatedStatusPhase {
		t.Errorf("phase = %q, want it left at %q", got.Status.Phase, CreatedStatusPhase)
	}
}

// Nothing migrates the historical finalizer name to the current one: the add
// path tests for the current name only, so a resource created by a module
// version that used the old name ends up carrying both. Removing the first
// match and stopping would leave the other attached and still report success,
// and the resource would sit in Terminating forever with no event source left
// to revisit it.
func TestRemoveControllerFinalizers_RemovesBothSpellings(t *testing.T) {
	const foreignFinalizer = "example.com/keep-me"

	deletedAt := metav1.Now()
	for _, tc := range []struct {
		name       string
		finalizers []string
		wantKept   []string
	}{
		{"current only", []string{LocalStorageClassFinalizerName, foreignFinalizer}, []string{foreignFinalizer}},
		{"historical only", []string{LocalStorageClassFinalizerNameOld, foreignFinalizer}, []string{foreignFinalizer}},
		{"both, historical first", []string{LocalStorageClassFinalizerNameOld, LocalStorageClassFinalizerName, foreignFinalizer}, []string{foreignFinalizer}},
		{"both, current first", []string{LocalStorageClassFinalizerName, LocalStorageClassFinalizerNameOld, foreignFinalizer}, []string{foreignFinalizer}},
		{"neither", []string{foreignFinalizer}, []string{foreignFinalizer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lsc := &slv.LocalStorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:              testLSCName,
					DeletionTimestamp: &deletedAt,
					Finalizers:        tc.finalizers,
				},
			}
			cl := newStatusTestClient(t, lsc)

			removed, err := removeControllerFinalizers(context.Background(), cl, lsc)
			if err != nil {
				t.Fatalf("removeControllerFinalizers: %v", err)
			}

			wantRemoved := len(tc.finalizers) != len(tc.wantKept)
			if removed != wantRemoved {
				t.Errorf("removed = %v, want %v", removed, wantRemoved)
			}

			got := readLSC(t, cl)
			if !slices.Equal(got.Finalizers, tc.wantKept) {
				t.Errorf("finalizers on the server = %v, want %v", got.Finalizers, tc.wantKept)
			}
			// The caller's copy is what the delete path keeps working with.
			if !slices.Equal(lsc.Finalizers, tc.wantKept) {
				t.Errorf("finalizers on the caller's object = %v, want %v", lsc.Finalizers, tc.wantKept)
			}
		})
	}
}
