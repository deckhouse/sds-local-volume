package validators

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
	csiEndpoint               = "lvm.csi.storage.deckhouse.io"
	schedulerName             = "sds-lvm"
)

func PodSchedulerMutation(ctx context.Context, _ *model.AdmissionReview, obj metav1.Object) (*kwhmutating.MutatorResult, error) {
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
