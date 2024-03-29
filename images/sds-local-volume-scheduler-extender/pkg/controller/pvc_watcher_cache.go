package controller

import (
	"context"
	"errors"
	"fmt"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/api/storage/v1"
	cache2 "k8s.io/client-go/tools/cache"
	"sds-local-volume-scheduler-extender/api/v1alpha1"
	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sds-local-volume-scheduler-extender/pkg/scheduler"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	PVCWatcherCacheCtrlName = "pvc-watcher-cache-controller"
	selectedNodeAnnotation  = "volume.kubernetes.io/selected-node"
)

func RunPVCWatcherCacheController(
	ctx context.Context,
	mgr manager.Manager,
	log logger.Logger,
	schedulerCache *cache.Cache,
) error {
	log.Info("[RunPVCWatcherCacheController] starts the work WITH EVENTS")

	cl := mgr.GetClient()
	inf, err := mgr.GetCache().GetInformer(ctx, &v1.PersistentVolumeClaim{})
	if err != nil {
		log.Error(err, "[RunPVCWatcherCacheController] unable to get the informer")
		return err
	}

	inf.AddEventHandler(cache2.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pvc, ok := obj.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] AddFunc starts the reconciliation for the PVC %s", pvc.Name))

			if shouldReconcilvePVC(pvc) {
				cachedPvc := schedulerCache.TryGetPVC(pvc.Name)
				if cachedPvc != nil {
					if pvc.Status.Phase == v1.ClaimBound {
						log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s in a Bound state. It will be removed from the cache"))
						//schedulerCache.RemovePVCSpaceReservation(pvc.Name)
						log.Info(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s was removed from the cache"))
						return
					}

					err := schedulerCache.UpdatePVC(pvc, pvc.Annotations[selectedNodeAnnotation])
					if err != nil {
						log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to update PVC %s in the cache", pvc.Name))
					}
				} else {
					lvg, err := findLVGByPVC(ctx, cl, pvc)
					if err != nil {
						log.Error(err, "[RunPVCWatcherCacheController] an error occurs the founding a LVMVolumeGroup")
						// TODO: requeue or something
						return
					}

					err = schedulerCache.AddPVC(lvg.Name, pvc, pvc.Annotations[selectedNodeAnnotation])
					if err != nil {
						log.Error(err, "[RunPVCWatcherCacheController] unable to add PVC to the cache")
						// TODO: requeue or something
						return
					}
				}
			}

		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			pvc, ok := newObj.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] AddFunc starts the reconciliation for the PVC %s", pvc.Name))

			if shouldReconcilvePVC(pvc) {
				cachedPvc := schedulerCache.TryGetPVC(pvc.Name)
				if cachedPvc != nil {
					if pvc.Status.Phase == v1.ClaimBound {
						log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s in a Bound state. It will be removed from the cache"))
						//schedulerCache.RemovePVCSpaceReservation(pvc.Name)
						log.Info(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s was removed from the cache"))
						return
					}

					err := schedulerCache.UpdatePVC(pvc, pvc.Annotations[selectedNodeAnnotation])
					if err != nil {
						log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to update PVC %s in the cache", pvc.Name))
					}
				} else {
					lvg, err := findLVGByPVC(ctx, cl, pvc)
					if err != nil {
						log.Error(err, "[RunPVCWatcherCacheController] an error occurs the founding a LVMVolumeGroup")
						// TODO: requeue or something
						return
					}

					err = schedulerCache.AddPVC(lvg.Name, pvc, pvc.Annotations[selectedNodeAnnotation])
					if err != nil {
						log.Error(err, "[RunPVCWatcherCacheController] unable to add PVC to the cache")
						// TODO: requeue or something
						return
					}
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			log.Info("HELLO FROM DELETEFUNC")
		},
	})

	return nil
}

func shouldReconcilvePVC(pvc *v1.PersistentVolumeClaim) bool {
	_, selected := pvc.Annotations[selectedNodeAnnotation]
	if pvc.Status.Phase != v1.ClaimBound && selected {
		return true
	}

	return false
}

func findLVGByPVC(ctx context.Context, cl client.Client, pvc *v1.PersistentVolumeClaim) (*v1alpha1.LvmVolumeGroup, error) {
	sc := &v12.StorageClass{}
	// TODO: Будет ли проставлен storage class для PVC, если не будет указан явно (иначе зачем тут поинтер?)
	err := cl.Get(ctx, client.ObjectKey{
		Name: *pvc.Spec.StorageClassName,
	}, sc)
	if err != nil {
		return nil, fmt.Errorf("[findLVGByPVC] unable to get a storage class %s", *pvc.Spec.StorageClassName)
	}

	lvgsFromSC, err := scheduler.ExtractLVGsFromSC(*sc)

	lvgList := &v1alpha1.LvmVolumeGroupList{}
	err = cl.List(ctx, lvgList)
	if err != nil {
		return nil, fmt.Errorf("[findLVGByPVC] unable to list LVMVolumeGroups")
	}

	lvgs := make(map[string]v1alpha1.LvmVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = lvg
	}

	for _, lvg := range lvgsFromSC {
		kubeLVG, exist := lvgs[lvg.Name]
		if !exist {
			return nil, fmt.Errorf("unable to found the LVMVolumeGroup %s for storage class %s", lvg.Name, sc.Name)
		}

		if kubeLVG.Status.Nodes == nil || len(kubeLVG.Status.Nodes) == 0 {
			return nil, fmt.Errorf("no nodes specified for the LVMVolumeGroup %s for storage class %s", lvg.Name, sc.Name)
		}

		if kubeLVG.Status.Nodes[0].Name == pvc.Annotations[selectedNodeAnnotation] {
			return &kubeLVG, nil
		}
	}

	return nil, nil
}
