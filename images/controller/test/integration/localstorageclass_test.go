//go:build integration

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

package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const poolLabelKey = "storage.deckhouse.io/pool"

func makeLVG(ctx SpecContext, name, node string, labels map[string]string) {
	lvg := &snc.LVMVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: snc.LVMVolumeGroupSpec{
			Type:                  "Local",
			ActualVGNameOnTheNode: "vg-test",
			Local:                 snc.LVMVolumeGroupLocalSpec{NodeName: node},
			BlockDeviceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/hostname": node},
			},
		},
	}
	Expect(k8sClient.Create(ctx, lvg)).To(Succeed())
	DeferCleanup(func(ctx SpecContext) {
		_ = k8sClient.Delete(ctx, lvg)
	})
}

func makeLSC(ctx SpecContext, name, lvmType string, entries []slv.LocalStorageClassLVG) *slv.LocalStorageClass {
	lsc := &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: slv.LocalStorageClassSpec{
			ReclaimPolicy:     "Delete",
			VolumeBindingMode: "WaitForFirstConsumer",
			LVM: &slv.LocalStorageClassLVMSpec{
				Type:            lvmType,
				LVMVolumeGroups: entries,
			},
		},
	}
	return lsc
}

func scParam(ctx SpecContext, name string) (string, error) {
	sc := &v1.StorageClass{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, sc); err != nil {
		return "", err
	}
	return sc.Parameters[controller.LVMVolumeGroupsParamKey], nil
}

var _ = Describe("LocalStorageClass lvmVolumeGroups", func() {
	AfterEach(func(ctx SpecContext) {
		// Delete any LSC created by the spec; the controller removes the
		// managed StorageClass through its finalizer.
		lscList := &slv.LocalStorageClassList{}
		Expect(k8sClient.List(ctx, lscList)).To(Succeed())
		for i := range lscList.Items {
			_ = k8sClient.Delete(ctx, &lscList.Items[i])
		}
	})

	It("creates a StorageClass from explicit name entries", func(ctx SpecContext) {
		makeLVG(ctx, "it-name-a", "node-a", nil)
		makeLVG(ctx, "it-name-b", "node-b", nil)

		lsc := makeLSC(ctx, "it-lsc-name", controller.LVMThickType, []slv.LocalStorageClassLVG{
			{Name: "it-name-a"},
			{Name: "it-name-b"},
		})
		Expect(k8sClient.Create(ctx, lsc)).To(Succeed())

		Eventually(func(ctx SpecContext) (string, error) {
			return scParam(ctx, "it-lsc-name")
		}).WithContext(ctx).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal("- name: it-name-a\n- name: it-name-b\n"))
	})

	It("expands a labelSelector entry into the matching LVMVolumeGroups (sorted)", func(ctx SpecContext) {
		makeLVG(ctx, "it-sel-a", "node-a", map[string]string{poolLabelKey: "local"})
		makeLVG(ctx, "it-sel-b", "node-b", map[string]string{poolLabelKey: "local"})
		makeLVG(ctx, "it-sel-c", "node-c", nil) // unmatched

		lsc := makeLSC(ctx, "it-lsc-sel", controller.LVMThickType, []slv.LocalStorageClassLVG{
			{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{poolLabelKey: "local"}}},
		})
		Expect(k8sClient.Create(ctx, lsc)).To(Succeed())

		Eventually(func(ctx SpecContext) (string, error) {
			return scParam(ctx, "it-lsc-sel")
		}).WithContext(ctx).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal("- name: it-sel-a\n- name: it-sel-b\n"))
	})

	It("re-resolves the StorageClass when a new matching LVMVolumeGroup appears (watch)", func(ctx SpecContext) {
		makeLVG(ctx, "it-dyn-a", "node-a", map[string]string{poolLabelKey: "dyn"})

		lsc := makeLSC(ctx, "it-lsc-dyn", controller.LVMThickType, []slv.LocalStorageClassLVG{
			{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{poolLabelKey: "dyn"}}},
		})
		Expect(k8sClient.Create(ctx, lsc)).To(Succeed())

		Eventually(func(ctx SpecContext) (string, error) {
			return scParam(ctx, "it-lsc-dyn")
		}).WithContext(ctx).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal("- name: it-dyn-a\n"))

		// A second matching LVG appears — the LVMVolumeGroup watch must widen the SC.
		makeLVG(ctx, "it-dyn-b", "node-b", map[string]string{poolLabelKey: "dyn"})

		Eventually(func(ctx SpecContext) (string, error) {
			return scParam(ctx, "it-lsc-dyn")
		}).WithContext(ctx).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal("- name: it-dyn-a\n- name: it-dyn-b\n"))

		// The label is removed from it-dyn-b — the watch must shrink the SC back.
		var lvgB snc.LVMVolumeGroup
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "it-dyn-b"}, &lvgB)).To(Succeed())
		lvgB.Labels = map[string]string{}
		Expect(k8sClient.Update(ctx, &lvgB)).To(Succeed())

		Eventually(func(ctx SpecContext) (string, error) {
			return scParam(ctx, "it-lsc-dyn")
		}).WithContext(ctx).WithTimeout(eventuallyTimeout).WithPolling(eventuallyInterval).
			Should(Equal("- name: it-dyn-a\n"))
	})

	It("rejects a LocalStorageClass that mixes name and labelSelector entries", func(ctx SpecContext) {
		lsc := makeLSC(ctx, "it-lsc-mix", controller.LVMThickType, []slv.LocalStorageClassLVG{
			{Name: "it-name-a"},
			{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{poolLabelKey: "local"}}},
		})
		err := k8sClient.Create(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("rejects an entry that sets neither name nor labelSelector", func(ctx SpecContext) {
		lsc := makeLSC(ctx, "it-lsc-empty-entry", controller.LVMThickType, []slv.LocalStorageClassLVG{
			{},
		})
		err := k8sClient.Create(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})
