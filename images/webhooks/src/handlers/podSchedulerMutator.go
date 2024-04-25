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

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhmutating "github.com/slok/kubewebhook/v2/pkg/webhook/mutating"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	annBetaStorageProvisioner = "volume.beta.kubernetes.io/storage-provisioner"
	annStorageProvisioner     = "volume.kubernetes.io/storage-provisioner"
	csiEndpoint               = "local.csi.storage.deckhouse.io"
	schedulerName             = "sds-local-volume"
)

func PodSchedulerMutate(ctx context.Context, _ *model.AdmissionReview, obj metav1.Object) (*kwhmutating.MutatorResult, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		// If not a pod just continue the mutation chain(if there is one) and do nothing.
		return &kwhmutating.MutatorResult{}, nil
	}

	if pod.Spec.SchedulerName == "" || pod.Spec.SchedulerName == "default-scheduler" {
		config, err := rest.InClusterConfig()
		if err != nil {
			return &kwhmutating.MutatorResult{}, err
		}

		staticClient, err := kubernetes.NewForConfig(config)
		if err != nil {
			return &kwhmutating.MutatorResult{}, err
		}

		for _, currentVolume := range pod.Spec.Volumes {
			var discoveredProvisioner string

			if currentVolume.PersistentVolumeClaim == nil {
				continue
			}

			pvc, err := staticClient.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, currentVolume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
			if err != nil {
				return &kwhmutating.MutatorResult{}, err
			}

			// Try to gather provisioner name from annotations
			if pvc != nil {
				if provisioner, ok := pvc.Annotations[annStorageProvisioner]; ok {
					discoveredProvisioner = provisioner
				}
				if provisioner, ok := pvc.Annotations[annBetaStorageProvisioner]; ok {
					discoveredProvisioner = provisioner
				}
			}
			// Try to gather provisioner name from associated StorageClass
			if discoveredProvisioner == "" && pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
				sc, err := staticClient.StorageV1().StorageClasses().Get(ctx, *pvc.Spec.StorageClassName, metav1.GetOptions{})
				if err != nil {
					return &kwhmutating.MutatorResult{}, err
				}

				if sc != nil && sc.Provisioner == csiEndpoint {
					discoveredProvisioner = sc.Provisioner
				}
			}
			// Try to gather provisioner name from associated PV
			if discoveredProvisioner == "" && pvc.Spec.VolumeName != "" {
				pv, err := staticClient.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
				if err != nil {
					return &kwhmutating.MutatorResult{}, err
				}
				if pv != nil && pv.Spec.CSI != nil {
					discoveredProvisioner = pv.Spec.CSI.Driver
				}
			}

			// Overwrite the scheduler name
			if discoveredProvisioner == csiEndpoint {
				pod.Spec.SchedulerName = schedulerName
				return &kwhmutating.MutatorResult{
					MutatedObject: pod,
				}, nil
			}
		}
	}

	return &kwhmutating.MutatorResult{
		MutatedObject: pod,
	}, nil
}
