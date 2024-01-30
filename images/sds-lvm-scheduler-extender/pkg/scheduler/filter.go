package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"local-lvm-scheduler-extender/api/v1alpha1"
	"local-lvm-scheduler-extender/pkg/logger"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sync"
)

const (
	lvmTypeParamKey = "local-lvm.csi.storage.deckhouse.io/lvm-type"
	thick           = "Thick"
	thin            = "Thin"
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

	s.log.Debug("[filter] starts to extract requested size")
	requested, err := extractRequestedSize(s.client, s.log, input.Pod)
	if err != nil {
		s.log.Error(err, fmt.Sprintf("[filter] unable to extract request size for a pod %s", input.Pod.Name))
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	s.log.Debug("[filter] successfully extracted the requested size")

	s.log.Debug("[filter] starts to filter requested nodes")
	result, err := filterNodes(s.client, s.log, *input.Nodes, requested)
	if err != nil {
		s.log.Error(err, "[filter] unable to filter requested nodes")
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	s.log.Debug("[filter] successfully filtered the requested nodes")

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		s.log.Error(err, "[filter] unable to encode a response")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	s.log.Debug("[filter] ends the serving")
}

func extractRequestedSize(cl client.Client, log logger.Logger, pod *corev1.Pod) (map[string]int64, error) {
	ctx := context.Background()
	usedPvc := make([]string, 0, len(pod.Spec.Volumes))
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			usedPvc = append(usedPvc, v.PersistentVolumeClaim.ClaimName)
		}
	}

	pvcs := &corev1.PersistentVolumeClaimList{}
	err := cl.List(ctx, pvcs)
	if err != nil {
		return nil, err
	}

	pvcMap := make(map[string]corev1.PersistentVolumeClaim, len(pvcs.Items))
	for _, pvc := range pvcs.Items {
		pvcMap[pvc.Name] = pvc
	}

	scs := &v1.StorageClassList{}
	err = cl.List(ctx, scs)
	if err != nil {
		return nil, err
	}

	scMap := make(map[string]v1.StorageClass, len(scs.Items))
	for _, sc := range scs.Items {
		scMap[sc.Name] = sc
	}

	result := make(map[string]int64, 2)

	for _, pvName := range usedPvc {
		pv := pvcMap[pvName]

		scName := pv.Spec.StorageClassName
		sc := scMap[*scName]
		log.Trace(fmt.Sprintf("[extractRequestedSize] StorageClass %s has LVMType %s", sc.Name, sc.Parameters[lvmTypeParamKey]))
		switch sc.Parameters[lvmTypeParamKey] {
		case thick:
			result[thick] += pv.Spec.Resources.Requests.Storage().Value()
		case thin:
			result[thin] += pv.Spec.Resources.Requests.Storage().Value()
		}
	}

	for t, s := range result {
		log.Trace(fmt.Sprintf("[extractRequestedSize] pod %s has requested type: %s, size: %d", pod.Name, t, s))
	}

	return result, nil
}

func filterNodes(cl client.Client, log logger.Logger, nodes corev1.NodeList, requested map[string]int64) (*ExtenderFilterResult, error) {
	if len(requested) == 0 {
		return &ExtenderFilterResult{
			Nodes: &nodes,
		}, nil
	}

	ctx := context.Background()
	lvgl := &v1alpha1.LvmVolumeGroupList{}
	err := cl.List(ctx, lvgl)
	if err != nil {
		return nil, err
	}

	lvgByNodes := make(map[string][]v1alpha1.LvmVolumeGroup, len(lvgl.Items))
	for _, lvg := range lvgl.Items {
		for _, node := range lvg.Status.Nodes {
			lvgByNodes[node.Name] = append(lvgByNodes[node.Name], lvg)
		}
	}

	log.Trace(fmt.Sprintf("[filterNodes] sorted LVG by nodes: %+v", lvgByNodes))

	result := &ExtenderFilterResult{
		Nodes:       &corev1.NodeList{},
		FailedNodes: FailedNodesMap{},
	}

	wg := &sync.WaitGroup{}
	wg.Add(len(nodes.Items))

	for _, node := range nodes.Items {
		go func(node corev1.Node) {
			defer wg.Done()

			lvgs := lvgByNodes[node.Name]
			freeSpace, err := getNodeFreeSpace(lvgs)
			if err != nil {
				log.Error(err, fmt.Sprintf("[filterNodes] unable to get node free space, node: %s, lvgs: %+v", node.Name, lvgs))
				result.FailedNodes[node.Name] = "error occurred while counting free space"
				return
			}
			if freeSpace[thick] < requested[thick] ||
				freeSpace[thin] < requested[thin] {
				result.FailedNodes[node.Name] = "not enough space"
				return
			}

			result.Nodes.Items = append(result.Nodes.Items, node)
		}(node)
	}
	wg.Wait()

	for _, node := range result.Nodes.Items {
		log.Trace(fmt.Sprintf("[filterNodes] suitable node: %s", node.Name))
	}

	for node, reason := range result.FailedNodes {
		log.Trace(fmt.Sprintf("[filterNodes] failed node: %s, reason: %s", node, reason))
	}

	return result, nil
}

func getNodeFreeSpace(lvgs []v1alpha1.LvmVolumeGroup) (map[string]int64, error) {
	freeSpaces := make(map[string]int64, 2)

	for _, lvg := range lvgs {
		// здесь не нужно делать выборку по типу, мы просто смотрим, сколько есть места такого и такого (а не одно из двух)

		// выбираю максимальное свободное место из thin pool
		for _, tp := range lvg.Status.ThinPools {
			thinSpace, err := getThinPoolFreeSpace(tp)
			if err != nil {
				return nil, err
			}

			if freeSpaces[thin] < thinSpace.Value() {
				freeSpaces[thin] = thinSpace.Value()
			}
		}

		thickSpace, err := getVGFreeSpace(&lvg)
		if err != nil {
			return nil, err
		}

		if freeSpaces[thick] < thickSpace.Value() {
			freeSpaces[thick] = thickSpace.Value()
		}
	}

	return freeSpaces, nil
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

func getThinPoolFreeSpace(tp v1alpha1.StatusThinPool) (resource.Quantity, error) {
	free := tp.ActualSize
	used, err := resource.ParseQuantity(tp.UsedSize)
	if err != nil {
		return resource.Quantity{}, err
	}
	free.Sub(used)

	return free, nil
}
