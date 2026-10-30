/*
Copyright 2026 Flant JSC

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

package controller

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

// csiNodeNames returns the nodes the module's CSI node runs on. It selects them
// exactly as the CSI node DaemonSet does, key and empty value, so a node that
// carries the key with another value does not count: nothing is scheduled there.
func csiNodeNames(ctx context.Context, cl client.Client) (map[string]struct{}, error) {
	nodes := &corev1.NodeList{}
	if err := cl.List(ctx, nodes, client.MatchingLabels{slv.CSINodeLabel: ""}); err != nil {
		return nil, err
	}

	names := make(map[string]struct{}, len(nodes.Items))
	for _, node := range nodes.Items {
		names[node.Name] = struct{}{}
	}
	return names, nil
}

// lvgsOnNodesWithoutCSI returns "<lvg> (<node>)", sorted, for every effective
// LVMVolumeGroup whose node does not run the module's CSI node. A volume placed
// on such a group cannot be mounted: nothing on the node serves it.
func lvgsOnNodesWithoutCSI(lvgList *snc.LVMVolumeGroupList, effectiveLVGs []slv.LocalStorageClassLVG, csiNodes map[string]struct{}) []string {
	used := make(map[string]struct{}, len(effectiveLVGs))
	for _, lvg := range effectiveLVGs {
		used[lvg.Name] = struct{}{}
	}

	var found []string
	for _, lvg := range lvgList.Items {
		if _, ok := used[lvg.Name]; !ok {
			continue
		}
		node := lvg.Spec.Local.NodeName
		if node == "" {
			continue
		}
		if _, ok := csiNodes[node]; !ok {
			found = append(found, fmt.Sprintf("%s (%s)", lvg.Name, node))
		}
	}
	slices.Sort(found)
	return found
}

// setLocalStorageClassCreated reports a class whose StorageClass is in place.
// A non-empty warning keeps the class Ready, under a reason of its own.
func setLocalStorageClassCreated(ctx context.Context, cl client.Client, lsc *slv.LocalStorageClass, warning string) error {
	if warning == "" {
		return updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	}

	if err := ensureFinalizer(ctx, cl, lsc); err != nil {
		return err
	}
	return publishLocalStorageClassStatus(ctx, cl, lsc, metav1.Condition{
		Type:               slv.ConditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             slv.ConditionReasonLVMVolumeGroupsOutsideCSINodes,
		Message:            warning,
		ObservedGeneration: lsc.Generation,
	}, CreatedStatusPhase, warning)
}

// isCSINode mirrors the CSI node DaemonSet selector: the key with an empty value.
func isCSINode(node *corev1.Node) bool {
	value, ok := node.Labels[slv.CSINodeLabel]
	return ok && value == ""
}

// csiNodeChanged passes the node events that change whether the CSI node runs
// on the node, and drops the rest, status heartbeats in particular.
func csiNodeChanged() predicate.TypedFuncs[*corev1.Node] {
	return predicate.TypedFuncs[*corev1.Node]{
		CreateFunc: func(e event.TypedCreateEvent[*corev1.Node]) bool { return isCSINode(e.Object) },
		DeleteFunc: func(e event.TypedDeleteEvent[*corev1.Node]) bool { return isCSINode(e.Object) },
		UpdateFunc: func(e event.TypedUpdateEvent[*corev1.Node]) bool {
			return isCSINode(e.ObjectOld) != isCSINode(e.ObjectNew)
		},
		GenericFunc: func(event.TypedGenericEvent[*corev1.Node]) bool { return false },
	}
}
