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

package validators

import (
	"context"
	"webhooks/v1alpha1"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func StorageClassUpdate(ctx context.Context, _ *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
	sc, ok := obj.(*v1alpha1.LocalStorageClass)
	if !ok {
		// If not a storage class just continue the validation chain(if there is one) and do nothing.
		return &kwhvalidating.ValidatorResult{}, nil
	}

	thickExists := false
	thinExists := false
	for _, lvmGroup := range sc.Spec.LVM.LVMVolumeGroups {
		if lvmGroup.Thin == nil {
			thickExists = true
		} else {
			thinExists = true
		}
	}

	if thinExists && sc.Spec.LVM.Type == "Thick" {
		return &kwhvalidating.ValidatorResult{Valid: false, Message: "there must be only thick pools with Thick LVM type"},
			nil
	}

	if thickExists && sc.Spec.LVM.Type == "Thin" {
		return &kwhvalidating.ValidatorResult{Valid: false, Message: "there must be only thin pools with Thin LVM type"},
			nil
	}

	if thickExists == true && thinExists == true {
		return &kwhvalidating.ValidatorResult{Valid: false, Message: "there must be only thin or thick pools simultaneously"},
			nil
	} else {
		return &kwhvalidating.ValidatorResult{Valid: true},
			nil
	}
}
