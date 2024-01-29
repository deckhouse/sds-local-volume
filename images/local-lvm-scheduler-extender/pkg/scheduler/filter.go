package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/topolvm/topolvm"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"local-lvm-scheduler-extender/api/v1alpha1"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
	"sync"
)

const (
	lvmTypeParamKey = "local-lvm.csi.storage.deckhouse.io/lvm-type"
	thick           = "Thick"
	thin            = "Thin"
)

func (s scheduler) filter(w http.ResponseWriter, r *http.Request) {
	var input ExtenderArgs
	reader := http.MaxBytesReader(w, r.Body, 10<<20)
	err := json.NewDecoder(reader).Decode(&input)
	if err != nil || input.Nodes == nil || input.Pod == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	requested, err := extractRequestedSize(s.client, input.Pod)
	if err != nil {
		fmt.Println("ERROR: " + err.Error())
		http.Error(w, "bad request", http.StatusBadRequest)
	}

	fmt.Println("EXTRACTED SIZE")

	result, err := filterNodes(s.client, *input.Nodes, requested)
	if err != nil {
		fmt.Println("ERROR: " + err.Error())
		http.Error(w, "bad request", http.StatusBadRequest)
	}

	fmt.Println("FILTERED NODES")

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func extractRequestedSize(cl client.Client, pod *corev1.Pod) (map[string]int64, error) {
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

		switch sc.Parameters[lvmTypeParamKey] {
		case thick:
			result[thick] += pv.Spec.Resources.Requests.Storage().Value()
		case thin:
			result[thin] += pv.Spec.Resources.Requests.Storage().Value()
		}
	}

	return result, nil
}

func filterNodes(cl client.Client, nodes corev1.NodeList, requested map[string]int64) (*ExtenderFilterResult, error) {
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

	result := &ExtenderFilterResult{
		Nodes:       &corev1.NodeList{},
		FailedNodes: FailedNodesMap{},
	}

	wg := &sync.WaitGroup{}
	wg.Add(len(nodes.Items))

	for _, n := range nodes.Items {
		node := n
		lvgs := lvgByNodes[node.Name]

		go func() {
			fmt.Println("FUNC STARTS")
			defer wg.Done()
			freeSpace, err := getNodeFreeSpace(lvgs)
			if err != nil {
				result.FailedNodes[node.Name] = err.Error()
				return
			}

			if freeSpace[thick] < requested[thick] ||
				freeSpace[thin] < requested[thin] {
				result.FailedNodes[node.Name] = "not enough space"
				return
			}

			result.Nodes.Items = append(result.Nodes.Items, node)
			fmt.Println("FUNC ENDS")
		}()
	}

	wg.Wait()

	//failedNodes := make([]string, len(nodes.Items))
	//wg := &sync.WaitGroup{}
	//wg.Add(len(nodes.Items))
	//for i := range nodes.Items {
	//	reason := &failedNodes[i]
	//	node := nodes.Items[i]
	//	go func() {
	//		*reason = filterNode(node, requested)
	//		wg.Done()
	//	}()
	//}
	//wg.Wait()
	//result := &ExtenderFilterResult{
	//	Nodes:       &corev1.NodeList{},
	//	FailedNodes: FailedNodesMap{},
	//}
	//for i, reason := range failedNodes {
	//	if len(reason) == 0 {
	//		result.Nodes.Items = append(result.Nodes.Items, nodes.Items[i])
	//	} else {
	//		result.FailedNodes[nodes.Items[i].Name] = reason
	//	}
	//}

	fmt.Println("RESULT NODE")
	fmt.Println(result.Nodes.Items)
	fmt.Println("RESULT NODE")

	fmt.Println("FAILED NODES")
	fmt.Println(result.FailedNodes)
	fmt.Println("FAILED NODES")

	fmt.Println("NODE NAMES")
	fmt.Println("NODE NAMES")

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

func getLVGType(lvg *v1alpha1.LvmVolumeGroup) string {
	if len(lvg.Spec.ThinPools) == 0 {
		return thick
	}

	return thin
}

func filterNode(node corev1.Node, requested map[string]int64) string {
	for dc, required := range requested {
		val, ok := node.Annotations[topolvm.GetCapacityKeyPrefix()+dc]
		if !ok {
			return "no capacity annotation"
		}
		capacity, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return "bad capacity annotation: " + val
		}
		if capacity < uint64(required) {
			return "out of VG free space"
		}
	}
	return ""
}
