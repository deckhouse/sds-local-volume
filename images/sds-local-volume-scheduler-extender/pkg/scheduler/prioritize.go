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
	"math"
	"net/http"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sync"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s scheduler) prioritize(w http.ResponseWriter, r *http.Request) {
	s.log.Debug("[prioritize] starts serving")
	var input ExtenderArgs
	reader := http.MaxBytesReader(w, r.Body, 10<<20)
	err := json.NewDecoder(reader).Decode(&input)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to decode a request")
		http.Error(w, "Bad Request.", http.StatusBadRequest)
		return
	}

	pvcs, err := getUsedPVC(s.ctx, s.client, input.Pod)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to get PVC from the Pod")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, pvc := range pvcs {
		s.log.Trace(fmt.Sprintf("[prioritize] used PVC: %s", pvc.Name))
	}

	scs, err := getStorageClassesUsedByPVCs(s.ctx, s.client, pvcs)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to get StorageClasses from the PVC")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, sc := range scs {
		s.log.Trace(fmt.Sprintf("[prioritize] used StorageClasses: %s", sc.Name))
	}

	s.log.Debug("[prioritize] starts to extract pvcRequests size")
	pvcRequests, err := extractRequestedSize(s.ctx, s.client, s.log, pvcs, scs)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[filter] unable to extract request size for a pod %s", input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	s.log.Debug("[filter] successfully extracted the pvcRequests size")

	s.log.Debug("[prioritize] starts to score the nodes")
	result, err := scoreNodes(s.ctx, s.client, s.log, input.Nodes, pvcs, scs, pvcRequests, s.defaultDivisor)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to score nodes")
		http.Error(w, "Bad Request.", http.StatusBadRequest)
		return
	}
	s.log.Debug("[prioritize] successfully scored the nodes")

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to encode a response")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	s.log.Debug("[prioritize] ends serving")
}

func scoreNodes(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	nodes *corev1.NodeList,
	pvcs map[string]corev1.PersistentVolumeClaim,
	scs map[string]v1.StorageClass,
	pvcRequests map[string]PVCRequest,
	divisor float64,
) ([]HostPriority, error) {
	lvgs, err := getLVMVolumeGroups(ctx, cl)
	if err != nil {
		return nil, err
	}

	scLVGs, err := getSortedLVGsFromStorageClasses(scs)
	if err != nil {
		return nil, err
	}

	usedLVGs := removeUnusedLVGs(lvgs, scLVGs)
	for lvgName := range usedLVGs {
		log.Trace(fmt.Sprintf("[scoreNodes] used LVMVolumeGroup %s", lvgName))
	}

	nodeLVGs := sortLVGsByNodeName(usedLVGs)
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
			// TODO: change pvs to vgs
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
					freeSpace, err := getVGFreeSpace(&lvg)
					if err != nil {
						errs <- err
						return
					}
					log.Trace(fmt.Sprintf("[scoreNodes] LVMVolumeGroup %s free thick space %s", lvg.Name, freeSpace.String()))
					totalFreeSpaceLeft += getFreeSpaceLeftPercent(freeSpace.Value(), pvcReq.RequestedSize)
				case thin:
					lvg := lvgs[commonLVG.Name]
					thinPool := findMatchedThinPool(lvg.Status.ThinPools, commonLVG.Thin.PoolName)
					if thinPool == nil {
						err = errors.New(fmt.Sprintf("unable to match Storage Class's ThinPools with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, node.Name))
						log.Error(err, "[scoreNodes] an error occurs while searching for target LVMVolumeGroup")
						errs <- err
						return
					}

					freeSpace, err := getThinPoolFreeSpace(thinPool)
					if err != nil {
						errs <- err
						return
					}

					totalFreeSpaceLeft += getFreeSpaceLeftPercent(freeSpace.Value(), pvcReq.RequestedSize)
				}
			}

			averageFreeSpace := totalFreeSpaceLeft / int64(len(pvcs))
			score := getNodeScore(averageFreeSpace, divisor)
			log.Trace(fmt.Sprintf("[scoreNodes] node %s has score %d with average free space percent %d", node.Name, score, averageFreeSpace))

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

func getFreeSpaceLeftPercent(freeSpace int64, requestedSpace int64) int64 {
	left := freeSpace - requestedSpace
	return left * 100 / freeSpace
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
