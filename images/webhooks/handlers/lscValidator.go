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
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	d8commonapi "github.com/deckhouse/sds-common-lib/api/v1alpha1"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	sdsLocalVolumeModuleName = "sds-local-volume"
)

func LSCValidate(ctx context.Context, _ *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
	lsc, ok := obj.(*slv.LocalStorageClass)
	if !ok {
		// If not a storage class just continue the validation chain(if there is one) and do nothing.
		return &kwhvalidating.ValidatorResult{}, nil
	}

	// Validate that either LVM or RawFile is specified, but not both
	if lsc.Spec.LVM != nil && lsc.Spec.RawFile != nil {
		errMsg := "LocalStorageClass must have either lvm or rawFile configuration, not both"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	if lsc.Spec.LVM == nil && lsc.Spec.RawFile == nil {
		errMsg := "LocalStorageClass must have either lvm or rawFile configuration"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}

	// RawFile validation
	if lsc.Spec.RawFile != nil {
		return validateRawFile(lsc)
	}

	// LVM validation
	return validateLVM(ctx, lsc)
}

func validateRawFile(lsc *slv.LocalStorageClass) (*kwhvalidating.ValidatorResult, error) {
	// RawFile doesn't require additional complex validation
	// The CRD schema handles basic validation (dataDir format, etc.)
	klog.Infof("Validating RawFile LocalStorageClass: %s", lsc.Name)
	return &kwhvalidating.ValidatorResult{Valid: true}, nil
}

func validateLVM(ctx context.Context, lsc *slv.LocalStorageClass) (*kwhvalidating.ValidatorResult, error) {
	cl, err := NewKubeClient("")
	if err != nil {
		klog.Fatal(err)
	}

	listDevice := &snc.LVMVolumeGroupList{}

	err = cl.List(ctx, listDevice)
	if err != nil {
		klog.Fatal(err)
	}

	errMsg := ""
	var lvmVolumeGroupUnique []string

	var thickNames, thinNames []string
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
			thickNames = append(thickNames, lvmGroup.Name)
		} else {
			thinNames = append(thinNames, lvmGroup.Name)
		}
	}

	thinExists, thickExists := len(thinNames) > 0, len(thickNames) > 0

	if thinExists {
		ctx := context.Background()
		cl, err := NewKubeClient("")
		if err != nil {
			klog.Fatal(err.Error())
		}

		slvModuleConfig := &d8commonapi.ModuleConfig{}

		err = cl.Get(ctx, types.NamespacedName{Name: sdsLocalVolumeModuleName, Namespace: ""}, slvModuleConfig)
		if err != nil {
			klog.Fatal(err)
		}

		if value, exists := slvModuleConfig.Spec.Settings["enableThinProvisioning"]; exists && value == true {
			klog.Info("Thin pools support is enabled")
		} else {
			klog.Info("Enabling thin pools support")
			patchBytes, err := json.Marshal(map[string]interface{}{
				"spec": map[string]interface{}{
					"version": 1,
					"settings": map[string]interface{}{
						"enableThinProvisioning": true,
					},
				},
			})

			if err != nil {
				klog.Fatalf("Error marshalling patch: %s", err.Error())
			}

			err = cl.Patch(context.TODO(), slvModuleConfig, client.RawPatch(types.MergePatchType, patchBytes))
			if err != nil {
				klog.Fatalf("Error patching object: %s", err.Error())
			}
		}
	}

	if thinExists && lsc.Spec.LVM.Type == "Thick" {
		errMsg = fmt.Sprintf("There must be only thick pools with Thick LVM type. Found: %s.", strings.Join(thinNames, ", "))
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
			nil
	}

	if thickExists && lsc.Spec.LVM.Type == "Thin" {
		errMsg = fmt.Sprintf("There must be only thin pools with Thin LVM type. Found: %s.", strings.Join(thickNames, ", "))
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg},
			nil
	}

	if thickExists && thinExists {
		errMsg = "There must be only thin or thick pools simultaneously"
		klog.Info(errMsg)
		return &kwhvalidating.ValidatorResult{Valid: false, Message: errMsg}, nil
	}
	return &kwhvalidating.ValidatorResult{Valid: true}, nil
}
