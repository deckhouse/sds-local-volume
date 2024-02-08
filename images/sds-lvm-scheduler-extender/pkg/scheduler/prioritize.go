package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"math"
	"net/http"
	"sds-lvm-scheduler-extender/pkg/logger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sync"
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

	scLVGs, err := getLVGsFromStorageClasses(scs)
	if err != nil {
		return nil, err
	}

	usedLVGs := removeUnusedLVGs(lvgs, scLVGs)

	nodeLVGs := sortLVGsByNodeName(usedLVGs)
	for n, ls := range nodeLVGs {
		for _, l := range ls {
			log.Trace(fmt.Sprintf("[filterNodes] the LVMVolumeGroup %s belongs to node %s", l.Name, n))
		}
	}

	result := make([]HostPriority, 0, len(nodes.Items))
	wg := &sync.WaitGroup{}
	wg.Add(len(nodes.Items))
	errs := make(chan error, len(pvcs)*len(nodes.Items))

	for i, node := range nodes.Items {
		go func(i int, node corev1.Node) {
			log.Debug(fmt.Sprintf("[filterNodes] gourutine %d starts the work", i))
			defer func() {
				log.Debug(fmt.Sprintf("[filterNodes] gourutine %d ends the work", i))
				wg.Done()
			}()

			lvgsFromNode := nodeLVGs[node.Name]
			var totalFreeSpaceLeft int64
			for _, pvc := range pvcs {
				pvcReq := pvcRequests[pvc.Name]
				lvgsFromSC := scLVGs[*pvc.Spec.StorageClassName]
				matchedLVG := findMatchedLVG(lvgsFromNode, lvgsFromSC)
				if matchedLVG == nil {
					err = errors.New(fmt.Sprintf("unable to match Storage Class's LVMVolumeGroup with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, node.Name))
					errs <- err
					return
				}

				switch pvcReq.DeviceType {
				case thick:
					lvg := lvgs[matchedLVG.Name]
					freeSpace, err := getVGFreeSpace(&lvg)
					if err != nil {
						errs <- err
						return
					}

					totalFreeSpaceLeft = getFreeSpaceLeftPercent(freeSpace.Value(), pvcReq.RequestedSize)
				case thin:
					lvg := lvgs[matchedLVG.Name]
					thinPool := findMatchedThinPool(lvg.Status.ThinPools, matchedLVG.Thin.PoolName)
					if thinPool == nil {
						err = errors.New(fmt.Sprintf("unable to match Storage Class's ThinPools with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, node.Name))
						log.Error(err, "an error occurs while searching for target LVMVolumeGroup")
						errs <- err
						return
					}

					freeSpace, err := getThinPoolFreeSpace(thinPool)
					if err != nil {
						errs <- err
						return
					}

					totalFreeSpaceLeft = getFreeSpaceLeftPercent(freeSpace.Value(), pvcReq.RequestedSize)
				}
			}

			averageFreeSpace := totalFreeSpaceLeft / int64(len(pvcs))
			score := getNodeScore(averageFreeSpace, divisor)
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
	converted := int(math.Log2(float64(freeSpace) / divisor))
	switch {
	case converted < 1:
		return 1
	case converted > 10:
		return 10
	default:
		return converted
	}
}
