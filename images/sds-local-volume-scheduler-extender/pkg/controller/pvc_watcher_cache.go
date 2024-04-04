package controller

import (
	"context"
	"errors"
	"fmt"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/api/storage/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/strings/slices"
	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sds-local-volume-scheduler-extender/pkg/scheduler"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	PVCWatcherCacheCtrlName = "pvc-watcher-cache-controller"
)

func RunPVCWatcherCacheController(
	mgr manager.Manager,
	log logger.Logger,
	schedulerCache *cache.Cache,
) error {
	log.Info("[RunPVCWatcherCacheController] starts the work")

	c, err := controller.New("test-pvc-watcher", mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunPVCWatcherCacheController] unable to create controller")
		return err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1.PersistentVolumeClaim{}), handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			log.Info("[RunPVCWatcherCacheController] CreateFunc reconciliation starts")
			pvc, ok := e.Object.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] CreateFunc starts the reconciliation for the PVC %s/%s", pvc.Namespace, pvc.Name))

			selectedNodeName, wasSelected := pvc.Annotations[cache.SelectedNodeAnnotation]
			if !wasSelected ||
				pvc.Status.Phase == v1.ClaimBound ||
				pvc.DeletionTimestamp != nil {
				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s should not be reconciled by CreateFunc", pvc.Namespace, pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s has selected node annotation, it will be reconciled in CreateFunc", pvc.Namespace, pvc.Name))
			log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s has been selected to the node %s", pvc.Namespace, pvc.Name, selectedNodeName))

			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] starts to find common LVMVolumeGroup for the selected node %s and PVC %s/%s", selectedNodeName, pvc.Namespace, pvc.Name))
			lvgsOnTheNode := schedulerCache.GetLVGNamesByNodeName(selectedNodeName)
			for _, lvgName := range lvgsOnTheNode {
				log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] LVMVolumeGroup %s belongs to the node %s", lvgName, selectedNodeName))
			}

			lvgsForPVC := schedulerCache.GetLVGNamesForPVC(pvc)
			if lvgsForPVC == nil || len(lvgsForPVC) == 0 {
				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] no LVMVolumeGroups were found in the cache for PVC %s/%s. Use Storage Class %s instead", pvc.Namespace, pvc.Name, *pvc.Spec.StorageClassName))
				sc := &v12.StorageClass{}
				err = mgr.GetClient().Get(ctx, client.ObjectKey{
					Name: *pvc.Spec.StorageClassName,
				}, sc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to get Storage Class %s for PVC %s/%s", *pvc.Spec.StorageClassName, pvc.Namespace, pvc.Name))
					return
				}

				lvgsFromSc, err := scheduler.ExtractLVGsFromSC(sc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to extract LVMVolumeGroups from the Storage Class %s", sc.Name))
				}

				for _, lvg := range lvgsFromSc {
					lvgsForPVC = append(lvgsForPVC, lvg.Name)
				}
			}
			for _, lvgName := range lvgsForPVC {
				log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] LVMVolumeGroup %s belongs to PVC %s/%s", lvgName, pvc.Namespace, pvc.Name))
			}

			var commonLVGName string
			for _, pvcLvg := range lvgsForPVC {
				if slices.Contains(lvgsOnTheNode, pvcLvg) {
					commonLVGName = pvcLvg
				}
			}
			if commonLVGName == "" {
				log.Error(errors.New("common LVMVolumeGroup was not found"), fmt.Sprintf("[RunPVCWatcherCacheController] unable to identify a LVMVolumeGroup for PVC %s/%s", pvc.Namespace, pvc.Name))
				return
			}

			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully found common LVMVolumeGroup %s for the selected node %s and PVC %s/%s", commonLVGName, selectedNodeName, pvc.Namespace, pvc.Name))
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] starts to update PVC %s/%s in the cache", pvc.Namespace, pvc.Name))
			log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s has status phase: %s", pvc.Namespace, pvc.Name, pvc.Status.Phase))
			err = schedulerCache.UpdatePVC(commonLVGName, pvc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to update PVC %s/%s in the cache", pvc.Namespace, pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully updated PVC %s/%s in the cache", pvc.Namespace, pvc.Name))

			schedulerCache.PrintTheCacheTraceLog()
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] starts to remove space reservation for PVC %s/%s with selected node from the cache", pvc.Namespace, pvc.Name))
			err = schedulerCache.RemoveSpaceReservationForPVCWithSelectedNode(pvc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to remove PVC %s/%s space reservation in the cache", pvc.Namespace, pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully removed space reservation for PVC %s/%s with selected node", pvc.Namespace, pvc.Name))
			schedulerCache.PrintTheCacheTraceLog()

			log.Info("[RunPVCWatcherCacheController] CreateFunc reconciliation ends")
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			log.Info("[RunPVCWatcherCacheController] Update Func reconciliation starts")
			pvc, ok := e.ObjectNew.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] UpdateFunc starts the reconciliation for the PVC %s/%s", pvc.Namespace, pvc.Name))

			selectedNodeName, wasSelected := pvc.Annotations[cache.SelectedNodeAnnotation]
			if !wasSelected || pvc.DeletionTimestamp != nil {
				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s should not be reconciled by UpdateFunc", pvc.Namespace, pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s has selected node annotation, it will be reconciled in UpdateFunc", pvc.Namespace, pvc.Name))
			log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s has been selected to the node %s", pvc.Namespace, pvc.Name, selectedNodeName))

			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] starts to find common LVMVolumeGroup for the selected node %s and PVC %s/%s", selectedNodeName, pvc.Namespace, pvc.Name))
			lvgsOnTheNode := schedulerCache.GetLVGNamesByNodeName(selectedNodeName)
			for _, lvgName := range lvgsOnTheNode {
				log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] LVMVolumeGroup %s belongs to the node %s", lvgName, selectedNodeName))
			}

			lvgsForPVC := schedulerCache.GetLVGNamesForPVC(pvc)
			if lvgsForPVC == nil || len(lvgsForPVC) == 0 {
				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] no LVMVolumeGroups were found in the cache for PVC %s/%s. Use Storage Class %s instead", pvc.Namespace, pvc.Name, *pvc.Spec.StorageClassName))
				sc := &v12.StorageClass{}
				err = mgr.GetClient().Get(ctx, client.ObjectKey{
					Name: *pvc.Spec.StorageClassName,
				}, sc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to get Storage Class %s for PVC %s/%s", *pvc.Spec.StorageClassName, pvc.Namespace, pvc.Name))
					return
				}

				lvgsFromSc, err := scheduler.ExtractLVGsFromSC(sc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to extract LVMVolumeGroups from the Storage Class %s", sc.Name))
				}

				for _, lvg := range lvgsFromSc {
					lvgsForPVC = append(lvgsForPVC, lvg.Name)
				}
			}
			for _, lvgName := range lvgsForPVC {
				log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] LVMVolumeGroup %s belongs to PVC %s/%s", lvgName, pvc.Namespace, pvc.Name))
			}

			var commonLVGName string
			for _, pvcLvg := range lvgsForPVC {
				if slices.Contains(lvgsOnTheNode, pvcLvg) {
					commonLVGName = pvcLvg
				}
			}
			if commonLVGName == "" {
				log.Error(errors.New("common LVMVolumeGroup was not found"), fmt.Sprintf("[RunPVCWatcherCacheController] unable to identify a LVMVolumeGroup for PVC %s/%s", pvc.Namespace, pvc.Name))
				return
			}

			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully found common LVMVolumeGroup %s for the selected node %s and PVC %s/%s", commonLVGName, selectedNodeName, pvc.Namespace, pvc.Name))
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] starts to update PVC %s/%s in the cache", pvc.Namespace, pvc.Name))
			log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s/%s has status phase: %s", pvc.Namespace, pvc.Name, pvc.Status.Phase))
			err = schedulerCache.UpdatePVC(commonLVGName, pvc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to update PVC %s/%s in the cache", pvc.Namespace, pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully updated PVC %s/%s in the cache", pvc.Namespace, pvc.Name))

			schedulerCache.PrintTheCacheTraceLog()
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] starts to remove space reservation for PVC %s/%s with selected node from the cache", pvc.Namespace, pvc.Name))
			err = schedulerCache.RemoveSpaceReservationForPVCWithSelectedNode(pvc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to remove PVC %s/%s space reservation in the cache", pvc.Namespace, pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully removed space reservation for PVC %s/%s with selected node", pvc.Namespace, pvc.Name))
			schedulerCache.PrintTheCacheTraceLog()

			log.Info("[RunPVCWatcherCacheController] Update Func reconciliation ends")
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.RateLimitingInterface) {
			log.Info("[RunPVCWatcherCacheController] Delete Func reconciliation starts")
			pvc, ok := e.Object.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] DeleteFunc starts the reconciliation for the PVC %s", pvc.Name))

			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s was removed from the cluster. It will be force deleted from the cache", pvc.Name))
			schedulerCache.RemovePVCSpaceReservationForced(pvc)
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully force removed PVC %s from the cache", pvc.Name))
		},
	})
	if err != nil {
		log.Error(err, "[RunPVCWatcherCacheController] unable to controller Watch")
		return err
	}

	return nil
}
