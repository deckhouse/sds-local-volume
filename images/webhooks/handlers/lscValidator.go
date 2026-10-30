/*
Copyright 2025 Flant JSC

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

package handlers

import (
	"context"
	"fmt"
	"slices"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/sds-common-lib/fsreserve"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/webhooks/pkg/logger"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

// LSCValidate returns the LocalStorageClass validator, bound to log.
func LSCValidate(log logger.Logger) kwhvalidating.ValidatorFunc {
	log = log.Named("lsc-validator")

	return func(ctx context.Context, _ *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
		lsc, ok := obj.(*slv.LocalStorageClass)
		if !ok {
			// If not a storage class just continue the validation chain(if there is one) and do nothing.
			return &kwhvalidating.ValidatorResult{}, nil
		}

		// A class being deleted only waits for its finalizers to go. Checking it
		// against the cluster as it is now would keep a class that no longer
		// passes (one of its LVMVolumeGroups deleted first, say) from ever
		// losing its finalizer, and so from ever being deleted.
		if lsc.GetDeletionTimestamp() != nil {
			return &kwhvalidating.ValidatorResult{Valid: true}, nil
		}

		// The reserved-block percentage travels as an annotation, so the CRD
		// schema cannot vet it. Reject a bad value here instead of letting
		// every volume of the class fail at NodeStageVolume.
		reservedPercent, err := fsreserve.FromMap(lsc.Annotations)
		if err != nil {
			errMsg := err.Error()
			log.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}

		var warnings []string
		if reservedPercent != fsreserve.Default && !fsreserve.SupportedFSType(lsc.Spec.FSType) {
			warnings = append(warnings, fmt.Sprintf(
				"%s is ignored for fsType %q: only the ext filesystems keep blocks for the superuser",
				fsreserve.AnnotationKey, lsc.Spec.FSType))
		}

		cl, err := NewKubeClient("")
		if err != nil {
			// Returning the error fails this one admission request. The previous
			// klog.Fatal exited the process, so a single transient failure here
			// took the webhook down for every request until the pod restarted.
			log.Error("unable to build a kube client", logger.Err(err))
			return nil, err
		}

		if lsc.Spec.LVM == nil {
			errMsg := fmt.Sprintf("LocalStorageClass %s has no spec.lvm configured", lsc.Name)
			log.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}

		if len(lsc.Spec.LVM.LVMVolumeGroups) == 0 {
			errMsg := "Field spec.lvm.lvmVolumeGroups must not be empty"
			log.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}

		listDevice := &snc.LVMVolumeGroupList{}

		err = cl.List(ctx, listDevice)
		if err != nil {
			// As above: fail the request, do not exit the process.
			log.Error("unable to list LVMVolumeGroups", logger.Err(err))
			return nil, err
		}

		nameEntries, selectorEntries := 0, 0
		for _, lvmGroup := range lsc.Spec.LVM.LVMVolumeGroups {
			if lvmGroup.Name != "" {
				nameEntries++
			}
			if lvmGroup.LabelSelector != nil {
				selectorEntries++
			}
		}
		if nameEntries > 0 && selectorEntries > 0 {
			errMsg := "spec.lvm.lvmVolumeGroups must use either name entries or labelSelector entries, not a mix"
			log.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}

		var lvmVolumeGroupUnique []string

		for i, lvmGroup := range lsc.Spec.LVM.LVMVolumeGroups {
			hasName := lvmGroup.Name != ""
			hasSelector := lvmGroup.LabelSelector != nil

			if hasName == hasSelector {
				errMsg := fmt.Sprintf("Each spec.lvm.lvmVolumeGroups entry must set exactly one of name or labelSelector (entry #%d)", i)
				log.Info(errMsg)
				return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
			}

			if lsc.Spec.LVM.Type == "Thin" {
				if lvmGroup.Thin == nil || lvmGroup.Thin.PoolName == "" {
					errMsg := fmt.Sprintf("Field thin.poolName is required for spec.lvm.lvmVolumeGroups entry #%d when type is Thin", i)
					log.Info(errMsg)
					return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
				}
			} else if lvmGroup.Thin != nil {
				errMsg := fmt.Sprintf("Field thin must not be specified for spec.lvm.lvmVolumeGroups entry #%d when type is Thick", i)
				log.Info(errMsg)
				return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
			}

			if hasSelector {
				if _, err := metav1.LabelSelectorAsSelector(lvmGroup.LabelSelector); err != nil {
					errMsg := fmt.Sprintf("Invalid labelSelector in spec.lvm.lvmVolumeGroups entry #%d: %s", i, err.Error())
					log.Info(errMsg)
					return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
				}
				// The concrete match set (existence, emptiness, same-node conflicts)
				// is validated by the controller, which reflects the result in the
				// LocalStorageClass status.
				continue
			}

			if slices.Contains(lvmVolumeGroupUnique, lvmGroup.Name) {
				errMsg := fmt.Sprintf("There must be unique LVMVolumeGroup names (%s duplicates)", lvmGroup.Name)
				log.Info(errMsg)
				return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
			}
			lvmVolumeGroupUnique = append(lvmVolumeGroupUnique, lvmGroup.Name)

			lvgExists := false
			for _, lvmVG := range listDevice.Items {
				if lvmVG.Name == lvmGroup.Name {
					lvgExists = true
					break
				}
			}
			if !lvgExists {
				errMsg := fmt.Sprintf("LVMVolumeGroup %s not found; ", lvmGroup.Name)
				log.Info(errMsg)
				return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
			}
		}

		if len(lvmVolumeGroupUnique) > 0 {
			nodes := &corev1.NodeList{}
			if err := cl.List(ctx, nodes, client.MatchingLabels{slv.CSINodeLabel: ""}); err != nil {
				log.Error("unable to list the nodes running the CSI node", logger.Err(err))
				warnings = append(warnings, "could not check whether the nodes of the LVMVolumeGroups run the sds-local-volume CSI node")
			} else if outside := lvgsOnNodesWithoutCSI(listDevice.Items, lvmVolumeGroupUnique, nodes.Items); len(outside) > 0 {
				warnings = append(warnings, slv.LVMVolumeGroupsOutsideCSINodesMessage(outside))
			}
		}

		return &kwhvalidating.ValidatorResult{Valid: true, Warnings: warnings}, nil
	}
}

// lvgsOnNodesWithoutCSI returns "<lvg> (<node>)", sorted, for every named
// LVMVolumeGroup whose node is not among csiNodes. It is a warning, not a
// rejection: the controller labels the nodes after a ModuleConfig change, so a
// class created right after one may come ahead of the label.
func lvgsOnNodesWithoutCSI(lvgs []snc.LVMVolumeGroup, names []string, csiNodes []corev1.Node) []string {
	withCSI := make(map[string]struct{}, len(csiNodes))
	for _, node := range csiNodes {
		withCSI[node.Name] = struct{}{}
	}

	var outside []string
	for _, lvg := range lvgs {
		if !slices.Contains(names, lvg.Name) || lvg.Spec.Local.NodeName == "" {
			continue
		}
		if _, ok := withCSI[lvg.Spec.Local.NodeName]; !ok {
			outside = append(outside, fmt.Sprintf("%s (%s)", lvg.Name, lvg.Spec.Local.NodeName))
		}
	}
	slices.Sort(outside)
	return outside
}
