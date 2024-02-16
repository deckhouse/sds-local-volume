package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sds-lvm-scheduler-extender/api/v1alpha1"
	"sds-lvm-scheduler-extender/pkg/logger"
	"sync"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	lvmTypeParamKey         = "lvm.csi.storage.deckhouse.io/lvm-type"
	lvmVolumeGroupsParamKey = "lvm.csi.storage.deckhouse.io/lvm-volume-groups"

	thick = "Thick"
	thin  = "Thin"
)

func (s scheduler) filter(w http.ResponseWriter, r *http.Request) {
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

	pvcs, err := getUsedPVC(s.ctx, s.client, input.Pod)
	if err != nil {
		s.log.Error(err, "[filter] unable to get PVC from the Pod")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, pvc := range pvcs {
		s.log.Trace(fmt.Sprintf("[filter] used PVC: %s", pvc.Name))
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
	}
	s.log.Debug("[filter] successfully extracted the pvcRequests size")

	s.log.Debug("[filter] starts to filter the nodes")
	result, err := filterNodes(s.ctx, s.client, s.log, input.Nodes, pvcs, scs, pvcRequests)
	if err != nil {
		s.log.Error(err, "[filter] unable to filter the nodes")
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	s.log.Debug("[filter] successfully filtered the nodes")

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		s.log.Error(err, "[filter] unable to encode a response")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	s.log.Debug("[filter] ends the serving")
}

type PVCRequest struct {
	DeviceType    string
	RequestedSize int64
}

func extractRequestedSize(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	pvcs map[string]corev1.PersistentVolumeClaim,
	scs map[string]v1.StorageClass,
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
					DeviceType:    thick,
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
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	nodes *corev1.NodeList,
	pvcs map[string]corev1.PersistentVolumeClaim,
	scs map[string]v1.StorageClass,
	pvcRequests map[string]PVCRequest,
) (*ExtenderFilterResult, error) {
	// Param "pvcRequests" is a total amount of the pvcRequests space (both thick and thin) for Pod (i.e. from every PVC)
	if len(pvcRequests) == 0 {
		return &ExtenderFilterResult{
			Nodes: nodes,
		}, nil
	}

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

	generalNodes, err := getGeneralNodesByStorageClasses(scs, nodeLVGs)

	result := &ExtenderFilterResult{
		Nodes:       &corev1.NodeList{},
		FailedNodes: FailedNodesMap{},
	}

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

			if _, general := generalNodes[node.Name]; !general {
				log.Debug(fmt.Sprintf("[filterNodes] node %s is not general for used Storage Classes", node.Name))
				result.FailedNodes[node.Name] = "node is not general for used Storage Classes"
				return
			}

			lvgsFromNode := generalNodes[node.Name]
			hasEnoughSpace := true

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

					log.Trace(fmt.Sprintf("[filterNodes] ThinPool free space: %d, PVC requested space: %d", freeSpace.Value(), pvcReq.RequestedSize))
					if freeSpace.Value() < pvcReq.RequestedSize {
						hasEnoughSpace = false
					}

				case thin:
					lvg := lvgs[matchedLVG.Name]
					targetThinPool := findMatchedThinPool(lvg.Status.ThinPools, matchedLVG.Thin.PoolName)
					if targetThinPool == nil {
						err = errors.New(fmt.Sprintf("unable to match Storage Class's ThinPools with the node's one, Storage Class: %s, node: %s", *pvc.Spec.StorageClassName, node.Name))
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
				result.FailedNodes[node.Name] = "not enough space"
				return
			}

			// TODO: add logic to filter nodes when pvcs has same storage class

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

func findMatchedThinPool(thinPools []v1alpha1.StatusThinPool, name string) *v1alpha1.StatusThinPool {
	for _, tp := range thinPools {
		if tp.Name == name {
			return &tp
		}
	}

	return nil
}

func findMatchedLVG(nodeLVGs []v1alpha1.LvmVolumeGroup, scLVGs LVMVolumeGroups) *LVMVolumeGroup {
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

func getGeneralNodesByStorageClasses(scs map[string]v1.StorageClass, nodesWithLVGs map[string][]v1alpha1.LvmVolumeGroup) (map[string][]v1alpha1.LvmVolumeGroup, error) {
	result := make(map[string][]v1alpha1.LvmVolumeGroup, len(nodesWithLVGs))

	for nodeName, lvgs := range nodesWithLVGs {
		lvgNames := make(map[string]struct{}, len(lvgs))
		for _, l := range lvgs {
			lvgNames[l.Name] = struct{}{}
		}

		nodeIncludesLVG := true
		for _, sc := range scs {
			scLvgs, err := extractLVGsFromSC(sc)
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

func removeUnusedLVGs(lvgs map[string]v1alpha1.LvmVolumeGroup, scsLVGs map[string]LVMVolumeGroups) map[string]v1alpha1.LvmVolumeGroup {
	result := make(map[string]v1alpha1.LvmVolumeGroup, len(lvgs))
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

func getLVGsFromStorageClasses(scs map[string]v1.StorageClass) (map[string]LVMVolumeGroups, error) {
	result := make(map[string]LVMVolumeGroups, len(scs))

	for _, sc := range scs {
		lvgs, err := extractLVGsFromSC(sc)
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

func extractLVGsFromSC(sc v1.StorageClass) (LVMVolumeGroups, error) {
	var lvmVolumeGroups LVMVolumeGroups
	err := yaml.Unmarshal([]byte(sc.Parameters[lvmVolumeGroupsParamKey]), &lvmVolumeGroups)
	if err != nil {
		return nil, err
	}
	return lvmVolumeGroups, nil
}

func sortLVGsByNodeName(lvgs map[string]v1alpha1.LvmVolumeGroup) map[string][]v1alpha1.LvmVolumeGroup {
	sorted := make(map[string][]v1alpha1.LvmVolumeGroup, len(lvgs))
	for _, lvg := range lvgs {
		for _, node := range lvg.Status.Nodes {
			sorted[node.Name] = append(sorted[node.Name], lvg)
		}
	}

	return sorted
}

func getLVMVolumeGroups(ctx context.Context, cl client.Client) (map[string]v1alpha1.LvmVolumeGroup, error) {
	lvgl := &v1alpha1.LvmVolumeGroupList{}
	err := cl.List(ctx, lvgl)
	if err != nil {
		return nil, err
	}

	lvgMap := make(map[string]v1alpha1.LvmVolumeGroup, len(lvgl.Items))
	for _, lvg := range lvgl.Items {
		lvgMap[lvg.Name] = lvg
	}

	return lvgMap, nil
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

func getStorageClassesUsedByPVCs(ctx context.Context, cl client.Client, pvcs map[string]corev1.PersistentVolumeClaim) (map[string]v1.StorageClass, error) {
	scs := &v1.StorageClassList{}
	err := cl.List(ctx, scs)
	if err != nil {
		return nil, err
	}

	scMap := make(map[string]v1.StorageClass, len(scs.Items))
	for _, sc := range scs.Items {
		scMap[sc.Name] = sc
	}

	result := make(map[string]v1.StorageClass, len(pvcs))
	for _, pvc := range pvcs {
		if pvc.Spec.StorageClassName == nil {
			err = errors.New(fmt.Sprintf("not StorageClass specified for PVC %s", pvc.Name))
			return nil, err
		}

		scName := *pvc.Spec.StorageClassName
		if sc, match := scMap[scName]; match {
			result[sc.Name] = sc
		}
	}

	return result, nil
}

func getUsedPVC(ctx context.Context, cl client.Client, pod *corev1.Pod) (map[string]corev1.PersistentVolumeClaim, error) {
	usedPvc := make(map[string]corev1.PersistentVolumeClaim, len(pod.Spec.Volumes))

	pvcs := &corev1.PersistentVolumeClaimList{}
	err := cl.List(ctx, pvcs)
	if err != nil {
		return nil, err
	}

	pvcMap := make(map[string]corev1.PersistentVolumeClaim, len(pvcs.Items))
	for _, pvc := range pvcs.Items {
		pvcMap[pvc.Name] = pvc
	}

	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			usedPvc[volume.PersistentVolumeClaim.ClaimName] = pvcMap[volume.PersistentVolumeClaim.ClaimName]
		}
	}

	return usedPvc, nil
}
