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
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sds-local-volume-scheduler-extender/pkg/cache"
	"sds-local-volume-scheduler-extender/pkg/consts"
	"sds-local-volume-scheduler-extender/pkg/logger"
)

func (s *scheduler) prioritize(w http.ResponseWriter, r *http.Request) {
	s.log.Debug("[prioritize] starts serving")
	var input ExtenderArgs
	reader := http.MaxBytesReader(w, r.Body, 10<<20)
	err := json.NewDecoder(reader).Decode(&input)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to decode a request")
		http.Error(w, "Bad Request.", http.StatusBadRequest)
		return
	}

	s.log.Debug(fmt.Sprintf("[prioritize] starts the prioritizing for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))

	pvcs, err := getUsedPVC(s.ctx, s.client, s.log, input.Pod)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[prioritize] unable to get PVC from the Pod %s/%s", input.Pod.Namespace, input.Pod.Name))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if len(pvcs) == 0 {
		s.log.Error(fmt.Errorf("no PVC was found for pod %s in namespace %s", input.Pod.Name, input.Pod.Namespace), fmt.Sprintf("[prioritize] unable to get used PVC for Pod %s", input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, pvc := range pvcs {
		s.log.Trace(fmt.Sprintf("[prioritize] Pod %s/%s uses PVC: %s", input.Pod.Namespace, input.Pod.Name, pvc.Name))
	}

	scs, err := getStorageClassesUsedByPVCs(s.ctx, s.client, pvcs)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[prioritize] unable to get StorageClasses from the PVC for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, sc := range scs {
		s.log.Trace(fmt.Sprintf("[prioritize] Pod %s/%s uses Storage Class: %s", input.Pod.Namespace, input.Pod.Name, sc.Name))
	}

	managedPVCs := filterNotManagedPVC(s.log, pvcs, scs)
	for _, pvc := range managedPVCs {
		s.log.Trace(fmt.Sprintf("[prioritize] filtered managed PVC %s/%s", pvc.Namespace, pvc.Name))
	}

	s.log.Debug(fmt.Sprintf("[prioritize] starts to extract pvcRequests size for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))
	pvcRequests, err := extractRequestedSize(s.ctx, s.client, s.log, managedPVCs, scs)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[prioritize] unable to extract request size for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	s.log.Debug(fmt.Sprintf("[prioritize] successfully extracted the pvcRequests size for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))

	s.log.Debug(fmt.Sprintf("[prioritize] starts to score the nodes for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))
	result, err := scoreNodes(s.log, s.cache, input.Nodes, managedPVCs, scs, pvcRequests, s.defaultDivisor)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[prioritize] unable to score nodes for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.log.Debug(fmt.Sprintf("[prioritize] successfully scored the nodes for Pod %s/%s", input.Pod.Namespace, input.Pod.Name))

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[prioritize] unable to encode a response for a Pod %s/%s", input.Pod.Namespace, input.Pod.Name))
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	s.log.Debug("[prioritize] ends serving")
}

func scoreNodes(
	log logger.Logger,
	schedulerCache *cache.Cache,
	nodes *corev1.NodeList,
	pvcs map[string]*corev1.PersistentVolumeClaim,
	scs map[string]*v1.StorageClass,
	pvcRequests map[string]PVCRequest,
	divisor float64,
) ([]HostPriority, error) {
	lvgs := schedulerCache.GetAllLVG()
	scLVGs, err := GetSortedLVGsFromStorageClasses(scs)
	if err != nil {
		return nil, err
	}

	usedLVGs := RemoveUnusedLVGs(lvgs, scLVGs)
	for lvgName := range usedLVGs {
		log.Trace(fmt.Sprintf("[scoreNodes] used LVMVolumeGroup %s", lvgName))
	}

	nodeLVGs := SortLVGsByNodeName(usedLVGs)
	for n, ls := range nodeLVGs {
		for _, l := range ls {
			log.Trace(fmt.Sprintf("[scoreNodes] the LVMVolumeGroup %s belongs to node %s", l.Name, n))
		}
	}

	result := make([]HostPriority, 0, len(nodes.Items))
	wg := &sync.WaitGroup{}
	wg.Add(len(nodes.Items))
	errs := make(chan error, len(pvcs)*len(nodes.Items))

	for i, node := range nodes.Items {
		go func(i int, node corev1.Node) {
			log.Debug(fmt.Sprintf("[scoreNodes] gourutine %d starts the work", i))
			defer func() {
				log.Debug(fmt.Sprintf("[scoreNodes] gourutine %d ends the work", i))
				wg.Done()
			}()

			lvgsFromNode := nodeLVGs[node.Name]
			var totalFreeSpaceLeft int64
			for _, pvc := range pvcs {
				pvcReq := pvcRequests[pvc.Name]
				lvgsFromSC := scLVGs[*pvc.Spec.StorageClassName]
				commonLVG := findMatchedLVG(lvgsFromNode, lvgsFromSC)
				if commonLVG == nil {
					err = fmt.Errorf("unable to match Storage Class's LVMVolumeGroup with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, node.Name)
					errs <- err
					return
				}
				log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s is common for storage class %s and node %s", commonLVG.Name, *pvc.Spec.StorageClassName, node.Name))

				var freeSpace resource.Quantity
				lvg := lvgs[commonLVG.Name]
				switch pvcReq.DeviceType {
				case consts.Thick:
					freeSpace = lvg.Status.VGFree
					log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s free Thick space before PVC reservation: %s", lvg.Name, freeSpace.String()))
					reserved, err := schedulerCache.GetLVGThickReservedSpace(lvg.Name)
					if err != nil {
						log.Error(err, fmt.Sprintf("[scoreNodes] unable to count reserved space for the LVMVolumeGroup %s", lvg.Name))
						continue
					}
					log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s PVC Space reservation: %s", lvg.Name, resource.NewQuantity(reserved, resource.BinarySI)))
					spaceWithReserved := freeSpace.Value() - reserved
					freeSpace = *resource.NewQuantity(spaceWithReserved, resource.BinarySI)
					log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s free Thick space after PVC reservation: %s", lvg.Name, freeSpace.String()))
				case consts.Thin:
					thinPool := findMatchedThinPool(lvg.Status.ThinPools, commonLVG.Thin.PoolName)
					if thinPool == nil {
						err = fmt.Errorf("unable to match Storage Class's ThinPools with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, node.Name)
						log.Error(err, "[scoreNodes] an error occurs while searching for target LVMVolumeGroup")
						errs <- err
						return
					}

					freeSpace = thinPool.AvailableSpace
				}

				log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s total size: %s", lvg.Name, lvg.Status.VGSize.String()))
				totalFreeSpaceLeft += getFreeSpaceLeftPercent(freeSpace.Value(), pvcReq.RequestedSize, lvg.Status.VGSize.Value())
			}

			averageFreeSpace := totalFreeSpaceLeft / int64(len(pvcs))
			log.Trace(fmt.Sprintf("[scoreNodes] average free space left for the node: %s", node.Name))
			score := getNodeScore(averageFreeSpace, divisor)
			log.Trace(fmt.Sprintf("[scoreNodes] node %s has score %d with average free space left (after all PVC bounded), percent %d", node.Name, score, averageFreeSpace))

			result = append(result, HostPriority{
				Host:  node.Name,
				Score: score,
			})
		}(i, node)
	}
	wg.Wait()

	if len(errs) != 0 {
		for err = range errs {
			log.Error(err, "[scoreNodes] an error occurs while scoring the nodes")
		}
	}
	close(errs)
	if err != nil {
		return nil, err
	}

	log.Trace("[scoreNodes] final result")
	for _, n := range result {
		log.Trace(fmt.Sprintf("[scoreNodes] host: %s", n.Host))
		log.Trace(fmt.Sprintf("[scoreNodes] score: %d", n.Score))
	}

	return result, nil
}

func getFreeSpaceLeftPercent(freeSize, requestedSpace, totalSize int64) int64 {
	leftFreeSize := freeSize - requestedSpace
	fraction := float64(leftFreeSize) / float64(totalSize)
	percent := fraction * 100
	return int64(percent)
}

func getNodeScore(freeSpace int64, divisor float64) int {
	converted := int(math.Round(math.Log2(float64(freeSpace) / divisor)))
	switch {
	case converted < 1:
		return 1
	case converted > 10:
		return 10
	default:
		return converted
	}
}
