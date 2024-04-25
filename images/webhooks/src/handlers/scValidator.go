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

	"k8s.io/klog/v2"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	localCSIProvisioner = "local.csi.storage.deckhouse.io"
	allowedUserName     = "system:serviceaccount:d8-sds-local-volume:sds-local-volume-controller"
)

func SCValidate(ctx context.Context, arReview *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
	sc, ok := obj.(*storagev1.StorageClass)
	if !ok {
		// If not a storage class just continue the validation chain(if there is one) and do nothing.
		return &kwhvalidating.ValidatorResult{}, nil
	}

	if sc.Provisioner == localCSIProvisioner {
		if arReview.UserInfo.Username == allowedUserName {
			klog.Infof("User %s is allowed to manage storage classes with provisioner %s", arReview.UserInfo.Username, localCSIProvisioner)
			return &kwhvalidating.ValidatorResult{Valid: true},
				nil
		} else {
			klog.Infof("User %s is not allowed to manage storage classes with provisioner %s", arReview.UserInfo.Username, localCSIProvisioner)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: fmt.Sprintf("Manual operations with StorageClass with provisioner %s are not allowed. Please use LocalStorageClass instead.", localCSIProvisioner)},
				nil
		}
	} else {
		return &kwhvalidating.ValidatorResult{Valid: true},
			nil
	}

}
