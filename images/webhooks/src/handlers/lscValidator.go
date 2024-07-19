/*
Copyright 2024 Flant JSC

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
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
	"k8s.io/klog/v2"
	"slices"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	listDevice := &snc.LvmVolumeGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       snc.BlockDeviceKind,
			APIVersion: snc.TypeMediaAPIVersion,
		},
		ListMeta: metav1.ListMeta{},
		Items:    []snc.LvmVolumeGroup{},
	}

	err = cl.List(ctx, listDevice)
	if err != nil {
		klog.Fatal(err)
	}

	errMsg := ""
	var lvmVolumeGroupUnique []string

	thickExists := false
	thinExists := false
	for _, lvmGroup := range lsc.Spec.LVM.LVMVolumeGroups {
		lvgExists := false

		if slices.Contains(lvmVolumeGroupUnique, lvmGroup.Name) {
			errMsg = fmt.Sprintf("There must be unique LVMVolumeGroup names (%s duplicates)", lvmGroup.Name)
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
				nil
		}

		lvmVolumeGroupUnique = append(lvmVolumeGroupUnique, lvmGroup.Name)

		for _, lvmVG := range listDevice.Items {
			if lvmVG.Name == lvmGroup.Name {
				lvgExists = true
				break
			}
		}

		if !lvgExists {
			errMsg = fmt.Sprintf("LVMVolumeGroup %s not found; ", lvmGroup.Name)
			klog.Info(errMsg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
				nil
		}

		if lvmGroup.Thin == nil {
			thickExists = true
		} else {
			thinExists = true
		}
	}

	if thinExists && lsc.Spec.LVM.Type == "Thick" {
		errMsg = "There must be only thick pools with Thick LVM type"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
			nil
	}

	if thickExists && lsc.Spec.LVM.Type == "Thin" {
		errMsg = "There must be only thin pools with Thin LVM type"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
			nil
	}

	if thickExists == true && thinExists == true {
		errMsg = "There must be only thin or thick pools simultaneously"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
			nil
	} else {
		return &kwhvalidating.ValidatorResult{Valid: true},
			nil
	}
}
