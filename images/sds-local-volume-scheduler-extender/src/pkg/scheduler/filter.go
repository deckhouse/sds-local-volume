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

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/consts"
	"sds-local-volume-scheduler-extender/pkg/logger"
)

func (s *scheduler) filter(w http.ResponseWriter, r *http.Request) {
	s.log.Debug("[filter] starts the serving")
	var inputData ExtenderArgs
	reader := http.MaxBytesReader(w, r.Body, 10<<20)
	err := json.NewDecoder(reader).Decode(&inputData)
	if err != nil {
		s.log.Error(err, "[filter] unable to decode a request")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Trace(fmt.Sprintf("[filter] input data: %+v", inputData))

	if inputData.Pod == nil {
		s.log.Error(errors.New("no pod in the request"), "[filter] unable to get a Pod from the request")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	nodeNames, err := getNodeNames(inputData)
	if err != nil {
		s.log.Error(err, "[filter] unable to get node names from the request")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.log.Debug(fmt.Sprintf("[filter] starts the filtering for Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
	s.log.Trace(fmt.Sprintf("[filter] Pod from the request: %+v", inputData.Pod))
	s.log.Trace(fmt.Sprintf("[filter] Node names from the request: %+v", nodeNames))

	s.log.Debug(fmt.Sprintf("[filter] Find out if the Pod %s/%s should be processed", inputData.Pod.Namespace, inputData.Pod.Name))
	shouldProcess, err := shouldProcessPod(s.ctx, s.client, s.log, inputData.Pod, consts.SdsLocalVolumeProvisioner)
	if err != nil {
		s.log.Error(err, "[filter] unable to check if the Pod should be processed")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !shouldProcess {
		s.log.Debug(fmt.Sprintf("[filter] Pod %s/%s should not be processed. Return the same nodes", inputData.Pod.Namespace, inputData.Pod.Name))
		filteredNodes := &ExtenderFilterResult{
			NodeNames: &nodeNames,
		}
		s.log.Trace(fmt.Sprintf("[filter] filtered nodes: %+v", filteredNodes))

		w.Header().Set("content-type", "application/json")
		err = json.NewEncoder(w).Encode(filteredNodes)
		if err != nil {
			s.log.Error(err, "[filter] unable to encode a response")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		return
	}
	s.log.Debug(fmt.Sprintf("[filter] Pod %s/%s should be processed", inputData.Pod.Namespace, inputData.Pod.Name))

	pvcs, err := getUsedPVC(s.ctx, s.client, s.log, inputData.Pod)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[filter] unable to get used PVC for a Pod %s in the namespace %s", inputData.Pod.Name, inputData.Pod.Namespace))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if len(pvcs) == 0 {
		s.log.Error(fmt.Errorf("no PVC was found for pod %s in namespace %s", inputData.Pod.Name, inputData.Pod.Namespace), fmt.Sprintf("[filter] unable to get used PVC for Pod %s", inputData.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, pvc := range pvcs {
		s.log.Trace(fmt.Sprintf("[filter] Pod %s/%s uses PVC: %s", inputData.Pod.Namespace, inputData.Pod.Name, pvc.Name))

		// this might happen when the extender-scheduler recovers after failure, populates the cache with PVC-watcher controller and then
		// the kube scheduler post a request to schedule the pod with the PVC.
		if s.cache.CheckIsPVCStored(pvc) {
			s.log.Debug(fmt.Sprintf("[filter] PVC %s/%s has been already stored in the cache. Old state will be removed from the cache", pvc.Namespace, pvc.Name))
			s.cache.RemovePVCFromTheCache(pvc)
		} else {
			s.log.Debug(fmt.Sprintf("[filter] PVC %s/%s was not found in the scheduler cache", pvc.Namespace, pvc.Name))
		}
	}

	scs, err := getStorageClassesUsedByPVCs(s.ctx, s.client, pvcs)
	if err != nil {
		s.log.Error(err, "[filter] unable to get StorageClasses from the PVC")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, sc := range scs {
		s.log.Trace(fmt.Sprintf("[filter] Pod %s/%s uses StorageClass: %s", inputData.Pod.Namespace, inputData.Pod.Name, sc.Name))
	}

	managedPVCs := filterNotManagedPVC(s.log, pvcs, scs)
	for _, pvc := range managedPVCs {
		s.log.Trace(fmt.Sprintf("[filter] filtered managed PVC %s/%s", pvc.Namespace, pvc.Name))
	}

	if len(managedPVCs) == 0 {
		s.log.Warning(fmt.Sprintf("[filter] Pod %s/%s uses PVCs which are not managed by the module. Unable to filter and score the nodes", inputData.Pod.Namespace, inputData.Pod.Name))
		return
	}

	s.log.Debug(fmt.Sprintf("[filter] starts to extract PVC requested sizes for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
	pvcRequests, err := extractRequestedSize(s.ctx, s.client, s.log, managedPVCs, scs)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[filter] unable to extract request size for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Debug(fmt.Sprintf("[filter] successfully extracted the PVC requested sizes of a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))

	s.log.Debug(fmt.Sprintf("[filter] starts to filter the nodes from the request for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
	filteredNodes, err := filterNodes(s.log, s.cache, &nodeNames, inputData.Pod, managedPVCs, scs, pvcRequests)
	if err != nil {
		s.log.Error(err, "[filter] unable to filter the nodes")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Debug(fmt.Sprintf("[filter] successfully filtered the nodes from the request for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))

	s.log.Debug(fmt.Sprintf("[filter] starts to populate the cache for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
	s.log.Cache(fmt.Sprintf("[filter] cache before the PVC reservation for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
	s.cache.PrintTheCacheLog()
	err = populateCache(s.log, filteredNodes.NodeNames, inputData.Pod, s.cache, managedPVCs, scs)
	if err != nil {
		s.log.Error(err, "[filter] unable to populate cache")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Debug(fmt.Sprintf("[filter] successfully populated the cache for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
	s.log.Cache(fmt.Sprintf("[filter] cache after the PVC reservation for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
	s.cache.PrintTheCacheLog()

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(filteredNodes)
	if err != nil {
		s.log.Error(err, "[filter] unable to encode a response")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.log.Debug(fmt.Sprintf("[filter] ends the serving the request for a Pod %s/%s", inputData.Pod.Namespace, inputData.Pod.Name))
}

func filterNotManagedPVC(log logger.Logger, pvcs map[string]*corev1.PersistentVolumeClaim, scs map[string]*v1.StorageClass) map[string]*corev1.PersistentVolumeClaim {
	filteredPVCs := make(map[string]*corev1.PersistentVolumeClaim, len(pvcs))
	for _, pvc := range pvcs {
		sc := scs[*pvc.Spec.StorageClassName]
		if sc.Provisioner != consts.SdsLocalVolumeProvisioner {
			log.Debug(fmt.Sprintf("[filterNotManagedPVC] filter out PVC %s/%s due to used Storage class %s is not managed by sds-local-volume-provisioner", pvc.Name, pvc.Namespace, sc.Name))
			continue
		}

		filteredPVCs[pvc.Name] = pvc
	}

	return filteredPVCs
}

func populateCache(log logger.Logger, nodeNames *[]string, pod *corev1.Pod, schedulerCache *cache.Cache, pvcs map[string]*corev1.PersistentVolumeClaim, scs map[string]*v1.StorageClass) error {
	for _, nodeName := range *nodeNames {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				log.Debug(fmt.Sprintf("[populateCache] reconcile the PVC %s for Pod %s/%s on node %s", volume.PersistentVolumeClaim.ClaimName, pod.Namespace, pod.Name, nodeName))
				lvgNamesForTheNode := schedulerCache.GetLVGNamesByNodeName(nodeName)
				log.Trace(fmt.Sprintf("[populateCache] LVMVolumeGroups from cache for the node %s: %v", nodeName, lvgNamesForTheNode))
				pvc := pvcs[volume.PersistentVolumeClaim.ClaimName]
				sc := scs[*pvc.Spec.StorageClassName]

				lvgsForPVC, err := ExtractLVGsFromSC(sc)
				if err != nil {
					return err
				}

				switch sc.Parameters[consts.LvmTypeParamKey] {
				case consts.Thick:
					log.Debug(fmt.Sprintf("[populateCache] Storage Class %s has device type Thick, so the cache will be populated by PVC space requests", sc.Name))
					log.Trace(fmt.Sprintf("[populateCache] LVMVolumeGroups from Storage Class %s for PVC %s/%s: %+v", sc.Name, pvc.Namespace, pvc.Name, lvgsForPVC))
					for _, lvg := range lvgsForPVC {
						if slices.Contains(lvgNamesForTheNode, lvg.Name) {
							log.Trace(fmt.Sprintf("[populateCache] PVC %s/%s will reserve space in LVMVolumeGroup %s cache", pvc.Namespace, pvc.Name, lvg.Name))
							err = schedulerCache.AddThickPVC(lvg.Name, pvc)
							if err != nil {
								return err
							}
						}
					}
				case consts.Thin:
					log.Debug(fmt.Sprintf("[populateCache] Storage Class %s has device type Thin, so the cache will be populated by PVC space requests", sc.Name))
					log.Trace(fmt.Sprintf("[populateCache] LVMVolumeGroups from Storage Class %s for PVC %s/%s: %+v", sc.Name, pvc.Namespace, pvc.Name, lvgsForPVC))
					for _, lvg := range lvgsForPVC {
						if slices.Contains(lvgNamesForTheNode, lvg.Name) {
							log.Trace(fmt.Sprintf("[populateCache] PVC %s/%s will reserve space in LVMVolumeGroup %s Thin Pool %s cache", pvc.Namespace, pvc.Name, lvg.Name, lvg.Thin.PoolName))
							err = schedulerCache.AddThinPVC(lvg.Name, lvg.Thin.PoolName, pvc)
							if err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}

	return nil
}

type PVCRequest struct {
	DeviceType    string
	RequestedSize int64
}

func extractRequestedSize(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	pvcs map[string]*corev1.PersistentVolumeClaim,
	scs map[string]*v1.StorageClass,
) (map[string]PVCRequest, error) {
	pvs, err := getPersistentVolumes(ctx, cl)
	if err != nil {
		return nil, err
	}

	pvcRequests := make(map[string]PVCRequest, len(pvcs))
	for _, pvc := range pvcs {
		sc := scs[*pvc.Spec.StorageClassName]
		log.Debug(fmt.Sprintf("[extractRequestedSize] PVC %s/%s has status phase: %s", pvc.Namespace, pvc.Name, pvc.Status.Phase))
		switch pvc.Status.Phase {
		case corev1.ClaimPending:
			switch sc.Parameters[consts.LvmTypeParamKey] {
			case consts.Thick:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    consts.Thick,
					RequestedSize: pvc.Spec.Resources.Requests.Storage().Value(),
				}
			case consts.Thin:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    consts.Thin,
					RequestedSize: pvc.Spec.Resources.Requests.Storage().Value(),
				}
			}

		case corev1.ClaimBound:
			pv := pvs[pvc.Spec.VolumeName]
			switch sc.Parameters[consts.LvmTypeParamKey] {
			case consts.Thick:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    consts.Thick,
					RequestedSize: pvc.Spec.Resources.Requests.Storage().Value() - pv.Spec.Capacity.Storage().Value(),
				}
			case consts.Thin:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    consts.Thin,
					RequestedSize: pvc.Spec.Resources.Requests.Storage().Value() - pv.Spec.Capacity.Storage().Value(),
				}
			}
		}
	}

	for name, req := range pvcRequests {
		log.Trace(fmt.Sprintf("[extractRequestedSize] pvc %s has requested size: %d, device type: %s", name, req.RequestedSize, req.DeviceType))
	}

	return pvcRequests, nil
}

func filterNodes(
	log logger.Logger,
	schedulerCache *cache.Cache,
	nodeNames *[]string,
	pod *corev1.Pod,
	pvcs map[string]*corev1.PersistentVolumeClaim,
	scs map[string]*v1.StorageClass,
	pvcRequests map[string]PVCRequest,
) (*ExtenderFilterResult, error) {
	// Param "pvcRequests" is a total amount of the pvcRequests space (both Thick and Thin) for Pod (i.e. from every PVC)
	if len(pvcRequests) == 0 {
		return &ExtenderFilterResult{
			NodeNames: nodeNames,
		}, nil
	}

	lvgs := schedulerCache.GetAllLVG()
	for _, lvg := range lvgs {
		log.Trace(fmt.Sprintf("[filterNodes] LVMVolumeGroup %s in the cache", lvg.Name))
	}

	log.Debug(fmt.Sprintf("[filterNodes] starts to get LVMVolumeGroups for Storage Classes for a Pod %s/%s", pod.Namespace, pod.Name))
	scLVGs, err := GetSortedLVGsFromStorageClasses(scs)
	if err != nil {
		return nil, err
	}
	log.Debug(fmt.Sprintf("[filterNodes] successfully got LVMVolumeGroups for Storage Classes for a Pod %s/%s", pod.Namespace, pod.Name))
	for scName, sortedLVGs := range scLVGs {
		for _, lvg := range sortedLVGs {
			log.Trace(fmt.Sprintf("[filterNodes] LVMVolumeGroup %s belongs to Storage Class %s", lvg.Name, scName))
		}
	}

	usedLVGs := RemoveUnusedLVGs(lvgs, scLVGs)
	for _, lvg := range usedLVGs {
		log.Trace(fmt.Sprintf("[filterNodes] the LVMVolumeGroup %s is actually used. VG size: %s, allocatedSize: %s", lvg.Name, lvg.Status.VGSize.String(), lvg.Status.AllocatedSize.String()))
	}

	lvgsThickFree := getLVGThickFreeSpaces(usedLVGs)
	log.Trace(fmt.Sprintf("[filterNodes] for a Pod %s/%s current LVMVolumeGroups Thick FreeSpace on the node: %+v", pod.Namespace, pod.Name, lvgsThickFree))
	for lvgName, freeSpace := range lvgsThickFree {
		log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroup %s Thick free space %s", lvgName, resource.NewQuantity(freeSpace, resource.BinarySI)))
		reservedSpace, err := schedulerCache.GetLVGThickReservedSpace(lvgName)
		if err != nil {
			log.Error(err, fmt.Sprintf("[filterNodes] unable to count cache reserved space for the LVMVolumeGroup %s", lvgName))
			continue
		}
		log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroup %s reserved PVC space %s", lvgName, resource.NewQuantity(reservedSpace, resource.BinarySI)))
		lvgsThickFree[lvgName] -= reservedSpace
	}
	log.Trace(fmt.Sprintf("[filterNodes] for a Pod %s/%s current LVMVolumeGroups Thick FreeSpace with reserved PVC: %+v", pod.Namespace, pod.Name, lvgsThickFree))

	lvgsThinFree := getLVGThinFreeSpaces(usedLVGs)
	log.Trace(fmt.Sprintf("[filterNodes] for a Pod %s/%s current LVMVolumeGroups Thin FreeSpace on the node: %+v", pod.Namespace, pod.Name, lvgsThinFree))
	for lvgName, thinPools := range lvgsThinFree {
		for tpName, freeSpace := range thinPools {
			log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroup %s Thin Pool %s free space %s", lvgName, tpName, resource.NewQuantity(freeSpace, resource.BinarySI)))
			reservedSpace, err := schedulerCache.GetLVGThinReservedSpace(lvgName, tpName)
			if err != nil {
				log.Error(err, fmt.Sprintf("[filterNodes] unable to count cache reserved space for the Thin pool %s of the LVMVolumeGroup %s", tpName, lvgName))
				continue
			}
			log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroup %s Thin pool %s reserved PVC space %s", lvgName, tpName, resource.NewQuantity(reservedSpace, resource.BinarySI)))
			lvgsThinFree[lvgName][tpName] -= reservedSpace
		}
	}

	nodeLVGs := SortLVGsByNodeName(usedLVGs)
	for n, ls := range nodeLVGs {
		for _, l := range ls {
			log.Trace(fmt.Sprintf("[filterNodes] the LVMVolumeGroup %s belongs to node %s", l.Name, n))
		}
	}

	// these are the nodes which might store every PVC from the Pod
	commonNodes, err := getCommonNodesByStorageClasses(scs, nodeLVGs)
	if err != nil {
		log.Error(err, fmt.Sprintf("[filterNodes] unable to get common nodes for PVCs from the Pod %s/%s", pod.Namespace, pod.Name))
		return nil, err
	}
	for nodeName := range commonNodes {
		log.Trace(fmt.Sprintf("[filterNodes] Node %s is a common for every storage class", nodeName))
	}

	result := &ExtenderFilterResult{
		NodeNames:   &[]string{},
		FailedNodes: FailedNodesMap{},
	}

	thickMapMtx := &sync.RWMutex{}
	thinMapMtx := &sync.RWMutex{}
	failedNodesMapMtx := &sync.Mutex{}

	wg := &sync.WaitGroup{}
	wg.Add(len(*nodeNames))
	errs := make(chan error, len(*nodeNames)*len(pvcs))

	for i, nodeName := range *nodeNames {
		go func(i int, nodeName string) {
			log.Trace(fmt.Sprintf("[filterNodes] gourutine %d starts the work with node %s", i, nodeName))
			defer func() {
				log.Trace(fmt.Sprintf("[filterNodes] gourutine %d ends the work with node %s", i, nodeName))
				wg.Done()
			}()

			if _, common := commonNodes[nodeName]; !common {
				log.Debug(fmt.Sprintf("[filterNodes] node %s is not common for used Storage Classes %+v", nodeName, scs))
				failedNodesMapMtx.Lock()
				result.FailedNodes[nodeName] = fmt.Sprintf("node %s is not common for used Storage Classes", nodeName)
				failedNodesMapMtx.Unlock()
				return
			}

			// we get all LVMVolumeGroups from the node-applicant (which is common for all the PVCs)
			lvgsFromNode := commonNodes[nodeName]
			hasEnoughSpace := true

			// now we iterate all over the PVCs to see if we can place all of them on the node (does the node have enough space)
			for _, pvc := range pvcs {
				pvcReq := pvcRequests[pvc.Name]

				// we get LVGs which might be used by the PVC
				lvgsFromSC := scLVGs[*pvc.Spec.StorageClassName]

				// we get the specific LVG which the PVC can use on the node as we support only one specified LVG in the Storage Class on each node
				commonLVG := findMatchedLVG(lvgsFromNode, lvgsFromSC)
				if commonLVG == nil {
					err = fmt.Errorf("unable to match Storage Class's LVMVolumeGroup with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, nodeName)
					errs <- err
					return
				}
				log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s is common for storage class %s and node %s", commonLVG.Name, *pvc.Spec.StorageClassName, nodeName))

				// see what kind of space does the PVC need
				switch pvcReq.DeviceType {
				case consts.Thick:
					thickMapMtx.RLock()
					freeSpace := lvgsThickFree[commonLVG.Name]
					thickMapMtx.RUnlock()

					log.Trace(fmt.Sprintf("[filterNodes] LVMVolumeGroup %s Thick free space: %s, PVC requested space: %s", commonLVG.Name, resource.NewQuantity(freeSpace, resource.BinarySI), resource.NewQuantity(pvcReq.RequestedSize, resource.BinarySI)))
					if freeSpace < pvcReq.RequestedSize {
						hasEnoughSpace = false
						break
					}

					thickMapMtx.Lock()
					lvgsThickFree[commonLVG.Name] -= pvcReq.RequestedSize
					thickMapMtx.Unlock()
				case consts.Thin:
					lvg := lvgs[commonLVG.Name]

					// we try to find specific ThinPool which the PVC can use in the LVMVolumeGroup
					targetThinPool := findMatchedThinPool(lvg.Status.ThinPools, commonLVG.Thin.PoolName)
					if targetThinPool == nil {
						err = fmt.Errorf("unable to match Storage Class's ThinPools with the node's one, Storage Class: %s; node: %s; lvg Thin pools: %+v; Thin.poolName from StorageClass: %s", *pvc.Spec.StorageClassName, nodeName, lvg.Status.ThinPools, commonLVG.Thin.PoolName)
						errs <- err
						return
					}

					thinMapMtx.RLock()
					freeSpace := lvgsThinFree[lvg.Name][targetThinPool.Name]
					thinMapMtx.RUnlock()

					log.Trace(fmt.Sprintf("[filterNodes] LVMVolumeGroup %s Thin Pool %s free space: %s, PVC requested space: %s", lvg.Name, targetThinPool.Name, resource.NewQuantity(freeSpace, resource.BinarySI), resource.NewQuantity(pvcReq.RequestedSize, resource.BinarySI)))

					if freeSpace < pvcReq.RequestedSize {
						hasEnoughSpace = false
						break
					}

					thinMapMtx.Lock()
					lvgsThinFree[lvg.Name][targetThinPool.Name] -= pvcReq.RequestedSize
					thinMapMtx.Unlock()
				}

				if !hasEnoughSpace {
					// we break as if only one PVC can't get enough space, the node does not fit
					break
				}
			}

			if !hasEnoughSpace {
				failedNodesMapMtx.Lock()
				result.FailedNodes[nodeName] = "not enough space"
				failedNodesMapMtx.Unlock()
				return
			}

			*result.NodeNames = append(*result.NodeNames, nodeName)
		}(i, nodeName)
	}
	wg.Wait()
	log.Debug("[filterNodes] goroutines work is done")
	if len(errs) != 0 {
		for err = range errs {
			log.Error(err, "[filterNodes] an error occurs while filtering the nodes")
		}
	}
	close(errs)
	if err != nil {
		log.Error(err, fmt.Sprintf("[filterNodes] unable to filter nodes for the Pod %s/%s, last error: %s", pod.Namespace, pod.Name, err.Error()))
		return nil, err
	}

	for _, nodeName := range *result.NodeNames {
		log.Trace(fmt.Sprintf("[filterNodes] for a Pod %s/%s there is a suitable node: %s", pod.Namespace, pod.Name, nodeName))
	}

	for node, reason := range result.FailedNodes {
		log.Trace(fmt.Sprintf("[filterNodes] for a Pod %s/%s there is a failed node: %s, reason: %s", pod.Namespace, pod.Name, node, reason))
	}

	return result, nil
}

func getLVGThinFreeSpaces(lvgs map[string]*snc.LVMVolumeGroup) map[string]map[string]int64 {
	result := make(map[string]map[string]int64, len(lvgs))

	for _, lvg := range lvgs {
		if result[lvg.Name] == nil {
			result[lvg.Name] = make(map[string]int64, len(lvg.Status.ThinPools))
		}

		for _, tp := range lvg.Status.ThinPools {
			result[lvg.Name][tp.Name] = tp.AvailableSpace.Value()
		}
	}

	return result
}

func getLVGThickFreeSpaces(lvgs map[string]*snc.LVMVolumeGroup) map[string]int64 {
	result := make(map[string]int64, len(lvgs))

	for _, lvg := range lvgs {
		result[lvg.Name] = lvg.Status.VGFree.Value()
	}

	return result
}

func findMatchedThinPool(thinPools []snc.LVMVolumeGroupThinPoolStatus, name string) *snc.LVMVolumeGroupThinPoolStatus {
	for _, tp := range thinPools {
		if tp.Name == name {
			return &tp
		}
	}

	return nil
}

func findMatchedLVG(nodeLVGs []*snc.LVMVolumeGroup, scLVGs LVMVolumeGroups) *LVMVolumeGroup {
	nodeLVGNames := make(map[string]struct{}, len(nodeLVGs))
	for _, lvg := range nodeLVGs {
		nodeLVGNames[lvg.Name] = struct{}{}
	}

	for _, lvg := range scLVGs {
		if _, match := nodeLVGNames[lvg.Name]; match {
			return &lvg
		}
	}

	return nil
}

func getCommonNodesByStorageClasses(scs map[string]*v1.StorageClass, nodesWithLVGs map[string][]*snc.LVMVolumeGroup) (map[string][]*snc.LVMVolumeGroup, error) {
	result := make(map[string][]*snc.LVMVolumeGroup, len(nodesWithLVGs))

	for nodeName, lvgs := range nodesWithLVGs {
		lvgNames := make(map[string]struct{}, len(lvgs))
		for _, l := range lvgs {
			lvgNames[l.Name] = struct{}{}
		}

		nodeIncludesLVG := true
		for _, sc := range scs {
			scLvgs, err := ExtractLVGsFromSC(sc)
			if err != nil {
				return nil, err
			}

			contains := false
			for _, lvg := range scLvgs {
				if _, exist := lvgNames[lvg.Name]; exist {
					contains = true
					break
				}
			}

			if !contains {
				nodeIncludesLVG = false
				break
			}
		}

		if nodeIncludesLVG {
			result[nodeName] = lvgs
		}
	}

	return result, nil
}

func RemoveUnusedLVGs(lvgs map[string]*snc.LVMVolumeGroup, scsLVGs map[string]LVMVolumeGroups) map[string]*snc.LVMVolumeGroup {
	result := make(map[string]*snc.LVMVolumeGroup, len(lvgs))
	usedLvgs := make(map[string]struct{}, len(lvgs))

	for _, scLvgs := range scsLVGs {
		for _, lvg := range scLvgs {
			usedLvgs[lvg.Name] = struct{}{}
		}
	}

	for _, lvg := range lvgs {
		if _, used := usedLvgs[lvg.Name]; used {
			result[lvg.Name] = lvg
		}
	}

	return result
}

func GetSortedLVGsFromStorageClasses(scs map[string]*v1.StorageClass) (map[string]LVMVolumeGroups, error) {
	result := make(map[string]LVMVolumeGroups, len(scs))

	for _, sc := range scs {
		lvgs, err := ExtractLVGsFromSC(sc)
		if err != nil {
			return nil, err
		}

		result[sc.Name] = append(result[sc.Name], lvgs...)
	}

	return result, nil
}

type LVMVolumeGroup struct {
	Name string `yaml:"name"`
	Thin struct {
		PoolName string `yaml:"poolName"`
	} `yaml:"Thin"`
}
type LVMVolumeGroups []LVMVolumeGroup

func ExtractLVGsFromSC(sc *v1.StorageClass) (LVMVolumeGroups, error) {
	var lvmVolumeGroups LVMVolumeGroups
	err := yaml.Unmarshal([]byte(sc.Parameters[consts.LVMVolumeGroupsParamKey]), &lvmVolumeGroups)
	if err != nil {
		return nil, err
	}
	return lvmVolumeGroups, nil
}

func SortLVGsByNodeName(lvgs map[string]*snc.LVMVolumeGroup) map[string][]*snc.LVMVolumeGroup {
	sorted := make(map[string][]*snc.LVMVolumeGroup, len(lvgs))
	for _, lvg := range lvgs {
		for _, node := range lvg.Status.Nodes {
			sorted[node.Name] = append(sorted[node.Name], lvg)
		}
	}

	return sorted
}

func getPersistentVolumes(ctx context.Context, cl client.Client) (map[string]corev1.PersistentVolume, error) {
	pvs := &corev1.PersistentVolumeList{}
	err := cl.List(ctx, pvs)
	if err != nil {
		return nil, err
	}

	pvMap := make(map[string]corev1.PersistentVolume, len(pvs.Items))
	for _, pv := range pvs.Items {
		pvMap[pv.Name] = pv
	}

	return pvMap, nil
}

func getStorageClassesUsedByPVCs(ctx context.Context, cl client.Client, pvcs map[string]*corev1.PersistentVolumeClaim) (map[string]*v1.StorageClass, error) {
	scs := &v1.StorageClassList{}
	err := cl.List(ctx, scs)
	if err != nil {
		return nil, err
	}

	scMap := make(map[string]v1.StorageClass, len(scs.Items))
	for _, sc := range scs.Items {
		scMap[sc.Name] = sc
	}

	result := make(map[string]*v1.StorageClass, len(pvcs))
	for _, pvc := range pvcs {
		if pvc.Spec.StorageClassName == nil {
			err = fmt.Errorf("no StorageClass specified for PVC %s", pvc.Name)
			return nil, err
		}

		scName := *pvc.Spec.StorageClassName
		if sc, match := scMap[scName]; match {
			result[sc.Name] = &sc
		}
	}

	return result, nil
}

func getUsedPVC(ctx context.Context, cl client.Client, log logger.Logger, pod *corev1.Pod) (map[string]*corev1.PersistentVolumeClaim, error) {
	pvcMap, err := getAllPVCsFromNamespace(ctx, cl, pod.Namespace)
	if err != nil {
		log.Error(err, fmt.Sprintf("[getUsedPVC] unable to get all PVC for Pod %s in the namespace %s", pod.Name, pod.Namespace))
		return nil, err
	}

	for pvcName := range pvcMap {
		log.Trace(fmt.Sprintf("[getUsedPVC] PVC %s is in namespace %s", pod.Namespace, pvcName))
	}

	usedPvc := make(map[string]*corev1.PersistentVolumeClaim, len(pod.Spec.Volumes))
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			log.Trace(fmt.Sprintf("[getUsedPVC] Pod %s/%s uses PVC %s", pod.Namespace, pod.Name, volume.PersistentVolumeClaim.ClaimName))
			pvc := pvcMap[volume.PersistentVolumeClaim.ClaimName]
			usedPvc[volume.PersistentVolumeClaim.ClaimName] = &pvc
		}
	}

	return usedPvc, err
}

func getAllPVCsFromNamespace(ctx context.Context, cl client.Client, namespace string) (map[string]corev1.PersistentVolumeClaim, error) {
	list := &corev1.PersistentVolumeClaimList{}
	err := cl.List(ctx, list, &client.ListOptions{Namespace: namespace})
	if err != nil {
		return nil, err
	}

	pvcs := make(map[string]corev1.PersistentVolumeClaim, len(list.Items))
	for _, pvc := range list.Items {
		pvcs[pvc.Name] = pvc
	}

	return pvcs, nil
}
