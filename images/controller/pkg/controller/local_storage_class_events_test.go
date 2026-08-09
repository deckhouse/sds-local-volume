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
	"slices"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
)

func newTestQueue(t *testing.T) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	t.Helper()
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	t.Cleanup(q.ShutDown)
	return q
}

func drain(t *testing.T, q workqueue.TypedRateLimitingInterface[reconcile.Request]) []string {
	t.Helper()
	names := make([]string, 0, q.Len())
	for q.Len() > 0 {
		req, shutdown := q.Get()
		if shutdown {
			break
		}
		names = append(names, req.Name)
		q.Done(req)
	}
	return names
}

// The reconciler is the only thing that calls ForgetLocalStorageClass, and it
// reaches it through the "the object is gone" branch — which needs a request
// for a name the API server no longer knows. Without a delete event no such
// request is ever produced, and the gauges keyed by that name keep exporting
// whatever the final pass published: for a teardown that succeeded, ready=0,
// which is the value an alert is written against.
func TestLocalStorageClassEventHandler_DeleteEnqueues(t *testing.T) {
	q := newTestQueue(t)

	localStorageClassEventHandler(testLogger(t)).Delete(
		context.Background(),
		event.TypedDeleteEvent[*slv.LocalStorageClass]{
			Object: &slv.LocalStorageClass{ObjectMeta: metav1.ObjectMeta{Name: testLSCName}},
		},
		q,
	)

	if got := drain(t, q); len(got) != 1 || got[0] != testLSCName {
		t.Errorf("enqueued %v, want [%s]", got, testLSCName)
	}
}

// The delete event is the one added here; the other two are the reason the
// handler is worth pinning as a whole, since a refactor that loses any of them
// stops the controller silently.
func TestLocalStorageClassEventHandler_CreateAndUpdate(t *testing.T) {
	h := localStorageClassEventHandler(testLogger(t))

	base := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Labels: map[string]string{"a": "b"}},
		Spec: slv.LocalStorageClassSpec{
			LVM: &slv.LocalStorageClassLVMSpec{
				Type:            LVMThickType,
				LVMVolumeGroups: []slv.LocalStorageClassLVG{{Name: "lvg-1"}},
			},
		},
	}

	t.Run("create", func(t *testing.T) {
		q := newTestQueue(t)
		h.Create(context.Background(), event.TypedCreateEvent[*slv.LocalStorageClass]{Object: base}, q)
		if got := drain(t, q); len(got) != 1 {
			t.Errorf("enqueued %v, want one request", got)
		}
	})

	t.Run("update touching neither spec nor labels", func(t *testing.T) {
		q := newTestQueue(t)
		// A status write is what this looks like in practice, and it must not
		// feed itself back into the queue.
		newer := base.DeepCopy()
		newer.Status = &slv.LocalStorageClassStatus{Phase: CreatedStatusPhase}

		h.Update(context.Background(), event.TypedUpdateEvent[*slv.LocalStorageClass]{ObjectOld: base, ObjectNew: newer}, q)
		if got := drain(t, q); len(got) != 0 {
			t.Errorf("enqueued %v, want nothing", got)
		}
	})

	t.Run("update setting a deletionTimestamp", func(t *testing.T) {
		q := newTestQueue(t)
		deletedAt := metav1.Now()
		newer := base.DeepCopy()
		newer.DeletionTimestamp = &deletedAt

		h.Update(context.Background(), event.TypedUpdateEvent[*slv.LocalStorageClass]{ObjectOld: base, ObjectNew: newer}, q)
		if got := drain(t, q); len(got) != 1 {
			t.Errorf("enqueued %v, want one request", got)
		}
	})

	t.Run("update changing the spec", func(t *testing.T) {
		q := newTestQueue(t)
		newer := base.DeepCopy()
		newer.Spec.LVM.LVMVolumeGroups = append(newer.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: "lvg-2"})

		h.Update(context.Background(), event.TypedUpdateEvent[*slv.LocalStorageClass]{ObjectOld: base, ObjectNew: newer}, q)
		if got := drain(t, q); len(got) != 1 {
			t.Errorf("enqueued %v, want one request", got)
		}
	})
}

// The StorageClass this runs against was just built by configureStorageClass,
// which puts the finalizer in its ObjectMeta, so the write it used to issue
// unconditionally never had anything to add.
func TestAddFinalizerIfNotExistsForSC(t *testing.T) {
	scheme := apiruntime.NewScheme()
	if err := storagev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding the storage scheme: %v", err)
	}

	for _, tc := range []struct {
		name       string
		finalizers []string
		wantAdded  bool
		wantWrites int
	}{
		{"already carries the finalizer", []string{LocalStorageClassFinalizerName}, false, 0},
		{"carries somebody else's only", []string{"example.com/keep-me"}, true, 1},
		{"carries none", nil, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := &storagev1.StorageClass{
				ObjectMeta:  metav1.ObjectMeta{Name: testLSCName, Finalizers: tc.finalizers},
				Provisioner: LocalStorageClassProvisioner,
			}

			writes := 0
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(sc.DeepCopy()).
				WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						writes++
						return c.Update(ctx, obj, opts...)
					},
				}).
				Build()

			// Re-read so the object carries the resourceVersion the tracker
			// assigned it, the way the create path hands it over.
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(sc), sc); err != nil {
				t.Fatalf("seeding: %v", err)
			}

			added, err := addFinalizerIfNotExistsForSC(context.Background(), cl, sc)
			if err != nil {
				t.Fatalf("addFinalizerIfNotExistsForSC: %v", err)
			}
			if added != tc.wantAdded {
				t.Errorf("added = %v, want %v", added, tc.wantAdded)
			}
			if writes != tc.wantWrites {
				t.Errorf("issued %d updates, want %d", writes, tc.wantWrites)
			}
			if !slices.Contains(sc.Finalizers, LocalStorageClassFinalizerName) {
				t.Errorf("the finalizer is missing afterwards: %v", sc.Finalizers)
			}
		})
	}
}
