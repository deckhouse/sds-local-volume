package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"math"
	"net/http"
	"sds-lvm-scheduler-extender/api/v1alpha1"
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

	result, err := scoreNodes(s.client, s.log, input.Nodes.Items, s.defaultDivisor)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to score nodes")
		http.Error(w, "Bad Request.", http.StatusBadRequest)
		return
	}

	w.Header().Set("content-type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		s.log.Error(err, "[prioritize] unable to encode a response")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	s.log.Debug("[prioritize] ends serving")
}

func scoreNodes(cl client.Client, log logger.Logger, nodes []corev1.Node, divisor float64) ([]HostPriority, error) {
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

	log.Trace(fmt.Sprintf("[scoreNodes] sorted LVG by nodes: %+v", lvgByNodes))

	result := make([]HostPriority, 0, len(nodes))

	// TODO: probably should score the nodes exactly to their free space
	wg := &sync.WaitGroup{}
	wg.Add(len(nodes))

	for _, node := range nodes {
		go func(node corev1.Node) {
			defer wg.Done()

			lvgs := lvgByNodes[node.Name]
			freeSpace, err := getNodeFreeSpace(lvgs)
			if err != nil {
				log.Error(err, fmt.Sprintf("[scoreNodes] unable to get node free space, node: %s, lvgs: %+v", node.Name, lvgs))
				return
			}

			score := getNodeScore(freeSpace, divisor)
			result = append(result, HostPriority{Host: node.Name, Score: score})
		}(node)
	}
	wg.Wait()

	for _, n := range result {
		log.Trace(fmt.Sprintf("[scoreNodes] host: %s", n.Host))
		log.Trace(fmt.Sprintf("[scoreNodes] score: %d", n.Score))
	}

	return result, nil
}

func getNodeScore(freeSpace map[string]int64, divisor float64) int {
	capacity := freeSpace[thin] + freeSpace[thick]
	gb := capacity >> 30

	// Avoid logarithm of zero, which diverges to negative infinity.
	if gb == 0 {
		// If there is a non-nil capacity, but we don't have at least one gigabyte, we score it with one.
		// This is because the capacityToScore precision is at the gigabyte level.
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
