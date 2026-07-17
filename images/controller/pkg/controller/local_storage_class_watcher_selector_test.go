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

package controller_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
)

var _ = Describe("local-storage-class-controller lvmVolumeGroups labelSelector", func() {
	const (
		lscName = "sds-local-volume-selector-sc"

		thickLVGAName = "selector-thick-vg-a"
		thickLVGBName = "selector-thick-vg-b"
		thickLVGCName = "selector-thick-vg-c-unmatched"

		thinLVGAName = "selector-thin-vg-a"
		thinLVGBName = "selector-thin-vg-b"

		namedThickLVGName = "named-thick-vg"
	)

	var (
		cl  client.Client
		log = logger.NewLoggerFromLogr(GinkgoLogr)

		reclaimPolicyDelete   = string(corev1.PersistentVolumeReclaimDelete)
		volumeBindingModeWFFC = string(v1.VolumeBindingWaitForFirstConsumer)

		matchedLabels = map[string]string{"storage.deckhouse.io/pool": "local"}
	)

	reconcile := func(ctx SpecContext, lsc *slv.LocalStorageClass) (bool, error) {
		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())
		return controller.RunEventReconcile(ctx, cl, log, scList, lsc, nil)
	}

	selectorEntry := func(thinPoolName string) slv.LocalStorageClassLVG {
		entry := slv.LocalStorageClassLVG{
			LabelSelector: &metav1.LabelSelector{MatchLabels: matchedLabels},
		}
		if thinPoolName != "" {
			entry.Thin = &slv.LocalStorageClassLVMThinPoolSpec{PoolName: thinPoolName}
		}
		return entry
	}

	BeforeEach(func() {
		cl = NewFakeClient()
	})

	It("expands a Thick labelSelector entry into the matched LVMVolumeGroups sorted by name", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thickLVGAName, nil)
		lvgA.Labels = matchedLabels
		lvgB := generateLVMVolumeGroup(thickLVGBName, nil)
		lvgB.Labels = matchedLabels
		lvgC := generateLVMVolumeGroup(thickLVGCName, nil) // no matching labels

		Expect(cl.Create(ctx, lvgB)).To(Succeed())
		Expect(cl.Create(ctx, lvgA)).To(Succeed())
		Expect(cl.Create(ctx, lvgC)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{selectorEntry("")})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())

		performStandardChecksForSC(sc, []slv.LocalStorageClassLVG{
			{Name: thickLVGAName},
			{Name: thickLVGBName},
		}, lscName, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, "")
	})

	It("applies the entry thin poolName to every LVMVolumeGroup matched by a Thin labelSelector", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thinLVGAName, []string{"thin-pool-1"})
		lvgA.Labels = matchedLabels
		lvgB := generateLVMVolumeGroup(thinLVGBName, []string{"thin-pool-1"})
		lvgB.Labels = matchedLabels

		Expect(cl.Create(ctx, lvgA)).To(Succeed())
		Expect(cl.Create(ctx, lvgB)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThinType, []slv.LocalStorageClassLVG{selectorEntry("thin-pool-1")})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())

		performStandardChecksForSC(sc, []slv.LocalStorageClassLVG{
			{Name: thinLVGAName, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
			{Name: thinLVGBName, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
		}, lscName, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyDelete, volumeBindingModeWFFC, "")
	})

	It("fails when name-based and labelSelector-based entries are mixed in one list", func(ctx SpecContext) {
		named := generateLVMVolumeGroup(namedThickLVGName, nil)
		selected := generateLVMVolumeGroup(thickLVGAName, nil)
		selected.Labels = matchedLabels

		Expect(cl.Create(ctx, named)).To(Succeed())
		Expect(cl.Create(ctx, selected)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{
				{Name: namedThickLVGName},
				selectorEntry(""),
			})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status).NotTo(BeNil())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))
	})

	It("resolves a homogeneous list of multiple labelSelector entries", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thickLVGAName, nil)
		lvgA.Labels = map[string]string{"storage.deckhouse.io/test-pool": "a"}
		lvgB := generateLVMVolumeGroup(thickLVGBName, nil)
		lvgB.Labels = map[string]string{"storage.deckhouse.io/test-pool": "b"}

		Expect(cl.Create(ctx, lvgA)).To(Succeed())
		Expect(cl.Create(ctx, lvgB)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{
				{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"storage.deckhouse.io/test-pool": "a"}}},
				{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"storage.deckhouse.io/test-pool": "b"}}},
			})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		performStandardChecksForSC(sc, []slv.LocalStorageClassLVG{
			{Name: thickLVGAName},
			{Name: thickLVGBName},
		}, lscName, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, "")
	})

	It("collapses an LVMVolumeGroup matched by overlapping selectors (Thick)", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thickLVGAName, nil)
		lvgA.Labels = map[string]string{
			"storage.deckhouse.io/test-pool": "a",
			"storage.deckhouse.io/tier":      "fast",
		}
		Expect(cl.Create(ctx, lvgA)).To(Succeed())

		// Both selectors match lvgA; for Thick both resolve to the same entry,
		// so it is collapsed rather than treated as a conflict.
		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{
				{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"storage.deckhouse.io/test-pool": "a"}}},
				{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"storage.deckhouse.io/tier": "fast"}}},
			})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		performStandardChecksForSC(sc, []slv.LocalStorageClassLVG{
			{Name: thickLVGAName},
		}, lscName, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, "")
	})

	It("fails when overlapping selectors resolve one LVMVolumeGroup with different thin pools", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thinLVGAName, []string{"thin-pool-1", "thin-pool-2"})
		lvgA.Labels = map[string]string{
			"storage.deckhouse.io/test-pool": "a",
			"storage.deckhouse.io/tier":      "fast",
		}
		Expect(cl.Create(ctx, lvgA)).To(Succeed())

		// Both selectors match lvgA but assign it different thin pools — ambiguous.
		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThinType, []slv.LocalStorageClassLVG{
				{
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"storage.deckhouse.io/test-pool": "a"}},
					Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"},
				},
				{
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"storage.deckhouse.io/tier": "fast"}},
					Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-2"},
				},
			})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status).NotTo(BeNil())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))
	})

	It("re-resolves the StorageClass when a newly labeled LVMVolumeGroup starts matching", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thickLVGAName, nil)
		lvgA.Labels = matchedLabels
		Expect(cl.Create(ctx, lvgA)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{selectorEntry("")})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		_, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		Expect(sc.Parameters[controller.LVMVolumeGroupsParamKey]).To(Equal("- name: " + thickLVGAName + "\n"))

		// A second matching LVG appears; reconciling again must widen the SC.
		lvgB := generateLVMVolumeGroup(thickLVGBName, nil)
		lvgB.Labels = matchedLabels
		Expect(cl.Create(ctx, lvgB)).To(Succeed())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		Expect(sc.Parameters[controller.LVMVolumeGroupsParamKey]).To(Equal("- name: " + thickLVGAName + "\n- name: " + thickLVGBName + "\n"))
	})

	It("fails when a labelSelector entry matches no LVMVolumeGroups", func(ctx SpecContext) {
		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{selectorEntry("")})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status).NotTo(BeNil())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))
	})

	It("recovers from Failed to Created when a matching LVMVolumeGroup appears", func(ctx SpecContext) {
		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{selectorEntry("")})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))

		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())
		Expect(scList.Items).To(BeEmpty())

		lvgA := generateLVMVolumeGroup(thickLVGAName, nil)
		lvgA.Labels = matchedLabels
		Expect(cl.Create(ctx, lvgA)).To(Succeed())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		shouldRequeue, err = reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		Expect(sc.Parameters[controller.LVMVolumeGroupsParamKey]).To(Equal("- name: " + thickLVGAName + "\n"))
	})

	It("recovers from Failed to Created after a temporary empty match set when the StorageClass already exists", func(ctx SpecContext) {
		// Healthy Created state with an SC first.
		lvgA := generateLVMVolumeGroup(thickLVGAName, nil)
		lvgA.Labels = matchedLabels
		Expect(cl.Create(ctx, lvgA)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{selectorEntry("")})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		_, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))

		// Relabel away so the selector matches nothing: validation fails, phase
		// becomes Failed, but the StorageClass is left in place (update path
		// fails before recreate).
		Expect(cl.Get(ctx, client.ObjectKey{Name: thickLVGAName}, lvgA)).To(Succeed())
		lvgA.Labels = nil
		Expect(cl.Update(ctx, lvgA)).To(Succeed())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())

		// Restore the label: match set is healthy again. Even with no SC param
		// diff, Failed phase must force an update reconcile that clears Failed.
		Expect(cl.Get(ctx, client.ObjectKey{Name: thickLVGAName}, lvgA)).To(Succeed())
		lvgA.Labels = matchedLabels
		Expect(cl.Update(ctx, lvgA)).To(Succeed())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		shouldRequeue, err = reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		Expect(sc.Parameters[controller.LVMVolumeGroupsParamKey]).To(Equal("- name: " + thickLVGAName + "\n"))
	})

	It("fails when an entry sets both name and labelSelector", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thickLVGAName, nil)
		lvgA.Labels = matchedLabels
		Expect(cl.Create(ctx, lvgA)).To(Succeed())

		entry := selectorEntry("")
		entry.Name = thickLVGAName

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThickType, []slv.LocalStorageClassLVG{entry})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status).NotTo(BeNil())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))
	})

	It("fails a Thin labelSelector entry without a thin poolName", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(thinLVGAName, []string{"thin-pool-1"})
		lvgA.Labels = matchedLabels
		Expect(cl.Create(ctx, lvgA)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC,
			controller.LVMThinType, []slv.LocalStorageClassLVG{selectorEntry("")})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status).NotTo(BeNil())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))
	})
})
