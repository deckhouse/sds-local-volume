/*
Copyright 2024 Flant JSC

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
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/strings/slices"
	"net/http"
	"sds-local-volume-scheduler-extender/api/v1alpha1"
	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
	"sync"
	"time"
)

const (
	lvmTypeParamKey         = "local.csi.storage.deckhouse.io/lvm-type"
	lvmVolumeGroupsParamKey = "local.csi.storage.deckhouse.io/lvm-volume-groups"

	thick = "Thick"
	thin  = "Thin"
)

func (s *scheduler) filter(w http.ResponseWriter, r *http.Request) {
	s.log.Debug("[filter] starts the serving")
	var input ExtenderArgs
	reader := http.MaxBytesReader(w, r.Body, 10<<20)
	err := json.NewDecoder(reader).Decode(&input)
	if err != nil || input.Nodes == nil || input.Pod == nil {
		s.log.Error(err, "[filter] unable to decode a request")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, n := range input.Nodes.Items {
		s.log.Trace(fmt.Sprintf("[filter] a node from request, name :%s", n.Name))
	}

	pvcs, err := getUsedPVC(s.ctx, s.client, s.log, input.Pod)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[filter] unable to get used PVC for a Pod %s in the namespace %s", input.Pod.Name, input.Pod.Namespace))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if len(pvcs) == 0 {
		s.log.Error(fmt.Errorf("no PVC was found for pod %s in namespace %s", input.Pod.Name, input.Pod.Namespace), fmt.Sprintf("[filter] unable to get used PVC for Pod %s", input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, pvc := range pvcs {
		s.log.Trace(fmt.Sprintf("[filter] Pod %s/%s used PVC: %s", input.Pod.Namespace, input.Pod.Name, pvc.Name))
	}

	scs, err := getStorageClassesUsedByPVCs(s.ctx, s.client, pvcs)
	if err != nil {
		s.log.Error(err, "[filter] unable to get StorageClasses from the PVC")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, sc := range scs {
		s.log.Trace(fmt.Sprintf("[filter] used StorageClasses: %s", sc.Name))
	}

	s.log.Debug("[filter] starts to extract pvcRequests size")
	pvcRequests, err := extractRequestedSize(s.ctx, s.client, s.log, pvcs, scs)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[filter] unable to extract request size for a pod %s", input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Debug("[filter] successfully extracted the pvcRequests size")

	s.log.Debug("[filter] starts to filter the nodes")
	filteredNodes, err := filterNodes(s.log, s.cache, input.Nodes, pvcs, scs, pvcRequests)
	if err != nil {
		s.log.Error(err, "[filter] unable to filter the nodes")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Debug("[filter] successfully filtered the nodes")

	s.log.Debug("[filter] starts to populate the cache")
	s.cache.PrintTheCacheTraceLog()
	err = populateCache(s.log, filteredNodes.Nodes.Items, input.Pod, s.cache, pvcs, scs)
	if err != nil {
		s.log.Error(err, "[filter] unable to populate cache")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Debug("[filter] successfully populated the cache")
	s.cache.PrintTheCacheTraceLog()

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(filteredNodes)
	if err != nil {
		s.log.Error(err, "[filter] unable to encode a response")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.log.Debug("[filter] ends the serving")
}

func populateCache(log logger.Logger, nodes []corev1.Node, pod *corev1.Pod, schedulerCache *cache.Cache, pvcs map[string]*corev1.PersistentVolumeClaim, scs map[string]*v1.StorageClass) error {
	for _, node := range nodes {
		log.Debug(fmt.Sprintf("[populateCache] starts the work for node %s", node.Name))
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				log.Debug(fmt.Sprintf("[populateCache] reconcile the PVC %s for Pod %s/%s", volume.PersistentVolumeClaim.ClaimName, pod.Namespace, pod.Name))
				lvgNamesForTheNode := schedulerCache.GetLVGNamesByNodeName(node.Name)
				log.Trace(fmt.Sprintf("[populateCache] LVMVolumeGroups from cache for the node %s: %v", node.Name, lvgNamesForTheNode))
				pvc := pvcs[volume.PersistentVolumeClaim.ClaimName]
				sc := scs[*pvc.Spec.StorageClassName]

				if sc.Parameters[lvmTypeParamKey] == thick {
					log.Debug(fmt.Sprintf("[populateCache] Storage Class %s has device type Thick, so the cache will be populated by PVC space requests", sc.Name))
					lvgsForPVC, err := ExtractLVGsFromSC(sc)
					if err != nil {
						return err
					}

					log.Trace(fmt.Sprintf("[populateCache] LVMVolumeGroups from Storage Class %s for PVC %s: %+v", sc.Name, volume.PersistentVolumeClaim.ClaimName, lvgsForPVC))
					for _, lvg := range lvgsForPVC {
						if slices.Contains(lvgNamesForTheNode, lvg.Name) {
							log.Trace(fmt.Sprintf("[populateCache] PVC %s will reserve space in LVMVolumeGroup %s cache", volume.PersistentVolumeClaim.ClaimName, lvg.Name))
							err = schedulerCache.AddPVCToLVG(lvg.Name, pvcs[volume.PersistentVolumeClaim.ClaimName])
							if err != nil {
								return err
							}
						}
					}
				} else {
					log.Debug(fmt.Sprintf("[populateCache] Storage Class %s has device type Thin, so the cache should NOT be populated by PVC space requests", sc.Name))
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
		switch pvc.Status.Phase {
		case corev1.ClaimPending:
			switch sc.Parameters[lvmTypeParamKey] {
			case thick:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    thick,
					RequestedSize: pvc.Spec.Resources.Requests.Storage().Value(),
				}
			case thin:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    thin,
					RequestedSize: pvc.Spec.Resources.Requests.Storage().Value(),
				}
			}

		case corev1.ClaimBound:
			pv := pvs[pvc.Spec.VolumeName]
			switch sc.Parameters[lvmTypeParamKey] {
			case thick:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    thick,
					RequestedSize: pvc.Spec.Resources.Requests.Storage().Value() - pv.Spec.Capacity.Storage().Value(),
				}
			case thin:
				pvcRequests[pvc.Name] = PVCRequest{
					DeviceType:    thin,
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
	nodes *corev1.NodeList,
	pvcs map[string]*corev1.PersistentVolumeClaim,
	scs map[string]*v1.StorageClass,
	pvcRequests map[string]PVCRequest,
) (*ExtenderFilterResult, error) {
	// Param "pvcRequests" is a total amount of the pvcRequests space (both thick and thin) for Pod (i.e. from every PVC)
	if len(pvcRequests) == 0 {
		return &ExtenderFilterResult{
			Nodes: nodes,
		}, nil
	}

	lvgs := schedulerCache.GetAllLVG()
	for _, lvg := range lvgs {
		log.Trace(fmt.Sprintf("[filterNodes] LVMVolumeGroup %s in the cache", lvg.Name))
	}

	log.Debug("[filterNodes] starts to get LVMVolumeGroups for Storage Classes")
	scLVGs, err := GetSortedLVGsFromStorageClasses(scs)
	if err != nil {
		return nil, err
	}
	log.Debug("[filterNodes] successfully got LVMVolumeGroups for Storage Classes")
	for scName, sortedLVGs := range scLVGs {
		for _, lvg := range sortedLVGs {
			log.Trace(fmt.Sprintf("[filterNodes] LVMVolumeGroup %s belongs to Storage Class %s", lvg.Name, scName))
		}
	}

	usedLVGs := RemoveUnusedLVGs(lvgs, scLVGs)
	for _, lvg := range usedLVGs {
		log.Trace(fmt.Sprintf("[filterNodes] the LVMVolumeGroup %s is actually used", lvg.Name))
	}
	lvgsThickFree, err := getLVGThickFreeSpaces(usedLVGs)
	if err != nil {
		return nil, err
	}
	log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroups Thick FreeSpace on the node: %+v", lvgsThickFree))

	for lvgName, freeSpace := range lvgsThickFree {
		log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroup %s Thick free space %s", lvgName, resource.NewQuantity(freeSpace, resource.BinarySI)))
		reservedSize, err := schedulerCache.GetLVGReservedSpace(lvgName)
		if err != nil {
			log.Error(err, fmt.Sprintf("[filterNodes] unable to cound cache reserved size for the LVMVolumeGroup %s", lvgName))
			continue
		}
		log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroup %s reserved PVC space %s", lvgName, resource.NewQuantity(reservedSize, resource.BinarySI)))
		lvgsThickFree[lvgName] -= reservedSize
	}
	log.Trace(fmt.Sprintf("[filterNodes] current LVMVolumeGroups Thick FreeSpace with reserved PVC: %+v", lvgsThickFree))

	lvgsThickFreeMutex := &sync.RWMutex{}

	nodeLVGs := SortLVGsByNodeName(usedLVGs)
	for n, ls := range nodeLVGs {
		for _, l := range ls {
			log.Trace(fmt.Sprintf("[filterNodes] the LVMVolumeGroup %s belongs to node %s", l.Name, n))
		}
	}

	commonNodes, err := getCommonNodesByStorageClasses(scs, nodeLVGs)
	for nodeName := range commonNodes {
		log.Trace(fmt.Sprintf("[filterNodes] Node %s is a common for every storage class", nodeName))
	}

	result := &ExtenderFilterResult{
		Nodes:       &corev1.NodeList{},
		FailedNodes: FailedNodesMap{},
	}
	failedNodesMutex := &sync.Mutex{}

	wg := &sync.WaitGroup{}
	wg.Add(len(nodes.Items))
	errs := make(chan error, len(nodes.Items)*len(pvcs))

	for i, node := range nodes.Items {
		go func(i int, node corev1.Node) {
			log.Debug(fmt.Sprintf("[filterNodes] gourutine %d starts the work", i))
			defer func() {
				log.Debug(fmt.Sprintf("[filterNodes] gourutine %d ends the work", i))
				wg.Done()
			}()

			if _, common := commonNodes[node.Name]; !common {
				log.Debug(fmt.Sprintf("[filterNodes] node %s is not common for used Storage Classes", node.Name))
				failedNodesMutex.Lock()
				result.FailedNodes[node.Name] = "node is not common for used Storage Classes"
				failedNodesMutex.Unlock()
				return
			}

			lvgsFromNode := commonNodes[node.Name]
			hasEnoughSpace := true

			for _, pvc := range pvcs {
				pvcReq := pvcRequests[pvc.Name]
				lvgsFromSC := scLVGs[*pvc.Spec.StorageClassName]
				commonLVG := findMatchedLVG(lvgsFromNode, lvgsFromSC)
				if commonLVG == nil {
					err = errors.New(fmt.Sprintf("unable to match Storage Class's LVMVolumeGroup with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, node.Name))
					errs <- err
					return
				}
				log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s is common for storage class %s and node %s", commonLVG.Name, *pvc.Spec.StorageClassName, node.Name))

				switch pvcReq.DeviceType {
				case thick:
					lvg := lvgs[commonLVG.Name]
					lvgsThickFreeMutex.RLock()
					freeSpace := lvgsThickFree[lvg.Name]
					lvgsThickFreeMutex.RUnlock()

					log.Trace(fmt.Sprintf("[filterNodes] LVMVolumeGroup %s Thick free space: %s, PVC requested space: %s", lvg.Name, resource.NewQuantity(freeSpace, resource.BinarySI), resource.NewQuantity(pvcReq.RequestedSize, resource.BinarySI)))
					if freeSpace < pvcReq.RequestedSize {
						hasEnoughSpace = false
						break
					}

					lvgsThickFreeMutex.Lock()
					lvgsThickFree[lvg.Name] -= pvcReq.RequestedSize
					lvgsThickFreeMutex.Unlock()
				case thin:
					lvg := lvgs[commonLVG.Name]
					targetThinPool := findMatchedThinPool(lvg.Status.ThinPools, commonLVG.Thin.PoolName)
					if targetThinPool == nil {
						err = fmt.Errorf("unable to match Storage Class's ThinPools with the node's one, Storage Class: %s; node: %s; lvg thin pools: %+v; thin.poolName from StorageClass: %s", *pvc.Spec.StorageClassName, node.Name, lvg.Status.ThinPools, commonLVG.Thin.PoolName)
						errs <- err
						return
					}
					// TODO: add after overCommit implementation
					// freeSpace, err := getThinPoolFreeSpace(targetThinPool)
					// if err != nil {
					// 	errs <- err
					// 	return
					// }

					// log.Trace(fmt.Sprintf("[filterNodes] ThinPool free space: %d, PVC requested space: %d", freeSpace.Value(), pvcReq.RequestedSize))

					// if freeSpace.Value() < pvcReq.RequestedSize {
					// 	hasEnoughSpace = false
					// }
				}

				if !hasEnoughSpace {
					break
				}
			}

			if !hasEnoughSpace {
				failedNodesMutex.Lock()
				result.FailedNodes[node.Name] = "not enough space"
				failedNodesMutex.Unlock()
				return
			}

			result.Nodes.Items = append(result.Nodes.Items, node)
		}(i, node)
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
		return nil, err
	}

	for _, node := range result.Nodes.Items {
		log.Trace(fmt.Sprintf("[filterNodes] suitable node: %s", node.Name))
	}

	for node, reason := range result.FailedNodes {
		log.Trace(fmt.Sprintf("[filterNodes] failed node: %s, reason: %s", node, reason))
	}

	return result, nil
}

func getLVGThickFreeSpaces(lvgs map[string]*v1alpha1.LvmVolumeGroup) (map[string]int64, error) {
	result := make(map[string]int64, len(lvgs))

	for _, lvg := range lvgs {
		free, err := getVGFreeSpace(lvg)
		if err != nil {
			return nil, err
		}

		result[lvg.Name] = free.Value()
	}

	return result, nil
}

func findMatchedThinPool(thinPools []v1alpha1.StatusThinPool, name string) *v1alpha1.StatusThinPool {
	for _, tp := range thinPools {
		if tp.Name == name {
			return &tp
		}
	}

	return nil
}

func findMatchedLVG(nodeLVGs []*v1alpha1.LvmVolumeGroup, scLVGs LVMVolumeGroups) *LVMVolumeGroup {
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

func getCommonNodesByStorageClasses(scs map[string]*v1.StorageClass, nodesWithLVGs map[string][]*v1alpha1.LvmVolumeGroup) (map[string][]*v1alpha1.LvmVolumeGroup, error) {
	result := make(map[string][]*v1alpha1.LvmVolumeGroup, len(nodesWithLVGs))

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

func RemoveUnusedLVGs(lvgs map[string]*v1alpha1.LvmVolumeGroup, scsLVGs map[string]LVMVolumeGroups) map[string]*v1alpha1.LvmVolumeGroup {
	result := make(map[string]*v1alpha1.LvmVolumeGroup, len(lvgs))
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

		for _, lvg := range lvgs {
			result[sc.Name] = append(result[sc.Name], lvg)
		}
	}

	return result, nil
}

type LVMVolumeGroup struct {
	Name string `yaml:"name"`
	Thin struct {
		PoolName string `yaml:"poolName"`
	} `yaml:"thin"`
}
type LVMVolumeGroups []LVMVolumeGroup

func ExtractLVGsFromSC(sc *v1.StorageClass) (LVMVolumeGroups, error) {
	var lvmVolumeGroups LVMVolumeGroups
	err := yaml.Unmarshal([]byte(sc.Parameters[lvmVolumeGroupsParamKey]), &lvmVolumeGroups)
	if err != nil {
		return nil, err
	}
	return lvmVolumeGroups, nil
}

func SortLVGsByNodeName(lvgs map[string]*v1alpha1.LvmVolumeGroup) map[string][]*v1alpha1.LvmVolumeGroup {
	sorted := make(map[string][]*v1alpha1.LvmVolumeGroup, len(lvgs))
	for _, lvg := range lvgs {
		for _, node := range lvg.Status.Nodes {
			sorted[node.Name] = append(sorted[node.Name], lvg)
		}
	}

	return sorted
}

func getVGFreeSpace(lvg *v1alpha1.LvmVolumeGroup) (resource.Quantity, error) {
	free, err := resource.ParseQuantity(lvg.Status.VGSize)
	if err != nil {
		return resource.Quantity{}, err
	}

	used, err := resource.ParseQuantity(lvg.Status.AllocatedSize)
	if err != nil {
		return resource.Quantity{}, err
	}

	free.Sub(used)
	return free, nil
}

func getThinPoolFreeSpace(tp *v1alpha1.StatusThinPool) (resource.Quantity, error) {
	free := tp.ActualSize
	used, err := resource.ParseQuantity(tp.UsedSize)
	if err != nil {
		return resource.Quantity{}, err
	}
	free.Sub(used)

	return free, nil
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
			err = errors.New(fmt.Sprintf("not StorageClass specified for PVC %s", pvc.Name))
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
	usedPvc := make(map[string]*corev1.PersistentVolumeClaim, len(pod.Spec.Volumes))

	var err error
	for {
		pvcMap, err := getAllPVCsFromNamespace(ctx, cl, pod.Namespace)
		if err != nil {
			log.Error(err, fmt.Sprintf("[getUsedPVC] unable to get all PVC for Pod %s in the namespace %s", pod.Name, pod.Namespace))
			return nil, err
		}

		for pvcName := range pvcMap {
			log.Trace(fmt.Sprintf("[getUsedPVC] PVC %s is in namespace %s", pod.Namespace, pvcName))
		}

		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				log.Trace(fmt.Sprintf("[getUsedPVC] Pod %s uses PVC %s", pod.Name, volume.PersistentVolumeClaim.ClaimName))
				usedPvc[volume.PersistentVolumeClaim.ClaimName] = pvcMap[volume.PersistentVolumeClaim.ClaimName]
			}
		}

		filled := false
		if len(pvcMap) > 0 {
			filled = true
			for _, volume := range pod.Spec.Volumes {
				if volume.PersistentVolumeClaim != nil {
					if _, added := usedPvc[volume.PersistentVolumeClaim.ClaimName]; !added {
						filled = false
						log.Warning(fmt.Sprintf("[getUsedPVC] PVC %s was not found in the cache for Pod %s", volume.PersistentVolumeClaim.ClaimName, pod.Name))
						break
					}
				}
			}
		}

		if !filled {
			log.Warning(fmt.Sprintf("[getUsedPVC] some PVCs were not found in the cache for Pod %s. Retry to find them again.", pod.Name))
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if filled {
			log.Debug(fmt.Sprintf("[getUsedPVC] Every PVC for Pod %s was found in the cache", pod.Name))
			break
		}
	}

	return usedPvc, err
}

func getAllPVCsFromNamespace(ctx context.Context, cl client.Client, namespace string) (map[string]*corev1.PersistentVolumeClaim, error) {
	list := &corev1.PersistentVolumeClaimList{}
	err := cl.List(ctx, list, &client.ListOptions{Namespace: namespace})
	if err != nil {
		return nil, err
	}

	pvcs := make(map[string]*corev1.PersistentVolumeClaim, len(list.Items))
	for _, pvc := range list.Items {
		pvcs[pvc.Name] = &pvc
	}

	return pvcs, nil
}
