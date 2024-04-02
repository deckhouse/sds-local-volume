package controller

import (
	"context"
	"errors"
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/util/workqueue"
	"sds-local-volume-scheduler-extender/api/v1alpha1"
	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	LVGWatcherCacheCtrlName = "lvg-watcher-cache-controller"
)

func RunLVGWatcherCacheController(
	mgr manager.Manager,
	log logger.Logger,
	cache *cache.Cache,
) (controller.Controller, error) {
	log.Info("[RunLVGWatcherCacheController] starts the work")

	c, err := controller.New(LVGWatcherCacheCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunCacheWatcherController] unable to create a controller")
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.LvmVolumeGroup{}), handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] CreateFunc starts the cache reconciliation for the LVMVolumeGroup %s", e.Object.GetName()))

			lvg, ok := e.Object.(*v1alpha1.LvmVolumeGroup)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunLVGWatcherCacheController] an error occurred while handling create event")
				return
			}

			if lvg.DeletionTimestamp != nil {
				log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s should not be reconciled", lvg.Name))
				return
			}

			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] tries to get the LVMVolumeGroup %s from the cache", lvg.Name))
			existedLVG := cache.TryGetLVG(lvg.Name)
			if existedLVG != nil {
				log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s was found in the cache. It will be updated", lvg.Name))
				err = cache.UpdateLVG(lvg)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to update the LVMVolumeGroup %s cache", lvg.Name))
				}

				log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] cache was updated for the LVMVolumeGroup %s", lvg.Name))
			} else {
				log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s was not found. It will be added to the cache", lvg.Name))
				err = cache.AddLVG(lvg)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to add the LVMVolumeGroup %s to the cache", lvg.Name))
				}

				log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] cache was added for the LVMVolumeGroup %s", lvg.Name))
			}

			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] starts to clear the cache for the LVMVolumeGroup %s", lvg.Name))
			pvcs := cache.GetAllPVCByLVG(lvg.Name)
			for _, pvc := range pvcs {
				log.Trace(fmt.Sprintf("[RunLVGWatcherCacheController] cached PVC %s belongs to LVMVolumeGroup %s", pvc.Name, lvg.Name))
				if pvc.Status.Phase == v1.ClaimBound {
					log.Trace(fmt.Sprintf("[RunLVGWatcherCacheController] cached PVC %s has Status.Phase Bound. It will be removed from the cache for LVMVolumeGroup %s", pvc.Name, lvg.Name))
					err = cache.RemoveBoundedPVCSpaceReservation(lvg.Name, pvc)
					if err != nil {
						log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to remove PVC %s from the cache for the LVMVolumeGroup %s", pvc.Name, lvg.Name))
						continue
					}

					log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] PVC %s was removed from the cache for LVMVolumeGroup %s", pvc.Name, lvg.Name))
				}
			}

			log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] cache for the LVMVolumeGroup %s was reconciled by CreateFunc", lvg.Name))
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[RunCacheWatcherController] UpdateFunc starts the cache reconciliation for the LVMVolumeGroup %s", e.ObjectNew.GetName()))
			oldLvg, ok := e.ObjectOld.(*v1alpha1.LvmVolumeGroup)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunLVGWatcherCacheController] an error occurred while handling create event")
				return
			}

			newLvg, ok := e.ObjectNew.(*v1alpha1.LvmVolumeGroup)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunLVGWatcherCacheController] an error occurred while handling create event")
				return
			}

			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] starts to calculate the size difference for LVMVolumeGroup %s", newLvg.Name))
			oldSize, err := resource.ParseQuantity(oldLvg.Status.AllocatedSize)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to parse the allocated size for the LVMVolumeGroup %s", oldLvg.Name))
				return
			}
			log.Trace(fmt.Sprintf("[RunLVGWatcherCacheController] old state LVMVolumeGroup %s has size %s", oldLvg.Name, oldSize.String()))

			newSize, err := resource.ParseQuantity(newLvg.Status.AllocatedSize)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to parse the allocated size for the LVMVolumeGroup %s", oldLvg.Name))
				return
			}
			log.Trace(fmt.Sprintf("[RunLVGWatcherCacheController] new state LVMVolumeGroup %s has size %s", newLvg.Name, newSize.String()))
			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] successfully calculated the size difference for LVMVolumeGroup %s", newLvg.Name))

			if newLvg.DeletionTimestamp != nil ||
				oldSize.Value() == newSize.Value() {
				log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s should not be reconciled", newLvg.Name))
				return
			}

			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s should be reconciled by Update Func. It will be updated in the cache", newLvg.Name))
			err = cache.UpdateLVG(newLvg)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to update the LVMVolumeGroup %s cache", newLvg.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] successfully updated the LVMVolumeGroup %s in the cache", newLvg.Name))

			cachedPvcs := cache.GetAllPVCByLVG(newLvg.Name)
			for _, pvc := range cachedPvcs {
				log.Trace(fmt.Sprintf("[RunLVGWatcherCacheController] PVC %s from the cache belongs to LVMVolumeGroup %s", pvc.Name, newLvg.Name))
				log.Trace(fmt.Sprintf("[RunLVGWatcherCacheController] PVC %s has status phase %s", pvc.Name, pvc.Status.Phase))
				if pvc.Status.Phase == v1.ClaimBound {
					log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] PVC %s from the cache has Status.Phase Bound. It will be removed from the reserved space in the LVMVolumeGroup %s", pvc.Name, newLvg.Name))
					err = cache.RemoveBoundedPVCSpaceReservation(newLvg.Name, pvc)
					if err != nil {
						log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to remove PVC %s from the cache in the LVMVolumeGroup %s", pvc.Name, newLvg.Name))
						continue
					}

					log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] PVC %s was removed from the LVMVolumeGroup %s in the cache", pvc.Name, newLvg.Name))
				}
			}

			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] Update Func ends reconciliation the LVMVolumeGroup %s cache", newLvg.Name))
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[RunCacheWatcherController] DeleteFunc starts the cache reconciliation for the LVMVolumeGroup %s", e.Object.GetName()))
			lvg, ok := e.Object.(*v1alpha1.LvmVolumeGroup)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunLVGWatcherCacheController] an error occurred while handling create event")
				return
			}
			cache.DeleteLVG(lvg.Name)
			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] LVMVolumeGroup %s was deleted from the cache", lvg.Name))
		},
	})
	if err != nil {
		log.Error(err, "[RunCacheWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}
