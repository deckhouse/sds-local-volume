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

package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const deleteWaitVolume = "pvc-delete-wait"

func deleteWaitLLV(phase, reason string, finalizers ...string) *v1alpha1.LVMLogicalVolume {
	llv := &v1alpha1.LVMLogicalVolume{ObjectMeta: metav1.ObjectMeta{Name: deleteWaitVolume, Finalizers: finalizers}}
	if phase != "" || reason != "" {
		llv.Status = &v1alpha1.LVMLogicalVolumeStatus{Phase: phase, Reason: reason}
	}
	return llv
}

func TestDeleteVolumeWaitsForTheLVMLogicalVolume(t *testing.T) {
	ctx := context.Background()
	req := &csi.DeleteVolumeRequest{VolumeId: deleteWaitVolume}

	t.Run("missing volume is deleted", func(t *testing.T) {
		d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t)}
		_, err := d.DeleteVolume(ctx, req)
		require.NoError(t, err)
	})

	t.Run("volume without agent finalizer is gone at once", func(t *testing.T) {
		d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t, deleteWaitLLV("Created", "", "storage.deckhouse.io/sds-local-volume-csi"))}
		_, err := d.DeleteVolume(ctx, req)
		require.NoError(t, err)
		err = d.cl.Get(ctx, client.ObjectKey{Name: deleteWaitVolume}, &v1alpha1.LVMLogicalVolume{})
		assert.True(t, kerrors.IsNotFound(err))
	})

	cases := []struct {
		name       string
		llv        *v1alpha1.LVMLogicalVolume
		contains   []string
		notContain []string
	}{
		{"failed", deleteWaitLLV("Failed", "Logical volume vg/lv in use", nodeConfiguratorFinalizer),
			[]string{"phase=Failed", "in use"}, []string{"blocked by finalizers"}},
		{"cleaning", deleteWaitLLV("Cleaning", "Cleaning up volume lv", nodeConfiguratorFinalizer),
			[]string{"volume cleanup in progress", "Cleaning up volume lv"}, nil},
		{"empty status", deleteWaitLLV("", "", nodeConfiguratorFinalizer),
			[]string{"waiting for the sds-node-configurator agent"}, nil},
		{"foreign finalizer", deleteWaitLLV("Created", "", nodeConfiguratorFinalizer, "example.com/keep"),
			[]string{"blocked by finalizers [example.com/keep]"}, nil},
	}
	for _, tc := range cases {
		t.Run("aborted while "+tc.name, func(t *testing.T) {
			d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t, tc.llv)}
			_, err := d.DeleteVolume(ctx, req)
			require.Equal(t, codes.Aborted, status.Code(err))
			for _, s := range tc.contains {
				assert.Contains(t, err.Error(), s)
			}
			for _, s := range tc.notContain {
				assert.NotContains(t, err.Error(), s)
			}
		})
	}

	t.Run("repeated calls keep the cleanup mode and finish after the agent", func(t *testing.T) {
		llv := deleteWaitLLV("Cleaning", "wiping", nodeConfiguratorFinalizer)
		mode := "RandomFillSinglePass"
		llv.Spec.VolumeCleanup = &mode
		d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t, llv)}
		require.NoError(t, d.cl.Delete(ctx, llv.DeepCopy()))
		for i := 0; i < 2; i++ {
			_, err := d.DeleteVolume(ctx, req)
			require.Equal(t, codes.Aborted, status.Code(err))
		}
		var got v1alpha1.LVMLogicalVolume
		require.NoError(t, d.cl.Get(ctx, client.ObjectKey{Name: deleteWaitVolume}, &got))
		require.NotNil(t, got.Spec.VolumeCleanup)
		assert.Equal(t, mode, *got.Spec.VolumeCleanup)
		assert.NotNil(t, got.DeletionTimestamp)

		got.Finalizers = nil
		require.NoError(t, d.cl.Update(ctx, &got))
		_, err := d.DeleteVolume(ctx, req)
		require.NoError(t, err)
	})

	apiErrors := []struct {
		name string
		err  error
		code codes.Code
	}{
		{"server timeout", kerrors.NewServerTimeout(schema.GroupResource{}, "get", 1), codes.Unavailable},
		{"service unavailable", kerrors.NewServiceUnavailable("down"), codes.Unavailable},
		{"too many requests", kerrors.NewTooManyRequests("slow down", 1), codes.Unavailable},
		{"context deadline", context.DeadlineExceeded, codes.Unavailable},
		{"forbidden", kerrors.NewForbidden(schema.GroupResource{}, "x", errors.New("no")), codes.Internal},
		{"other", errors.New("boom"), codes.Internal},
	}
	for _, tc := range apiErrors {
		t.Run("get error "+tc.name, func(t *testing.T) {
			cl := newCleanupTestClient(t, deleteWaitLLV("Created", "", nodeConfiguratorFinalizer))
			d := &Driver{log: logger.NewNop(), cl: failingGet(t, cl, tc.err)}
			_, err := d.DeleteVolume(ctx, req)
			assert.Equal(t, tc.code, status.Code(err))
		})
	}

	t.Run("delete error is a status", func(t *testing.T) {
		cl := interceptor.NewClient(newCleanupTestClient(t, deleteWaitLLV("Created", "", nodeConfiguratorFinalizer)), interceptor.Funcs{
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return kerrors.NewServerTimeout(schema.GroupResource{}, "delete", 1)
			},
		})
		d := &Driver{log: logger.NewNop(), cl: cl}
		_, err := d.DeleteVolume(ctx, req)
		assert.Equal(t, codes.Unavailable, status.Code(err))
	})

	t.Run("delete race with the agent is a success", func(t *testing.T) {
		cl := interceptor.NewClient(newCleanupTestClient(t, deleteWaitLLV("Created", "", nodeConfiguratorFinalizer)), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				obj.SetFinalizers(nil)
				if err := c.Update(ctx, obj); err != nil {
					return err
				}
				if err := c.Delete(ctx, obj, opts...); err != nil {
					return err
				}
				return kerrors.NewNotFound(schema.GroupResource{}, deleteWaitVolume)
			},
		})
		d := &Driver{log: logger.NewNop(), cl: cl}
		_, err := d.DeleteVolume(ctx, req)
		assert.NoError(t, err)
	})
}

func failingGet(t *testing.T, cl client.WithWatch, err error) client.WithWatch {
	t.Helper()
	return interceptor.NewClient(cl, interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return err
		},
	})
}

func TestDeletionPendingMessage(t *testing.T) {
	long := strings.Repeat("é", maxPendingReasonLen)
	msg := deletionPendingMessage("LVMLogicalVolume", "v", "Failed", long, nil)
	assert.LessOrEqual(t, len(msg), maxPendingReasonLen+200)
	assert.Contains(t, msg, "...")

	msg = deletionPendingMessage("LVMLogicalVolume", "v", "Failed", "", []string{"storage.deckhouse.io/sds-local-volume-csi"})
	assert.NotContains(t, msg, "blocked")
}
