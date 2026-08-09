//go:build integration

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

package integration

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	testVGName = "vg-integration"
	// Taken from the controller rather than restated, so that a driver name change
	// cannot leave these specs passing against a name nothing uses any more.
	testProvider = controller.LocalStorageClassProvisioner
)

// These specs run against a real API server, so a deletion really does block on
// the finalizers and the object really does disappear when the last one is
// removed — the behaviour the reported bug is about.
var _ = Describe("LVMLogicalVolume garbage collector", func() {
	AfterEach(func() {
		cleanupLLVs(suiteCtx)
		cleanupPVs(suiteCtx)
	})

	It("unblocks the deletion of a volume whose PersistentVolume is gone", func() {
		name := "pvc-orphan-reclaimed"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)

		By("Asking for the volume to be deleted, as the manual reclaim step does")
		deleteLLV(suiteCtx, name)

		// The API server keeps the object while any finalizer is on it. Before the
		// fix the CSI finalizer stayed forever, so this is the assertion that the
		// deletion is no longer stuck.
		Eventually(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSNodeConfiguratorFinalizer}),
				"the collector should remove only its own finalizer")

		By("Standing in for the sds-node-configurator agent finishing the lvremove")
		removeFinalizer(suiteCtx, name, controller.SDSNodeConfiguratorFinalizer)

		Eventually(func() bool {
			return llvGone(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(BeTrue(), "the resource should be gone once no finalizer is left")
	})

	It("leaves a volume nobody asked to delete alone", func() {
		name := "pvc-orphan-kept"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)

		// Retain means the data outlives the PersistentVolume until an administrator
		// asks for it to go. The collector reports such a volume, and nothing else.
		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(ContainElement(controller.SDSLocalVolumeCSIFinalizer))
	})

	It("keeps the finalizer while a PersistentVolume still refers to the volume", func() {
		name := "pvc-still-bound"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		createCSIPV(suiteCtx, name, name)

		deleteLLV(suiteCtx, name)

		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(ContainElement(controller.SDSLocalVolumeCSIFinalizer),
				"a volume backing a live PersistentVolume must never be unblocked")
	})

	It("unblocks a pending deletion as soon as the PersistentVolume is removed", func() {
		name := "pvc-deleted-later"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		createCSIPV(suiteCtx, name, name)

		deleteLLV(suiteCtx, name)
		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(3 * eventuallyInterval).
			Should(ContainElement(controller.SDSLocalVolumeCSIFinalizer))

		By("Deleting the PersistentVolume, which is what makes the volume an orphan")
		deletePV(suiteCtx, name)
		Eventually(func() bool {
			return pvGone(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(BeTrue(), "the PersistentVolume has to be really gone for the reclaim to be allowed")

		// The PersistentVolume watch has to turn this around without waiting for the
		// periodic sweep.
		Eventually(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSNodeConfiguratorFinalizer}))
	})

	It("recognises a PersistentVolume that refers to the volume only by its CSI handle", func() {
		name := "pvc-by-handle"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		createCSIPV(suiteCtx, "imported-pv", name)

		deleteLLV(suiteCtx, name)

		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(ContainElement(controller.SDSLocalVolumeCSIFinalizer))
	})

	It("refuses to unblock a volume that still has snapshots", func() {
		name := "pvc-snapshot-source"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		createLLVS(suiteCtx, "snapshot-of-"+name, name)
		DeferCleanup(func(ctx SpecContext) { cleanupLLVSs(ctx) })

		deleteLLV(suiteCtx, name)

		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(ContainElement(controller.SDSLocalVolumeCSIFinalizer),
				"removing the finalizer would destroy the volume the snapshots came from")
	})

	It("refuses to unblock a volume no sds-node-configurator agent owns", func() {
		name := "pvc-agentless"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer)

		deleteLLV(suiteCtx, name)

		// Removing the last finalizer would delete the only record of a logical
		// volume that may still exist on the node, which leaks worse than the bug
		// being fixed.
		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSLocalVolumeCSIFinalizer}))
	})

	It("ignores an LVMLogicalVolume the module does not own", func() {
		name := "hand-made"
		createLLV(suiteCtx, name, controller.SDSNodeConfiguratorFinalizer)

		deleteLLV(suiteCtx, name)

		// No CSI finalizer means the volume was not created by this module; its
		// deletion is between its owner and the agent.
		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSNodeConfiguratorFinalizer}))
	})

	It("leaves a volume acknowledged as retained alone, and still reclaims it when asked", func() {
		name := "pvc-acknowledged"
		createLLV(suiteCtx, name, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		annotateLLV(suiteCtx, name, controller.RetainAcknowledgedAnnotation, "true")

		// The annotation answers "is this a leak", it protects nothing.
		Consistently(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(3 * eventuallyInterval).
			Should(ContainElement(controller.SDSLocalVolumeCSIFinalizer))

		deleteLLV(suiteCtx, name)

		Eventually(func() ([]string, error) {
			return llvFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSNodeConfiguratorFinalizer}))
	})

	It("unblocks the deletion of a snapshot the same way", func() {
		name := "snapshot-orphan-reclaimed"
		createLLVS(suiteCtx, name, "pvc-any",
			controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		DeferCleanup(func(ctx SpecContext) { cleanupLLVSs(ctx) })

		deleteLLVS(suiteCtx, name)

		// No VolumeSnapshotContent CRD is installed in this suite, which is also the
		// shape of a cluster without snapshot-controller: the collector has to read
		// that as "nothing refers to the snapshot" rather than as a failure.
		Eventually(func() ([]string, error) {
			return llvsFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSNodeConfiguratorFinalizer}))
	})

	It("refuses to unblock a snapshot no sds-node-configurator agent owns", func() {
		name := "snapshot-agentless"
		createLLVS(suiteCtx, name, "pvc-any", controller.SDSLocalVolumeCSIFinalizer)
		DeferCleanup(func(ctx SpecContext) { cleanupLLVSs(ctx) })

		deleteLLVS(suiteCtx, name)

		Consistently(func() ([]string, error) {
			return llvsFinalizers(suiteCtx, name)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSLocalVolumeCSIFinalizer}))
	})

	// The transform itself is covered by a unit test; this is about the wiring around
	// it, which nothing else reaches. The manager restricts its default cache to the
	// controller namespace, and a PersistentVolume is cluster-scoped — so the entry
	// that gives it a cluster-wide cache of its own, and the transform that entry
	// carries, both have to survive controller-runtime's ByObject defaulting. If
	// either stopped working the reclaim would break with every other spec green.
	It("caches PersistentVolumes cluster-wide, stripped to what a sweep reads", func() {
		name := "pvc-cached"
		createCSIPV(suiteCtx, name, "handle-of-"+name)

		var cached corev1.PersistentVolume
		Eventually(func() error {
			return mgrCache.Get(suiteCtx, client.ObjectKey{Name: name}, &cached)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Succeed(), "a cluster-scoped PersistentVolume has to reach the cache despite DefaultNamespaces")

		// The two ways a PersistentVolume refers to an LVMLogicalVolume survive.
		Expect(cached.Name).To(Equal(name))
		Expect(cached.Spec.CSI).NotTo(BeNil())
		Expect(cached.Spec.CSI.Driver).To(Equal(testProvider))
		Expect(cached.Spec.CSI.VolumeHandle).To(Equal("handle-of-" + name))

		// And the fields the transform drops really are dropped, so the spec fails if
		// the transform stops being applied at all.
		Expect(cached.Spec.Capacity).To(BeEmpty())
		Expect(cached.Spec.PersistentVolumeReclaimPolicy).To(BeEmpty())
		Expect(cached.Status.Phase).To(BeEmpty())

		// The same object read through the manager's client, which is what the sweep
		// lists, has to agree.
		var listed corev1.PersistentVolumeList
		Expect(mgrCache.List(suiteCtx, &listed)).To(Succeed())
		Expect(listed.Items).To(HaveLen(1))
		Expect(listed.Items[0].Spec.CSI.VolumeHandle).To(Equal("handle-of-" + name))
	})

	It("reclaims a snapshot and then the volume it was blocking", func() {
		volume := "pvc-chain-source"
		snapshot := "snapshot-of-" + volume

		createLLV(suiteCtx, volume, controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		createLLVS(suiteCtx, snapshot, volume,
			controller.SDSLocalVolumeCSIFinalizer, controller.SDSNodeConfiguratorFinalizer)
		DeferCleanup(func(ctx SpecContext) { cleanupLLVSs(ctx) })

		By("Asking for both to be deleted, which is the documented reclaim order")
		deleteLLV(suiteCtx, volume)
		deleteLLVS(suiteCtx, snapshot)

		// Until the snapshot is really gone the source stays blocked: unblocking it
		// would destroy the volume the snapshot was taken from.
		Eventually(func() ([]string, error) {
			return llvsFinalizers(suiteCtx, snapshot)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSNodeConfiguratorFinalizer}))
		Expect(llvFinalizers(suiteCtx, volume)).To(ContainElement(controller.SDSLocalVolumeCSIFinalizer))

		By("Standing in for the agent removing the snapshot on the node")
		removeSnapshotFinalizer(suiteCtx, snapshot, controller.SDSNodeConfiguratorFinalizer)
		Eventually(func() bool {
			return llvsGone(suiteCtx, snapshot)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).Should(BeTrue())

		// This is the step that used to be impossible: before the snapshot was
		// collected too, "delete the snapshots first" hung on the same finalizer, so
		// the source could never leave snapshots_present.
		Eventually(func() ([]string, error) {
			return llvFinalizers(suiteCtx, volume)
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal([]string{controller.SDSNodeConfiguratorFinalizer}))
	})
})

func createLLV(ctx context.Context, name string, finalizers ...string) {
	GinkgoHelper()

	llv := &snc.LVMLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Finalizers: finalizers,
		},
		Spec: snc.LVMLogicalVolumeSpec{
			ActualLVNameOnTheNode: name,
			Type:                  controller.LVMThickType,
			Size:                  "1Gi",
			LVMVolumeGroupName:    testVGName,
		},
	}
	Expect(k8sClient.Create(ctx, llv)).To(Succeed())

	// The status is a subresource, so it takes a second write — the collector reads
	// the actual size from it for the metrics.
	llv.Status = &snc.LVMLogicalVolumeStatus{
		Phase:      "Created",
		ActualSize: resource.MustParse("1Gi"),
	}
	Expect(k8sClient.Status().Update(ctx, llv)).To(Succeed())
}

func createLLVS(ctx context.Context, name, sourceLLV string, finalizers ...string) {
	GinkgoHelper()

	Expect(k8sClient.Create(ctx, &snc.LVMLogicalVolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Finalizers: finalizers,
		},
		Spec: snc.LVMLogicalVolumeSnapshotSpec{
			ActualSnapshotNameOnTheNode: name,
			LVMLogicalVolumeName:        sourceLLV,
		},
	})).To(Succeed())
}

// annotateLLV puts an annotation on an existing volume, which is how an
// administrator acknowledges a Retain volume.
func annotateLLV(ctx context.Context, name, key, value string) {
	GinkgoHelper()

	llv := &snc.LVMLogicalVolume{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, llv)).To(Succeed())

	if llv.Annotations == nil {
		llv.Annotations = map[string]string{}
	}
	llv.Annotations[key] = value
	Expect(k8sClient.Update(ctx, llv)).To(Succeed())
}

// createCSIPV creates the PersistentVolume the provisioner would have created.
// name is the resource name and handle the CSI volume handle, which are the same
// string for a dynamically provisioned volume.
func createCSIPV(ctx context.Context, name, handle string) {
	GinkgoHelper()

	Expect(k8sClient.Create(ctx, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       testProvider,
					VolumeHandle: handle,
				},
			},
		},
	})).To(Succeed())
}

func deleteLLV(ctx context.Context, name string) {
	GinkgoHelper()
	Expect(k8sClient.Delete(ctx, &snc.LVMLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	})).To(Succeed())
}

func llvFinalizers(ctx context.Context, name string) ([]string, error) {
	llv := &snc.LVMLogicalVolume{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, llv); err != nil {
		return nil, err
	}
	return llv.Finalizers, nil
}

func llvGone(ctx context.Context, name string) bool {
	llv := &snc.LVMLogicalVolume{}
	return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Name: name}, llv))
}

func deleteLLVS(ctx context.Context, name string) {
	GinkgoHelper()
	Expect(k8sClient.Delete(ctx, &snc.LVMLogicalVolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	})).To(Succeed())
}

func llvsFinalizers(ctx context.Context, name string) ([]string, error) {
	llvs := &snc.LVMLogicalVolumeSnapshot{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, llvs); err != nil {
		return nil, err
	}
	return llvs.Finalizers, nil
}

func llvsGone(ctx context.Context, name string) bool {
	llvs := &snc.LVMLogicalVolumeSnapshot{}
	return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Name: name}, llvs))
}

// removeSnapshotFinalizer is removeFinalizer for a snapshot.
func removeSnapshotFinalizer(ctx context.Context, name, finalizer string) {
	GinkgoHelper()

	llvs := &snc.LVMLogicalVolumeSnapshot{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, llvs)).To(Succeed())

	kept := make([]string, 0, len(llvs.Finalizers))
	for _, f := range llvs.Finalizers {
		if f != finalizer {
			kept = append(kept, f)
		}
	}
	llvs.Finalizers = kept
	Expect(k8sClient.Update(ctx, llvs)).To(Succeed())
}

func pvGone(ctx context.Context, name string) bool {
	pv := &corev1.PersistentVolume{}
	return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Name: name}, pv))
}

// removeFinalizer stands in for whoever owns the given finalizer having finished
// its work.
func removeFinalizer(ctx context.Context, name, finalizer string) {
	GinkgoHelper()

	llv := &snc.LVMLogicalVolume{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, llv)).To(Succeed())

	kept := make([]string, 0, len(llv.Finalizers))
	for _, f := range llv.Finalizers {
		if f != finalizer {
			kept = append(kept, f)
		}
	}
	llv.Finalizers = kept
	Expect(k8sClient.Update(ctx, llv)).To(Succeed())
}

// cleanupLLVs drops every LVMLogicalVolume, finalizers included: a spec that
// asserts a deletion stays blocked leaves an object the next spec must not see.
//
// The collector is running against the same API server and writing to the same
// objects, so clearing the finalizers is retried rather than asserted in one go: a
// lost write here is a conflict, not a failure, and failing the suite from a
// cleanup is the worst place to debug a flake from.
func cleanupLLVs(ctx context.Context) {
	GinkgoHelper()

	list := &snc.LVMLogicalVolumeList{}
	Expect(k8sClient.List(ctx, list)).To(Succeed())

	for i := range list.Items {
		name := list.Items[i].Name

		Eventually(func() error {
			cur := &snc.LVMLogicalVolume{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, cur); err != nil {
				return client.IgnoreNotFound(err)
			}
			if len(cur.Finalizers) == 0 {
				return nil
			}
			cur.Finalizers = nil
			return client.IgnoreNotFound(k8sClient.Update(ctx, cur))
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Succeed(), fmt.Sprintf("clear finalizers of %s", name))

		if err := k8sClient.Delete(ctx, &snc.LVMLogicalVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}); err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("delete %s", name))
		}
	}

	Eventually(func() (int, error) {
		l := &snc.LVMLogicalVolumeList{}
		if err := k8sClient.List(ctx, l); err != nil {
			return -1, err
		}
		return len(l.Items), nil
	}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).Should(BeZero())
}

// cleanupLLVSs drops every LVMLogicalVolumeSnapshot, finalizers included, for the
// same reason as cleanupLLVs: a spec that asserts a deletion stays blocked leaves
// an object the next spec must not see, and the collector is writing to the same
// objects, so clearing them is retried rather than asserted in one go.
func cleanupLLVSs(ctx context.Context) {
	GinkgoHelper()

	list := &snc.LVMLogicalVolumeSnapshotList{}
	Expect(k8sClient.List(ctx, list)).To(Succeed())

	for i := range list.Items {
		name := list.Items[i].Name

		Eventually(func() error {
			cur := &snc.LVMLogicalVolumeSnapshot{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, cur); err != nil {
				return client.IgnoreNotFound(err)
			}
			if len(cur.Finalizers) == 0 {
				return nil
			}
			cur.Finalizers = nil
			return client.IgnoreNotFound(k8sClient.Update(ctx, cur))
		}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Succeed(), fmt.Sprintf("clear finalizers of %s", name))

		if err := k8sClient.Delete(ctx, &snc.LVMLogicalVolumeSnapshot{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}); err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("delete %s", name))
		}
	}

	Eventually(func() (int, error) {
		l := &snc.LVMLogicalVolumeSnapshotList{}
		if err := k8sClient.List(ctx, l); err != nil {
			return -1, err
		}
		return len(l.Items), nil
	}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).Should(BeZero())
}

// deletePV deletes a PersistentVolume and makes it actually disappear.
//
// The API server's StorageObjectInUseProtection admission plugin puts
// kubernetes.io/pv-protection on every PersistentVolume, and it is the
// pv-protection controller in kube-controller-manager that takes it off again once
// the volume is no longer in use. envtest runs no controller manager, so without
// this the object would sit in Terminating forever and the collector would keep
// treating the volume as referenced — which is what it is supposed to do.
func deletePV(ctx context.Context, name string) {
	GinkgoHelper()

	pv := &corev1.PersistentVolume{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, pv); err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), fmt.Sprintf("get %s: %v", name, err))
		return
	}

	if err := k8sClient.Delete(ctx, pv); err != nil && !apierrors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("delete %s", name))
	}

	Eventually(func() error {
		cur := &corev1.PersistentVolume{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, cur); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if len(cur.Finalizers) == 0 {
			return nil
		}
		cur.Finalizers = nil
		return k8sClient.Update(ctx, cur)
	}).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).Should(Succeed())
}

func cleanupPVs(ctx context.Context) {
	GinkgoHelper()

	list := &corev1.PersistentVolumeList{}
	Expect(k8sClient.List(ctx, list)).To(Succeed())
	for i := range list.Items {
		deletePV(ctx, list.Items[i].Name)
	}
}
