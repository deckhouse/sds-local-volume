package scheduler

import (
	apiv1 "k8s.io/api/core/v1"
)

type ExtenderArgs struct {
	// Pod being scheduled
	Pod *apiv1.Pod `json:"pod"`
	// List of candidate nodes where the pod can be scheduled; to be populated
	// only if ExtenderConfig.NodeCacheCapable == false
	Nodes *apiv1.NodeList `json:"nodes,omitempty"`
	// List of candidate node names where the pod can be scheduled; to be
	// populated only if ExtenderConfig.NodeCacheCapable == true
	NodeNames *[]string `json:"nodenames,omitempty"`
}

type HostPriority struct {
	// Name of the host
	Host string `json:"host"`
	// Score associated with the host
	Score int `json:"score"`
}

type HostPriorityList []HostPriority

type ExtenderFilterResult struct {
	// Filtered set of nodes where the pod can be scheduled; to be populated
	// only if ExtenderConfig.NodeCacheCapable == false
	Nodes *apiv1.NodeList `json:"nodes,omitempty"`
	// Filtered set of nodes where the pod can be scheduled; to be populated
	// only if ExtenderConfig.NodeCacheCapable == true
	NodeNames *[]string `json:"nodenames,omitempty"`
	// Filtered out nodes where the pod can't be scheduled and the failure messages
	FailedNodes FailedNodesMap `json:"failedNodes,omitempty"`
	// Error message indicating failure
	Error string `json:"error,omitempty"`
}

type FailedNodesMap map[string]string
