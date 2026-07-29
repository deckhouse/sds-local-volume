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
	"reflect"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/deckhouse/sds-local-volume/images/webhooks/pkg/logger"
)

const (
	localCSIProvisioner = "local.csi.storage.deckhouse.io"
	allowedUserName     = "system:serviceaccount:d8-sds-local-volume:controller"
)

// SCValidate returns the StorageClass validator, bound to log.
func SCValidate(log logger.Logger) kwhvalidating.ValidatorFunc {
	log = log.Named("sc-validator")

	return func(_ context.Context, arReview *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
		sc, ok := obj.(*storagev1.StorageClass)
		if !ok {
			// If not a storage class just continue the validation chain(if there is one) and do nothing.
			return &kwhvalidating.ValidatorResult{}, nil
		}

		if sc.Provisioner == localCSIProvisioner {
			if arReview.UserInfo.Username == allowedUserName {
				log.Info("the user is allowed to manage StorageClasses of this provisioner", "user", arReview.UserInfo.Username, "provisioner", localCSIProvisioner)
				return &kwhvalidating.ValidatorResult{Valid: true}, nil
			}
			if arReview.Operation == model.OperationUpdate {
				changed, err := isStorageClassChangedExceptAnnotations(log, arReview.OldObjectRaw, arReview.NewObjectRaw)
				if err != nil {
					return nil, err
				}

				if !changed {
					log.Info("the user is allowed to change annotations of StorageClasses of this provisioner", "user", arReview.UserInfo.Username, "provisioner", localCSIProvisioner)
					return &kwhvalidating.ValidatorResult{Valid: true}, nil
				}
			}

			log.Info("the user is not allowed to manage StorageClasses of this provisioner", "user", arReview.UserInfo.Username, "provisioner", localCSIProvisioner)
			return &kwhvalidating.ValidatorResult{
				Valid:   false,
				Message: fmt.Sprintf("Direct modifications to the StorageClass (other than annotations) with the provisioner %s are not allowed. Please use LocalStorageClass for such operations.", localCSIProvisioner),
			}, nil
		}

		return &kwhvalidating.ValidatorResult{Valid: true}, nil
	}
}

func isStorageClassChangedExceptAnnotations(log logger.Logger, oldObjectRaw, newObjectRaw []byte) (bool, error) {
	var oldSC, newSC storagev1.StorageClass

	if err := json.Unmarshal(oldObjectRaw, &oldSC); err != nil {
		err := fmt.Errorf("failed to unmarshal old object: %v", err)
		return false, err
	}

	if err := json.Unmarshal(newObjectRaw, &newSC); err != nil {
		err := fmt.Errorf("failed to unmarshal new object: %v", err)
		return false, err
	}

	log.Info("=====================================")
	log.Debug("comparing the StorageClass revisions", "old", fmt.Sprintf("%+v", oldSC), "new", fmt.Sprintf("%+v", newSC))
	log.Info("=====================================")

	log.Info("=====================================")

	if oldSC.Provisioner != newSC.Provisioner {
		log.Info("the provisioner changed", "field", "provisioner", "old", oldSC.Provisioner, "new", newSC.Provisioner)
		return true, nil
	}

	if *oldSC.VolumeBindingMode != *newSC.VolumeBindingMode {
		log.Info("the volume binding mode changed", "field", "volumeBindingMode", "old", *oldSC.VolumeBindingMode, "new", *newSC.VolumeBindingMode)
		return true, nil
	}

	if *oldSC.ReclaimPolicy != *newSC.ReclaimPolicy {
		log.Info("the reclaim policy changed", "field", "reclaimPolicy", "old", *oldSC.ReclaimPolicy, "new", *newSC.ReclaimPolicy)
		return true, nil
	}

	if !reflect.DeepEqual(oldSC.Parameters, newSC.Parameters) {
		log.Info("the parameters changed", "field", "parameters", "old", fmt.Sprintf("%v", oldSC.Parameters), "new", fmt.Sprintf("%v", newSC.Parameters))
		return true, nil
	}

	if *oldSC.AllowVolumeExpansion != *newSC.AllowVolumeExpansion {
		log.Info("allowVolumeExpansion changed", "field", "allowVolumeExpansion", "old", *oldSC.AllowVolumeExpansion, "new", *newSC.AllowVolumeExpansion)
		return true, nil
	}

	if !reflect.DeepEqual(oldSC.MountOptions, newSC.MountOptions) {
		log.Info("the mount options changed", "field", "mountOptions", "old", fmt.Sprintf("%v", oldSC.MountOptions), "new", fmt.Sprintf("%v", newSC.MountOptions))
		return true, nil
	}

	if !reflect.DeepEqual(oldSC.AllowedTopologies, newSC.AllowedTopologies) {
		log.Info("the allowed topologies changed", "field", "allowedTopologies", "old", fmt.Sprintf("%v", oldSC.AllowedTopologies), "new", fmt.Sprintf("%v", newSC.AllowedTopologies))
		return true, nil
	}

	newSC.Annotations = nil
	oldSC.Annotations = nil

	newSC.ManagedFields = nil
	oldSC.ManagedFields = nil

	if !reflect.DeepEqual(oldSC.ObjectMeta, newSC.ObjectMeta) {
		log.Info("the object metadata changed", "field", "objectMeta", "old", fmt.Sprintf("%+v", oldSC.ObjectMeta), "new", fmt.Sprintf("%+v", newSC.ObjectMeta))
		return true, nil
	}

	return false, nil
}
