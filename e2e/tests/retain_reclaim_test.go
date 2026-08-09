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

package tests

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

const (
	// csiFinalizer is put on an LVMLogicalVolume by the CSI driver and is the one
	// the controller has to remove once the PersistentVolume is gone.
	csiFinalizer = "storage.deckhouse.io/sds-local-volume-csi"

	// nodeConfiguratorFinalizer is put on an LVMLogicalVolume by the
	// sds-node-configurator agent, which removes it only after lvremove has
	// succeeded. An LVMLogicalVolume disappearing is therefore proof that the
	// logical volume was really removed and its space returned to the volume group.
	nodeConfiguratorFinalizer = "storage.deckhouse.io/sds-node-configurator"

	// llvReclaimTimeout covers the controller grace period plus the agent lvremove.
	llvReclaimTimeout = 3 * time.Minute

	// retainWindow is how long a volume is watched to prove that nothing deletes
	// it on its own.
	retainWindow = 45 * time.Second

	pvGoneTimeout = 2 * time.Minute

	// controllerNamespace and controllerServiceAccount are where the module's
	// controller runs; its ClusterRole is what the reclaim needs the patch verb from.
	controllerNamespace      = "d8-sds-local-volume"
	controllerServiceAccount = "controller"
)

// lvmLogicalVolumeGVR is the sds-node-configurator LVMLogicalVolume resource.
var lvmLogicalVolumeGVR = storagekube.LocalStorageClassGVR.GroupVersion().WithResource("lvmlogicalvolumes")

var _ = Describe("sds-local-volume Retain reclaim", Ordered, func() {
	const scName = "e2e-lsc-retain"

	// The reclaim is a merge patch of metadata.finalizers, so the controller needs the
	// patch verb; the authorizer derives the verb from the HTTP method, and a
	// ClusterRole granting only update would deny every write while answering "yes" to
	// a `can-i update` check. This is the only layer that sees RBAC at all: the unit
	// specs use a fake client and envtest runs as its own admin, so a ClusterRole that
	// drifts from the write path is invisible everywhere else. Asserted before the
	// specs below, so that a failure names the cause instead of showing up as a
	// deletion that never completes.
	It("grants the controller the verb its write path uses", func(ctx SpecContext) {
		for _, resource := range []string{"lvmlogicalvolumes", "lvmlogicalvolumesnapshots"} {
			Expect(controllerCan(ctx, "patch", resource)).To(BeTrue(),
				"the controller ServiceAccount must be allowed to patch %s", resource)
		}
	})

	BeforeAll(func(ctx SpecContext) {
		By("Creating a Retain LocalStorageClass")
		Expect(storagekube.CreateLocalStorageClass(ctx, suiteRestCfg, storagekube.LocalStorageClassConfig{
			Name:            scName,
			LVMVolumeGroups: []string{suiteLVGs[0]},
			LVMType:         "Thick",
			ReclaimPolicy:   "Retain",
		})).To(Succeed())
		Expect(storagekube.WaitForLocalStorageClassCreated(ctx, suiteRestCfg, scName, lscCreatedTimeout)).To(Succeed())
		assertStorageClassExists(ctx, scName)
	})

	AfterAll(func(ctx SpecContext) {
		deleteLSC(ctx, scName)
	})

	// This is the flow from the bug report: with reclaimPolicy: Retain, deleting the
	// PersistentVolume never reaches the CSI driver, so nothing removes the CSI
	// finalizer. Before the fix the LVMLogicalVolume stayed in Terminating forever
	// and its space stayed allocated in the volume group.
	It("reclaims an LVMLogicalVolume whose Retain PersistentVolume was deleted", func(ctx SpecContext) {
		const prefix = "e2e-retain"
		pvcName, podName := prefix+"-pvc", prefix+"-pod"

		By("Provisioning a volume through the Retain StorageClass")
		Expect(suiteK8s.Create(ctx, buildPVC(pvcName, scName))).To(Succeed())
		Expect(suiteK8s.Create(ctx, buildPod(podName, pvcName))).To(Succeed())
		waitPodRunning(ctx, podName)

		pvName := boundPVName(ctx, pvcName)
		// The CSI driver names the LVMLogicalVolume after the volume ID, which for a
		// dynamically provisioned volume is also the PersistentVolume name.
		llvName := pvName
		// Both owners have to be on the object for the bug to be reproducible: the
		// CSI finalizer is the one that never gets removed, and the agent one is what
		// makes the agent wait for it.
		Expect(llvFinalizers(ctx, llvName)).To(ContainElements(csiFinalizer, nodeConfiguratorFinalizer))

		By("Releasing the PersistentVolume, which Retain leaves behind")
		deletePVCAndPod(ctx, pvcName, podName)
		Eventually(func() (corev1.PersistentVolumePhase, error) {
			return pvPhase(ctx, pvName)
		}).WithTimeout(pvGoneTimeout).WithPolling(pollInterval).
			Should(Equal(corev1.VolumeReleased), "a Retain PersistentVolume should end up Released")

		By("Deleting the released PersistentVolume, as an administrator reclaiming the storage would")
		Expect(suiteK8s.Delete(ctx, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: pvName}})).To(Succeed())
		Eventually(func() bool {
			return pvGone(ctx, pvName)
		}).WithTimeout(pvGoneTimeout).WithPolling(pollInterval).
			Should(BeTrue(), "the PersistentVolume should be gone")

		By("Checking that the data is not thrown away with the PersistentVolume")
		// Retain means the volume outlives its PersistentVolume until somebody asks
		// for it to go. The controller only reports it; it must not delete it.
		Consistently(func() (bool, error) {
			return llvExists(ctx, llvName)
		}).WithTimeout(retainWindow).WithPolling(pollInterval).
			Should(BeTrue(), "the LVMLogicalVolume must survive the PersistentVolume")

		By("Deleting the LVMLogicalVolume, the documented manual reclaim step")
		Expect(deleteLLV(ctx, llvName)).To(Succeed())

		// The resource going away means sds-node-configurator ran lvremove and then
		// dropped its own finalizer: the space is back in the volume group. Before
		// the fix this hung on the CSI finalizer indefinitely.
		Eventually(func() (bool, error) {
			return llvGone(ctx, llvName)
		}).WithTimeout(llvReclaimTimeout).WithPolling(pollInterval).
			Should(BeTrue(), "d8 k delete lvmlogicalvolume should complete on its own")
	})

	// The dangerous direction: the collector must never take its finalizer off a
	// volume that still backs a live PersistentVolume, however the deletion was
	// asked for.
	It("keeps the CSI finalizer while the PersistentVolume still exists", func(ctx SpecContext) {
		const prefix = "e2e-retain-live"
		pvcName, podName := prefix+"-pvc", prefix+"-pod"

		By("Provisioning a volume through the Retain StorageClass")
		Expect(suiteK8s.Create(ctx, buildPVC(pvcName, scName))).To(Succeed())
		Expect(suiteK8s.Create(ctx, buildPod(podName, pvcName))).To(Succeed())
		waitPodRunning(ctx, podName)

		pvName := boundPVName(ctx, pvcName)
		llvName := pvName

		By("Asking for the LVMLogicalVolume to be deleted while its PersistentVolume is alive")
		Expect(deleteLLV(ctx, llvName)).To(Succeed())

		Consistently(func() ([]string, error) {
			return llvFinalizers(ctx, llvName)
		}).WithTimeout(retainWindow).WithPolling(pollInterval).
			Should(ContainElement(csiFinalizer), "a volume backing a live PersistentVolume must stay blocked")

		By("Removing the PersistentVolume and watching the reclaim unblock itself")
		deletePVCAndPod(ctx, pvcName, podName)
		if err := suiteK8s.Delete(ctx, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: pvName}}); err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "delete PersistentVolume %s: %v", pvName, err)
		}

		Eventually(func() (bool, error) {
			return llvGone(ctx, llvName)
		}).WithTimeout(llvReclaimTimeout+pvGoneTimeout).WithPolling(pollInterval).
			Should(BeTrue(), "the pending deletion should complete once the PersistentVolume is gone")
	})
})

// controllerCan asks the API server whether the module's controller ServiceAccount
// may perform verb on a storage.deckhouse.io resource, which is what the runbook of
// D8SdsLocalVolumeCSIFinalizerRemovalErrors tells an operator to check by hand.
func controllerCan(ctx context.Context, verb, resource string) (bool, error) {
	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: "system:serviceaccount:" + controllerNamespace + ":" + controllerServiceAccount,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:    "storage.deckhouse.io",
				Resource: resource,
				Verb:     verb,
			},
		},
	}
	if err := suiteK8s.Create(ctx, review); err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}

func waitPodRunning(ctx context.Context, podName string) {
	GinkgoHelper()
	Eventually(func() (corev1.PodPhase, error) {
		var pod corev1.Pod
		if err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: podName}, &pod); err != nil {
			return "", err
		}
		return pod.Status.Phase, nil
	}).WithTimeout(pvcBindTimeout+podRunningTimeout).WithPolling(pollInterval).
		Should(Equal(corev1.PodRunning), "consumer Pod should reach Running")
}

// boundPVName returns the PersistentVolume a PVC is bound to.
func boundPVName(ctx context.Context, pvcName string) string {
	GinkgoHelper()
	var pvc corev1.PersistentVolumeClaim
	Expect(suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: pvcName}, &pvc)).To(Succeed())
	Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound), "PVC %s should be Bound", pvcName)
	Expect(pvc.Spec.VolumeName).NotTo(BeEmpty(), "PVC %s should name its PersistentVolume", pvcName)
	return pvc.Spec.VolumeName
}

func pvPhase(ctx context.Context, pvName string) (corev1.PersistentVolumePhase, error) {
	var pv corev1.PersistentVolume
	if err := suiteK8s.Get(ctx, client.ObjectKey{Name: pvName}, &pv); err != nil {
		return "", err
	}
	return pv.Status.Phase, nil
}

func pvGone(ctx context.Context, pvName string) bool {
	var pv corev1.PersistentVolume
	return apierrors.IsNotFound(suiteK8s.Get(ctx, client.ObjectKey{Name: pvName}, &pv))
}

func llvExists(ctx context.Context, name string) (bool, error) {
	_, err := suiteDyn.Resource(lvmLogicalVolumeGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// llvGone distinguishes "the resource is really gone" from "the API server could
// not be asked". Treating any error as absence would let a throttled or timed-out
// request satisfy the assertion that the reclaim completed, which is the one
// assertion these specs exist for.
func llvGone(ctx context.Context, name string) (bool, error) {
	exists, err := llvExists(ctx, name)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// llvFinalizers reads the finalizers of an LVMLogicalVolume.
func llvFinalizers(ctx context.Context, name string) ([]string, error) {
	llv, err := suiteDyn.Resource(lvmLogicalVolumeGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return llv.GetFinalizers(), nil
}

// deleteLLV asks for an LVMLogicalVolume to be deleted without waiting: the whole
// point of the specs is what happens to an object that stays in Terminating.
func deleteLLV(ctx context.Context, name string) error {
	err := suiteDyn.Resource(lvmLogicalVolumeGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
