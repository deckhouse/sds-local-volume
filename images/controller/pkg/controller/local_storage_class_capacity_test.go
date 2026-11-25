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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/deckhouse/sds-common-lib/logger"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const gi = int64(1) << 30

func lvgWithPool(name, pool string, size, used, allocated, available int64) *snc.LVMVolumeGroup {
	return &snc.LVMVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: snc.LVMVolumeGroupStatus{ThinPools: []snc.LVMVolumeGroupThinPoolStatus{{
			Name:           pool,
			ActualSize:     *resource.NewQuantity(size, resource.BinarySI),
			UsedSize:       *resource.NewQuantity(used, resource.BinarySI),
			AllocatedSize:  *resource.NewQuantity(allocated, resource.BinarySI),
			AvailableSpace: *resource.NewQuantity(available, resource.BinarySI),
		}}},
	}
}

func TestComputeCapacitySumsThinPools(t *testing.T) {
	lsc := newTestLSC("thin", LVMThinType, thinEntry("lvg-1", "pool"), thinEntry("lvg-2", "pool"))
	lvgs := &snc.LVMVolumeGroupList{Items: []snc.LVMVolumeGroup{
		*lvgWithPool("lvg-1", "pool", 100*gi, 30*gi, 120*gi, 30*gi),
		*lvgWithPool("lvg-2", "pool", 50*gi, 10*gi, 90*gi, -15*gi),
	}}

	got, err := computeLocalStorageClassCapacity(lsc, lvgs)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, 150*gi, got.Size.Value())
	assert.Equal(t, 40*gi, got.Used.Value())
	assert.Equal(t, 110*gi, got.FreeOnDisk.Value())
	assert.Equal(t, 210*gi, got.Allocated.Value())
	assert.Equal(t, 30*gi, got.FreeByAllocationLimit.Value(), "a pool past its limit counts as zero")
	assert.Zero(t, got.PoolsWithoutData)
	require.Len(t, got.Pools, 2)
	assert.Equal(t, "lvg-1", got.Pools[0].LVMVolumeGroup)
	assert.Equal(t, 70*gi, got.Pools[0].FreeOnDisk.Value())
	assert.Equal(t, int64(0), got.Pools[1].FreeByAllocationLimit.Value())
}

func TestComputeCapacityCountsPoolsWithoutData(t *testing.T) {
	lsc := newTestLSC("thin", LVMThinType, thinEntry("lvg-1", "pool"), thinEntry("lvg-missing", "pool"), thinEntry("lvg-2", "other"))
	lvgs := &snc.LVMVolumeGroupList{Items: []snc.LVMVolumeGroup{
		*lvgWithPool("lvg-1", "pool", 100*gi, 30*gi, 0, 150*gi),
		*lvgWithPool("lvg-2", "unrelated", 50*gi, 10*gi, 0, 75*gi),
	}}

	got, err := computeLocalStorageClassCapacity(lsc, lvgs)
	require.NoError(t, err)
	assert.Equal(t, int32(2), got.PoolsWithoutData)
	assert.Equal(t, 100*gi, got.Size.Value())
	require.Len(t, got.Pools, 1)
}

func TestComputeCapacityThickHasNone(t *testing.T) {
	lsc := newTestLSC("thick", LVMThickType, slv.LocalStorageClassLVG{Name: "vg"})
	got, err := computeLocalStorageClassCapacity(lsc, &snc.LVMVolumeGroupList{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestComputeCapacityResolvesLabelSelector(t *testing.T) {
	lsc := newTestLSC("thin", LVMThinType, slv.LocalStorageClassLVG{
		LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"zone": "a"}},
		Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "pool"},
	})
	in := lvgWithPool("lvg-in", "pool", 10*gi, gi, 0, 15*gi)
	in.Labels = map[string]string{"zone": "a"}
	out := lvgWithPool("lvg-out", "pool", 10*gi, gi, 0, 15*gi)

	got, err := computeLocalStorageClassCapacity(lsc, &snc.LVMVolumeGroupList{Items: []snc.LVMVolumeGroup{*in, *out}})
	require.NoError(t, err)
	assert.Equal(t, 10*gi, got.Size.Value())
}

func capacityOf(size, used, free, allocated, limitFree int64, at time.Time) *slv.LocalStorageClassCapacity {
	q := func(v int64) resource.Quantity { return *resource.NewQuantity(v, resource.BinarySI) }
	return &slv.LocalStorageClassCapacity{
		Size: q(size), Used: q(used), FreeOnDisk: q(free),
		Allocated: resource.NewQuantity(allocated, resource.BinarySI), FreeByAllocationLimit: resource.NewQuantity(limitFree, resource.BinarySI),
		Pools: []slv.LocalStorageClassPoolCapacity{{
			LVMVolumeGroup: "lvg", ThinPool: "pool", Size: q(size), Used: q(used), FreeOnDisk: q(free), FreeByAllocationLimit: q(limitFree),
		}},
		UpdatedAt: metav1.NewTime(at),
	}
}

func TestCapacityNeedsWrite(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	prev := capacityOf(1000*gi, 300*gi, 700*gi, 500*gi, 1000*gi, now.Add(-time.Minute))

	t.Run("first write", func(t *testing.T) {
		assert.True(t, capacityNeedsWrite(nil, prev, now))
	})
	t.Run("unchanged", func(t *testing.T) {
		assert.False(t, capacityNeedsWrite(prev, prev.DeepCopy(), now))
	})
	t.Run("a move under 1 percent of the size", func(t *testing.T) {
		next := capacityOf(1000*gi, 305*gi, 695*gi, 505*gi, 1000*gi, now)
		assert.False(t, capacityNeedsWrite(prev, next, now))
	})
	t.Run("a move over 1 percent of the size", func(t *testing.T) {
		next := capacityOf(1000*gi, 311*gi, 689*gi, 500*gi, 990*gi, now)
		assert.True(t, capacityNeedsWrite(prev, next, now))
	})
	t.Run("size changed", func(t *testing.T) {
		next := capacityOf(1020*gi, 300*gi, 720*gi, 500*gi, 1000*gi, now)
		assert.True(t, capacityNeedsWrite(prev, next, now))
	})
	t.Run("a pool without data appeared", func(t *testing.T) {
		next := prev.DeepCopy()
		next.PoolsWithoutData = 1
		assert.True(t, capacityNeedsWrite(prev, next, now))
	})
	t.Run("a pool is gone", func(t *testing.T) {
		next := prev.DeepCopy()
		next.Pools = nil
		assert.True(t, capacityNeedsWrite(prev, next, now))
	})
	t.Run("refresh after 5 minutes", func(t *testing.T) {
		assert.False(t, capacityNeedsWrite(prev, prev.DeepCopy(), now.Add(3*time.Minute)))
		assert.True(t, capacityNeedsWrite(prev, prev.DeepCopy(), now.Add(4*time.Minute)))
	})
	t.Run("no capacity any more", func(t *testing.T) {
		assert.True(t, capacityNeedsWrite(prev, nil, now))
		assert.False(t, capacityNeedsWrite(nil, nil, now))
	})
}

func newCapacityClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, slv.AddToScheme(s))
	require.NoError(t, snc.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&slv.LocalStorageClass{}).Build()
}

func TestSyncCapacitiesWritesOnlyWhenNeeded(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	lsc := newTestLSC("thin", LVMThinType, thinEntry("lvg-1", "pool"))
	lsc.Status = &slv.LocalStorageClassStatus{Phase: "Created", Reason: "ok"}
	thick := newTestLSC("thick", LVMThickType, slv.LocalStorageClassLVG{Name: "vg"})
	cl := newCapacityClient(t, lsc, thick, lvgWithPool("lvg-1", "pool", 100*gi, 30*gi, 120*gi, 30*gi))
	log, err := logger.NewLogger(logger.InfoLevel)
	require.NoError(t, err)

	require.NoError(t, syncLocalStorageClassCapacities(ctx, cl, log, now))

	got := &slv.LocalStorageClass{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "thin"}, got))
	require.NotNil(t, got.Status.Capacity)
	assert.Equal(t, 70*gi, got.Status.Capacity.FreeOnDisk.Value())
	assert.Equal(t, "Created", got.Status.Phase, "the rest of the status is kept")
	assert.True(t, got.Status.Capacity.UpdatedAt.Time.Equal(now))

	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "thick"}, got))
	assert.Nil(t, got.Status, "a Thick class gets no status")

	require.NoError(t, syncLocalStorageClassCapacities(ctx, cl, log, now.Add(time.Minute)))
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "thin"}, got))
	assert.True(t, got.Status.Capacity.UpdatedAt.Time.Equal(now), "no write for an unchanged figure")

	lvg := &snc.LVMVolumeGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "lvg-1"}, lvg))
	lvg.Status.ThinPools[0].UsedSize = *resource.NewQuantity(40*gi, resource.BinarySI)
	require.NoError(t, cl.Update(ctx, lvg))
	require.NoError(t, syncLocalStorageClassCapacities(ctx, cl, log, now.Add(2*time.Minute)))
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "thin"}, got))
	assert.Equal(t, 60*gi, got.Status.Capacity.FreeOnDisk.Value(), "a move over 1 percent is written at once")
}
