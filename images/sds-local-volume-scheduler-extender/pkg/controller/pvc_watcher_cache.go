package controller

import (
	"context"
	"errors"
	"fmt"
	v1 "k8s.io/api/core/v1"
	cache2 "k8s.io/client-go/tools/cache"
	"k8s.io/utils/strings/slices"
	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	PVCWatcherCacheCtrlName = "pvc-watcher-cache-controller"
)

func RunPVCWatcherCacheController(
	ctx context.Context,
	mgr manager.Manager,
	log logger.Logger,
	schedulerCache *cache.Cache,
) error {
	log.Info("[RunPVCWatcherCacheController] starts the work WITH EVENTS")

	inf, err := mgr.GetCache().GetInformer(ctx, &v1.PersistentVolumeClaim{})
	if err != nil {
		log.Error(err, "[RunPVCWatcherCacheController] unable to get the informer")
		return err
	}

	_, err = inf.AddEventHandler(cache2.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			log.Info("[RunPVCWatcherCacheController] Add Func reconciliation starts")
			pvc, ok := obj.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] AddFunc starts the reconciliation for the PVC %s", pvc.Name))

			switch shouldAddPVCToCache(schedulerCache, pvc) {
			case true:
				// Добавляем в queue, иначе фильтр не сможет получить ее из кеша
				schedulerCache.AddPVCToFilterQueue(pvc)
				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s was added to the cache", pvc.Name))
			case false:
				// Update Func (если рухнули на апдейте)
				selectedNode, wasSelected := pvc.Annotations[cache.SelectedNodeAnnotation]
				if !wasSelected {
					log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s should not be reconciled by Add Func due it has not selected node annotation", pvc.Name))
					return
				}
				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s has node annotation, it will be reconciled in Add func", pvc.Name))

				//cachedPvc := schedulerCache.TryGetPVC(pvc)
				//if cachedPvc == nil {
				//	log.Error(fmt.Errorf("PVC %s was not found in the cache", pvc.Name), fmt.Sprintf("[RunPVCWatcherCacheController] unable to get PVC %s from the cache", pvc.Name))
				//	return
				//}

				lvgsOnTheNode := schedulerCache.GetLVGNamesByNodeName(selectedNode)
				lvgsForPVC := schedulerCache.GetLVGNamesForPVC(pvc)

				var lvgName string
				for _, pvcLvg := range lvgsForPVC {
					if slices.Contains(lvgsOnTheNode, pvcLvg) {
						lvgName = pvcLvg
					}
				}

				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s has status phase: %s", pvc.Name, pvc.Status.Phase))
				err = schedulerCache.UpdatePVC(lvgName, pvc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to update PVC %s in the cache", pvc.Name))
					return
				}

				// У PVC выбралась нода, но она еще не в баунд (в кеше PVC без ноды на лвгхах)
				err = schedulerCache.RemoveUnboundedPVCSpaceReservation(log, pvc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to remove space reservation in the cache for unbounded PVC %s", pvc.Name))
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			log.Info("[RunPVCWatcherCacheController] Update Func reconciliation starts")
			pvc, ok := newObj.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] UpdateFunc starts the reconciliation for the PVC %s", pvc.Name))

			selectedNode, wasSelected := pvc.Annotations[cache.SelectedNodeAnnotation]
			if !wasSelected {
				log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s should not be reconciled by Add Func due it has not selected node annotation", pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s has node annotation, it will be reconciled in Update func", pvc.Name))

			lvgsOnTheNode := schedulerCache.GetLVGNamesByNodeName(selectedNode)
			lvgsForPVC := schedulerCache.GetLVGNamesForPVC(pvc)

			var lvgName string
			for _, pvcLvg := range lvgsForPVC {
				if slices.Contains(lvgsOnTheNode, pvcLvg) {
					lvgName = pvcLvg
				}
			}
			log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s uses LVG %s on node %s", pvc.Name, lvgName, selectedNode))
			log.Trace(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s has status phase: %s", pvc.Name, pvc.Status.Phase))
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] updates cache PVC %s in LVG %s", pvc.Name, lvgName))
			err = schedulerCache.UpdatePVC(lvgName, pvc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to update PVC %s in the cache", pvc.Name))
				return
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] successfully updated cache PVC %s in LVG %s", pvc.Name, lvgName))

			// У PVC выбралась нода, но она еще не в баунд (в кеше PVC без ноды на лвгхах)
			err = schedulerCache.RemoveUnboundedPVCSpaceReservation(log, pvc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[RunPVCWatcherCacheController] unable to remove space reservation in the cache for unbounded PVC %s", pvc.Name))
			}
		},
		DeleteFunc: func(obj interface{}) {
			log.Info("[RunPVCWatcherCacheController] Delete Func reconciliation starts")
			pvc, ok := obj.(*v1.PersistentVolumeClaim)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[RunPVCWatcherCacheController] an error occurred while handling create event")
			}
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] DeleteFunc starts the reconciliation for the PVC %s", pvc.Name))

			schedulerCache.RemovePVCSpaceReservationForced(pvc)
			log.Debug(fmt.Sprintf("[RunPVCWatcherCacheController] PVC %s was force removed from the cache", pvc.Name))
		},
	})
	if err != nil {
		log.Error(err, "[RunPVCWatcherCacheController] unable to add event handler to the informer")
	}

	return nil
}

func shouldAddPVCToCache(schedulerCache *cache.Cache, pvc *v1.PersistentVolumeClaim) bool {
	//_, selected := pvc.Annotations[cache.SelectedNodeAnnotation]
	if pvc.Status.Phase != v1.ClaimBound {
		return true
	}

	exist := schedulerCache.CheckPVCInQueue(pvc)
	if !exist {
		return true
	}

	return false
}

//
//func findLVGByPVC(ctx context.Context, cl client.Client, pvc *v1.PersistentVolumeClaim) (*v1alpha1.LvmVolumeGroup, error) {
//	sc := &v12.StorageClass{}
//	// TODO: Будет ли проставлен storage class для PVC, если не будет указан явно (иначе зачем тут поинтер?)
//	err := cl.Get(ctx, client.ObjectKey{
//		Name: *pvc.Spec.StorageClassName,
//	}, sc)
//	if err != nil {
//		return nil, fmt.Errorf("[findLVGByPVC] unable to get a storage class %s", *pvc.Spec.StorageClassName)
//	}
//
//	lvgsFromSC, err := scheduler.ExtractLVGsFromSC(sc)
//
//	lvgList := &v1alpha1.LvmVolumeGroupList{}
//	err = cl.List(ctx, lvgList)
//	if err != nil {
//		return nil, fmt.Errorf("[findLVGByPVC] unable to list LVMVolumeGroups")
//	}
//
//	lvgs := make(map[string]v1alpha1.LvmVolumeGroup, len(lvgList.Items))
//	for _, lvg := range lvgList.Items {
//		lvgs[lvg.Name] = lvg
//	}
//
//	for _, lvg := range lvgsFromSC {
//		kubeLVG, exist := lvgs[lvg.Name]
//		if !exist {
//			return nil, fmt.Errorf("unable to found the LVMVolumeGroup %s for storage class %s", lvg.Name, sc.Name)
//		}
//
//		if kubeLVG.Status.Nodes == nil || len(kubeLVG.Status.Nodes) == 0 {
//			return nil, fmt.Errorf("no nodes specified for the LVMVolumeGroup %s for storage class %s", lvg.Name, sc.Name)
//		}
//
//		if kubeLVG.Status.Nodes[0].Name == pvc.Annotations[cache.SelectedNodeAnnotation] {
//			return &kubeLVG, nil
//		}
//	}
//
//	return nil, nil
//}
