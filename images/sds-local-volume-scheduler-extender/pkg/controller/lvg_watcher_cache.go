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
	log.Info("[RunLVGWatcherCacheController] starts the work WITH EVENTS")
	//cl := mgr.GetClient()

	c, err := controller.New(LVGWatcherCacheCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			return reconcile.Result{}, nil
		})})
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

			// PopulateLVG
			log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] tries to get the LVMVolumeGroup %s from the cache", lvg.Name))
			existedLVG := cache.TryGetLVG(lvg.Name)

			if existedLVG != nil {
				log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s was found. It will be updated", lvg.Name))
				err = cache.UpdateLVG(lvg)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to update the LVMVolumeGroup %s cache", lvg.Name))
				}

				log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] cache was updated for the LVMVolumeGroup %s", lvg.Name))
			} else {
				log.Debug(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s was not found. It will be added", lvg.Name))
				err = cache.AddLVG(lvg)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to add the LVMVolumeGroup %s to the cache", lvg.Name))
				}

				log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] cache was added for the LVMVolumeGroup %s", lvg.Name))
			}

			//pvcs := &v1.PersistentVolumeClaimList{}
			//err := cl.List(ctx, pvcs)
			//if err != nil {
			//	log.Error(err, "[RunLVGWatcherCacheController] unable to list all PVCs")
			//	// TODO: requeue
			//	return
			//}
			//
			//scsList := &v12.StorageClassList{}
			//err = cl.List(ctx, scsList)
			//if err != nil {
			//	log.Error(err, "[RunLVGWatcherCacheController] unable to list all StorageClasses")
			//	// TODO: requeue
			//	return
			//}
			//scs := make(map[string]v12.StorageClass, len(scsList.Items))
			//for _, sc := range scsList.Items {
			//	scs[sc.Name] = sc
			//}
			//
			//lvgsBySC, err := scheduler.GetSortedLVGsFromStorageClasses(scs)
			//if err != nil {
			//	log.Error(err, "[RunLVGWatcherCacheController] unable to sort LVGs by StorageClasses")
			//	// TODO: requeue
			//	return
			//}

			pvcs := cache.GetAllPVCByLVG(lvg.Name)
			for _, pvc := range pvcs {
				if pvc.Status.Phase == v1.ClaimBound {
					cache.RemovePVCSpaceReservation(pvc.Name)
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

			oldSize, err := resource.ParseQuantity(oldLvg.Status.AllocatedSize)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to parse the allocated size for the LVMVolumeGroup %s", oldLvg.Name))
				return
			}

			newSize, err := resource.ParseQuantity(newLvg.Status.AllocatedSize)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to parse the allocated size for the LVMVolumeGroup %s", oldLvg.Name))
				return
			}

			if newLvg.DeletionTimestamp != nil ||
				oldSize.Value() == newSize.Value() {
				log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] the LVMVolumeGroup %s should not be reconciled", newLvg.Name))
				return
			}

			err = cache.UpdateLVG(newLvg)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunLVGWatcherCacheController] unable to update the LVMVolumeGroup %s cache", newLvg.Name))
			}

			pvcs := cache.GetAllPVCByLVG(newLvg.Name)
			for _, pvc := range pvcs {
				if pvc.Status.Phase == v1.ClaimBound {
					cache.RemovePVCSpaceReservation(pvc.Name)
				}
			}

			log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] updated LVMVolumeGroup %s cache size", newLvg.Name))
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
			log.Info(fmt.Sprintf("[RunLVGWatcherCacheController] LVMVolumeGroup %s was deleted from the cache", lvg.Name))
		},
	})
	if err != nil {
		log.Error(err, "[RunCacheWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}
