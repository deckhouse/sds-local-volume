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
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/deckhouse/sds-common-lib/conditions"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
)

// drainEvents returns what the fake recorder has received so far.
func drainEvents(rec *record.FakeRecorder) []string {
	var got []string
	for {
		select {
		case e := <-rec.Events:
			got = append(got, e)
		default:
			return got
		}
	}
}

// The status writer records an Event when Ready moves and only then: a
// periodic resync that repeats the verdict, or re-words it, must not add to the
// resource's Events, or the one that tells when it broke scrolls away. The
// recorder is the core/v1 one the manager hands out, adapted the way the
// reconciler adapts it.
func TestStatusWriterRecordsReadyTransitions(t *testing.T) {
	lsc := &slv.LocalStorageClass{ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1}}
	cl := newStatusTestClient(t, lsc)
	rec := record.NewFakeRecorder(10)
	ctx := withEventRecorder(context.Background(), conditions.FromLegacyRecorder(rec))

	const created = "Normal Reconciled the LocalStorageClass is in the Created phase"
	steps := []struct {
		name  string
		write func() error
		want  []string
	}{
		{"first verdict", func() error {
			return updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
		}, []string{created}},
		{"same verdict", func() error {
			return updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
		}, nil},
		{"breaks", func() error {
			return updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, "unable to create the StorageClass")
		}, []string{"Warning ReconcileFailed unable to create the StorageClass"}},
		{"fails with another message", func() error {
			return updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, "unable to create the StorageClass: timeout")
		}, nil},
		{"recovers", func() error {
			return updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
		}, []string{created}},
		{"stays Ready under another reason", func() error {
			return setLocalStorageClassCreated(ctx, cl, lsc, "lvg-1 (node-1)")
		}, []string{"Normal " + slv.ConditionReasonLVMVolumeGroupsOutsideCSINodes + " lvg-1 (node-1)"}},
		{"is deleted", func() error {
			return setLocalStorageClassDeleting(ctx, cl, lsc)
		}, []string{"Normal " + conditions.ReasonDeleting + " the LocalStorageClass is being deleted"}},
		{"is still being deleted", func() error {
			return setLocalStorageClassDeleting(ctx, cl, lsc)
		}, nil},
		{"fails to finish the deletion", func() error {
			return updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, "Unable to remove a finalizer")
		}, []string{"Warning ReconcileFailed Unable to remove a finalizer"}},
	}

	for _, s := range steps {
		if err := s.write(); err != nil {
			t.Fatalf("%s: writing the status: %v", s.name, err)
		}
		if got := drainEvents(rec); !slices.Equal(got, s.want) {
			t.Errorf("%s: Events = %q, want %q", s.name, got, s.want)
		}
	}
}

func TestSetLocalStorageClassDeletingWithoutARecorder(t *testing.T) {
	lsc := &slv.LocalStorageClass{ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Generation: 1}}
	cl := newStatusTestClient(t, lsc)

	if err := setLocalStorageClassDeleting(context.Background(), cl, lsc); err != nil {
		t.Fatalf("setLocalStorageClassDeleting: %v", err)
	}
	if !conditions.IsFalse(readLSC(t, cl).Status.Conditions, slv.ConditionTypeReady) {
		t.Error("Ready was not written as False")
	}
}

// Without a recorder on the context the writer records nothing and still
// writes, so the tests and any caller outside the reconciler need no recorder.
func TestStatusWriterWithoutARecorder(t *testing.T) {
	lsc := &slv.LocalStorageClass{ObjectMeta: metav1.ObjectMeta{Name: testLSCName}}
	cl := newStatusTestClient(t, lsc)

	if err := updateLocalStorageClassPhase(context.Background(), cl, lsc, FailedStatusPhase, "broken"); err != nil {
		t.Fatalf("updateLocalStorageClassPhase: %v", err)
	}
	if !conditions.IsFalse(readLSC(t, cl).Status.Conditions, slv.ConditionTypeReady) {
		t.Error("Ready was not written as False")
	}
}
