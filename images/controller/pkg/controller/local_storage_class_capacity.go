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
	"fmt"
	"math"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	sdscapacity "github.com/deckhouse/sds-common-lib/capacity"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	LocalStorageClassCapacityCtrlName = "local-storage-class-capacity"

	capacityCheckInterval = 30 * time.Second
	capacityRefreshPeriod = 5 * time.Minute
	capacityChangeShare   = 0.01
)

func computeLocalStorageClassCapacity(lsc *slv.LocalStorageClass, lvgList *snc.LVMVolumeGroupList) (*slv.LocalStorageClassCapacity, error) {
	if lsc.Spec.LVM == nil || lsc.Spec.LVM.Type != LVMThinType {
		return nil, nil
	}

	resolved, err := resolveEffectiveLVGs(lsc, lvgList)
	if err != nil {
		return nil, err
	}

	lvgs := make(map[string]*snc.LVMVolumeGroup, len(lvgList.Items))
	for i := range lvgList.Items {
		lvgs[lvgList.Items[i].Name] = &lvgList.Items[i]
	}

	var size, used, free, allocated, limitFree int64
	capacity := &slv.LocalStorageClassCapacity{}
	for _, entry := range resolved {
		lvg, ok := lvgs[entry.Name]
		if !ok {
			capacity.PoolsWithoutData++
			continue
		}
		poolName := thinPoolName(entry.Thin)
		var pool *snc.LVMVolumeGroupThinPoolStatus
		for i := range lvg.Status.ThinPools {
			if lvg.Status.ThinPools[i].Name == poolName {
				pool = &lvg.Status.ThinPools[i]
				break
			}
		}
		if pool == nil {
			capacity.PoolsWithoutData++
			continue
		}

		poolSize := pool.ActualSize.Value()
		poolUsed := pool.UsedSize.Value()
		poolFree := max(poolSize-poolUsed, 0)
		poolLimitFree := max(pool.AvailableSpace.Value(), 0)

		size += poolSize
		used += poolUsed
		free += poolFree
		allocated += pool.AllocatedSize.Value()
		limitFree += poolLimitFree

		capacity.Pools = append(capacity.Pools, slv.LocalStorageClassPoolCapacity{
			LVMVolumeGroup:        lvg.Name,
			ThinPool:              poolName,
			Size:                  *resource.NewQuantity(poolSize, resource.BinarySI),
			Used:                  *resource.NewQuantity(poolUsed, resource.BinarySI),
			FreeOnDisk:            *resource.NewQuantity(poolFree, resource.BinarySI),
			FreeByAllocationLimit: *resource.NewQuantity(poolLimitFree, resource.BinarySI),
		})
	}

	capacity.Size = *resource.NewQuantity(size, resource.BinarySI)
	capacity.Used = *resource.NewQuantity(used, resource.BinarySI)
	capacity.FreeOnDisk = *resource.NewQuantity(free, resource.BinarySI)
	capacity.Allocated = resource.NewQuantity(allocated, resource.BinarySI)
	capacity.FreeByAllocationLimit = resource.NewQuantity(limitFree, resource.BinarySI)

	return capacity, nil
}

func capacityNeedsWrite(prev, next *slv.LocalStorageClassCapacity, now time.Time) bool {
	if next == nil {
		return prev != nil
	}
	if prev == nil || now.Sub(prev.UpdatedAt.Time) >= capacityRefreshPeriod {
		return true
	}
	if prev.PoolsWithoutData != next.PoolsWithoutData || len(prev.Pools) != len(next.Pools) {
		return true
	}
	if moved(prev.Size, next.Size, next.Size) ||
		moved(prev.Used, next.Used, next.Size) ||
		moved(prev.FreeOnDisk, next.FreeOnDisk, next.Size) ||
		sdscapacity.MovedOptional(prev.Allocated, next.Allocated, next.Size) ||
		sdscapacity.MovedOptional(prev.FreeByAllocationLimit, next.FreeByAllocationLimit, next.Size) {
		return true
	}
	for i := range next.Pools {
		p, n := prev.Pools[i], next.Pools[i]
		if p.LVMVolumeGroup != n.LVMVolumeGroup || p.ThinPool != n.ThinPool ||
			moved(p.Size, n.Size, n.Size) ||
			moved(p.Used, n.Used, n.Size) ||
			moved(p.FreeOnDisk, n.FreeOnDisk, n.Size) ||
			moved(p.FreeByAllocationLimit, n.FreeByAllocationLimit, n.Size) {
			return true
		}
	}
	return false
}

func moved(prev, next, of resource.Quantity) bool {
	delta := math.Abs(float64(next.Value() - prev.Value()))
	if delta == 0 {
		return false
	}
	return delta > capacityChangeShare*float64(of.Value())
}

func publishLocalStorageClassCapacity(ctx context.Context, cl client.Client, lsc *slv.LocalStorageClass, next *slv.LocalStorageClassCapacity, now time.Time) error {
	if next != nil {
		next = next.DeepCopy()
		next.UpdatedAt = metav1.NewTime(now)
	}
	return conditions.UpdateStatus(ctx, cl, lsc, func(sc *slv.LocalStorageClass) {
		if next == nil && sc.Status == nil {
			return
		}
		if sc.Status == nil {
			sc.Status = new(slv.LocalStorageClassStatus)
		}
		sc.Status.Capacity = next
	})
}

func syncLocalStorageClassCapacities(ctx context.Context, cl client.Client, log logger.Logger, now time.Time) error {
	lscList := &slv.LocalStorageClassList{}
	if err := cl.List(ctx, lscList); err != nil {
		return fmt.Errorf("list LocalStorageClasses: %w", err)
	}
	lvgList := &snc.LVMVolumeGroupList{}
	if err := cl.List(ctx, lvgList); err != nil {
		return fmt.Errorf("list LVMVolumeGroups: %w", err)
	}

	for i := range lscList.Items {
		lsc := &lscList.Items[i]
		if lsc.DeletionTimestamp != nil {
			continue
		}
		next, err := computeLocalStorageClassCapacity(lsc, lvgList)
		if err != nil {
			continue
		}
		var prev *slv.LocalStorageClassCapacity
		if lsc.Status != nil {
			prev = lsc.Status.Capacity
		}
		if !capacityNeedsWrite(prev, next, now) {
			continue
		}
		if err := publishLocalStorageClassCapacity(ctx, cl, lsc, next, now); err != nil {
			log.Error("unable to publish the capacity of the LocalStorageClass", logger.Err(err), "localStorageClass", lsc.Name)
		}
	}
	return nil
}

func RunLocalStorageClassCapacityPublisher(mgr manager.Manager, log logger.Logger) error {
	log = log.Named(LocalStorageClassCapacityCtrlName)
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		ticker := time.NewTicker(capacityCheckInterval)
		defer ticker.Stop()
		for {
			if err := syncLocalStorageClassCapacities(ctx, mgr.GetClient(), log, time.Now()); err != nil {
				log.Error("unable to sync the capacity of the LocalStorageClasses", logger.Err(err))
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}))
}
