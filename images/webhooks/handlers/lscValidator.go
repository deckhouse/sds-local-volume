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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func LSCValidate(ctx context.Context, _ *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
	lsc, ok := obj.(*slv.LocalStorageClass)
	if !ok {
		// If not a storage class just continue the validation chain(if there is one) and do nothing.
		return &kwhvalidating.ValidatorResult{}, nil
	}

	cl, err := NewKubeClient("")
	if err != nil {
		klog.Fatal(err)
	}

	if lsc.Spec.LVM == nil {
		errMsg := fmt.Sprintf("LocalStorageClass %s has no spec.lvm configured", lsc.Name)
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	if len(lsc.Spec.LVM.LVMVolumeGroups) == 0 {
		errMsg := "Field spec.lvm.lvmVolumeGroups must not be empty"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	listDevice := &snc.LVMVolumeGroupList{}

	err = cl.List(ctx, listDevice)
	if err != nil {
		klog.Fatal(err)
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
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	var lvmVolumeGroupUnique []string

	for i, lvmGroup := range lsc.Spec.LVM.LVMVolumeGroups {
		hasName := lvmGroup.Name != ""
		hasSelector := lvmGroup.LabelSelector != nil

		if hasName == hasSelector {
			errMsg := fmt.Sprintf("Each spec.lvm.lvmVolumeGroups entry must set exactly one of name or labelSelector (entry #%d)", i)
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}

		if lsc.Spec.LVM.Type == "Thin" {
			if lvmGroup.Thin == nil || lvmGroup.Thin.PoolName == "" {
				errMsg := fmt.Sprintf("Field thin.poolName is required for spec.lvm.lvmVolumeGroups entry #%d when type is Thin", i)
				klog.Info(errMsg)
				return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
			}
		} else if lvmGroup.Thin != nil {
			errMsg := fmt.Sprintf("Field thin must not be specified for spec.lvm.lvmVolumeGroups entry #%d when type is Thick", i)
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}

		if hasSelector {
			if _, err := metav1.LabelSelectorAsSelector(lvmGroup.LabelSelector); err != nil {
				errMsg := fmt.Sprintf("Invalid labelSelector in spec.lvm.lvmVolumeGroups entry #%d: %s", i, err.Error())
				klog.Info(errMsg)
				return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
			}
			// The concrete match set (existence, emptiness, same-node conflicts)
			// is validated by the controller, which reflects the result in the
			// LocalStorageClass status.
			continue
		}

		if slices.Contains(lvmVolumeGroupUnique, lvmGroup.Name) {
			errMsg := fmt.Sprintf("There must be unique LVMVolumeGroup names (%s duplicates)", lvmGroup.Name)
			klog.Info(errMsg)
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
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
		}
	}

	return &kwhvalidating.ValidatorResult{Valid: true}, nil
}
