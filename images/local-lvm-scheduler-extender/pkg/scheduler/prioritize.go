package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/topolvm/topolvm"
	corev1 "k8s.io/api/core/v1"
	"local-lvm-scheduler-extender/api/v1alpha1"
	"math"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
	"sync"
)

func (s scheduler) prioritize(w http.ResponseWriter, r *http.Request) {
	var input ExtenderArgs

	reader := http.MaxBytesReader(w, r.Body, 10<<20)
	err := json.NewDecoder(reader).Decode(&input)
	if err != nil {
		http.Error(w, "Bad Request.", http.StatusBadRequest)
		return
	}

	result, err := scoreNodes(s.client, input.Pod, input.Nodes.Items, s.defaultDivisor, s.divisors)
	if err != nil {
		http.Error(w, "Bad Request.", http.StatusBadRequest)
		return
	}

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func scoreNodes(cl client.Client, pod *corev1.Pod, nodes []corev1.Node, defaultDivisor float64, divisors map[string]float64) ([]HostPriority, error) {
	//var dcs []string
	//for k := range pod.Annotations {
	//	fmt.Println("PRefiX: " + topolvm.GetCapacityKeyPrefix())
	//	if strings.HasPrefix(k, topolvm.GetCapacityKeyPrefix()) {
	//		dcs = append(dcs, k[len(topolvm.GetCapacityKeyPrefix()):])
	//	}
	//}
	//if len(dcs) == 0 {
	//	fmt.Println("DSC IS NIL")
	//	return nil, nil
	//}

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

	wg := &sync.WaitGroup{}
	wg.Add(len(nodes))

	result := make([]HostPriority, 0, len(nodes))

	// TODO: возможно нужно к самой свободной ноде всегда делать 10
	for _, node := range nodes {
		lvgs := lvgByNodes[node.Name]
		freeSpace, err := getNodeFreeSpace(lvgs)
		if err != nil {
			// log.error?
			continue
		}

		score := getNodeScore(freeSpace)
		result = append(result, HostPriority{Host: node.Name, Score: score})
	}

	//wg := &sync.WaitGroup{}
	//wg.Add(len(nodes))
	//for i := range nodes {
	//	r := &result[i]
	//	item := nodes[i]
	//	go func() {
	//		score := scoreNode(item, dcs, defaultDivisor, divisors)
	//		*r = HostPriority{Host: item.Name, Score: score}
	//		wg.Done()
	//	}()
	//}
	//wg.Wait()

	fmt.Println("ALL RESULT")
	fmt.Println(result)
	for _, n := range result {
		fmt.Println("HOST: " + n.Host)
		fmt.Println("SCORE: ", n.Score)
	}
	fmt.Println("ALL RESULT")

	return result, nil
}

func getNodeScore(freeSpace map[string]int64) int {
	capacity := freeSpace[thin] + freeSpace[thick]
	gb := capacity >> 30

	// Avoid logarithm of zero, which diverges to negative infinity.
	if gb == 0 {
		// If there is a non-nil capacity but we dont have at least one gigabyte, we score it with one.
		// This is because the capacityToScore precision is at the gigabyte level.
		if capacity > 0 {
			return 1
		}

		return 0
	}

	converted := int(math.Log2(float64(gb) / 1))
	switch {
	case converted < 1:
		return 1
	case converted > 10:
		return 10
	default:
		return converted
	}
}

func scoreNode(item corev1.Node, deviceClasses []string, defaultDivisor float64, divisors map[string]float64) int {
	minScore := math.MaxInt32
	for _, dc := range deviceClasses {
		if val, ok := item.Annotations[topolvm.GetCapacityKeyPrefix()+dc]; ok {
			capacity, _ := strconv.ParseUint(val, 10, 64)
			var divisor float64
			if v, ok := divisors[dc]; ok {
				divisor = v
			} else {
				divisor = defaultDivisor
			}
			score := capacityToScore(capacity, divisor)
			if score < minScore {
				minScore = score
			}
		}
	}
	if minScore == math.MaxInt32 {
		minScore = 0
	}
	return minScore
}

func capacityToScore(capacity uint64, divisor float64) int {
	gb := capacity >> 30

	// Avoid logarithm of zero, which diverges to negative infinity.
	if gb == 0 {
		// If there is a non-nil capacity but we dont have at least one gigabyte, we score it with one.
		// This is because the capacityToScore precision is at the gigabyte level.
		// TODO: introduce another scheduling algorithm for byte-level precision.
		if capacity > 0 {
			return 1
		}

		return 0
	}

	converted := int(math.Log2(float64(gb) / divisor))
	switch {
	case converted < 1:
		return 1
	case converted > 10:
		return 10
	default:
		return converted
	}
}
