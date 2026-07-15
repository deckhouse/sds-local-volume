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
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

var _ = Describe("local-storage-class-controller labelSelector (matchExpressions, shrink, conflicts)", func() {
	const (
		lscName  = "sds-local-volume-selector-more"
		tierKey  = "storage.deckhouse.io/tier"
		poolKey  = "storage.deckhouse.io/pool"
		poolVal  = "local"
		vgFast   = "more-vg-fast"
		vgSlow   = "more-vg-slow"
		vgThin   = "more-vg-thin"
		nodeName = "shared-node"
	)

	var (
		cl  client.Client
		log = logger.NewLoggerFromLogr(GinkgoLogr)

		rp  = string(corev1.PersistentVolumeReclaimDelete)
		vbm = string(v1.VolumeBindingWaitForFirstConsumer)
	)

	reconcile := func(ctx SpecContext, lsc *slv.LocalStorageClass) (bool, error) {
		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())
		return controller.RunEventReconcile(ctx, cl, log, scList, lsc, nil)
	}

	scParam := func(ctx SpecContext) string {
		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		return sc.Parameters[controller.LVMVolumeGroupsParamKey]
	}

	BeforeEach(func() {
		cl = NewFakeClient()
	})

	It("shrinks the StorageClass when a matching LVMVolumeGroup loses the label", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(vgFast, nil)
		lvgA.Labels = map[string]string{poolKey: poolVal}
		lvgB := generateLVMVolumeGroup(vgSlow, nil)
		lvgB.Labels = map[string]string{poolKey: poolVal}
		Expect(cl.Create(ctx, lvgA)).To(Succeed())
		Expect(cl.Create(ctx, lvgB)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, rp, vbm, controller.LVMThickType, []slv.LocalStorageClassLVG{
			{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{poolKey: poolVal}}},
		})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		_, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(scParam(ctx)).To(Equal("- name: " + vgFast + "\n- name: " + vgSlow + "\n"))

		// Remove the label from one LVG: it must drop out of the StorageClass.
		Expect(cl.Get(ctx, client.ObjectKey{Name: vgSlow}, lvgB)).To(Succeed())
		lvgB.Labels = map[string]string{}
		Expect(cl.Update(ctx, lvgB)).To(Succeed())

		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())
		Expect(scParam(ctx)).To(Equal("- name: " + vgFast + "\n"))
	})

	It("resolves a matchExpressions In selector (inclusion)", func(ctx SpecContext) {
		fast := generateLVMVolumeGroup(vgFast, nil)
		fast.Labels = map[string]string{tierKey: "fast"}
		slow := generateLVMVolumeGroup(vgSlow, nil)
		slow.Labels = map[string]string{tierKey: "slow"}
		Expect(cl.Create(ctx, fast)).To(Succeed())
		Expect(cl.Create(ctx, slow)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, rp, vbm, controller.LVMThickType, []slv.LocalStorageClassLVG{
			{LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: tierKey, Operator: metav1.LabelSelectorOpIn, Values: []string{"fast"}},
			}}},
		})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		_, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(scParam(ctx)).To(Equal("- name: " + vgFast + "\n"))
	})

	It("resolves a matchExpressions NotIn selector (exclusion)", func(ctx SpecContext) {
		fast := generateLVMVolumeGroup(vgFast, nil)
		fast.Labels = map[string]string{tierKey: "fast"}
		slow := generateLVMVolumeGroup(vgSlow, nil)
		slow.Labels = map[string]string{tierKey: "slow"}
		Expect(cl.Create(ctx, fast)).To(Succeed())
		Expect(cl.Create(ctx, slow)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, rp, vbm, controller.LVMThickType, []slv.LocalStorageClassLVG{
			{LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: tierKey, Operator: metav1.LabelSelectorOpNotIn, Values: []string{"slow"}},
			}}},
		})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		_, err := reconcile(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(scParam(ctx)).To(Equal("- name: " + vgFast + "\n"))
	})

	It("fails when a selector matches LVMVolumeGroups on the same node", func(ctx SpecContext) {
		lvgA := generateLVMVolumeGroup(vgFast, nil)
		lvgA.Labels = map[string]string{poolKey: poolVal}
		lvgA.Status.Nodes = []snc.LVMVolumeGroupNode{{Name: nodeName}}
		lvgB := generateLVMVolumeGroup(vgSlow, nil)
		lvgB.Labels = map[string]string{poolKey: poolVal}
		lvgB.Status.Nodes = []snc.LVMVolumeGroupNode{{Name: nodeName}}
		Expect(cl.Create(ctx, lvgA)).To(Succeed())
		Expect(cl.Create(ctx, lvgB)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, rp, vbm, controller.LVMThickType, []slv.LocalStorageClassLVG{
			{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{poolKey: poolVal}}},
		})
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		shouldRequeue, err := reconcile(ctx, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		Expect(lsc.Status).NotTo(BeNil())
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))
	})

	It("fails a Thin selector when a matched LVMVolumeGroup lacks the requested thin pool", func(ctx SpecContext) {
		// The LVG only has thin pool "other-pool" in its status.
		lvg := generateLVMVolumeGroup(vgThin, []string{"other-pool"})
		lvg.Labels = map[string]string{poolKey: poolVal}
		Expect(cl.Create(ctx, lvg)).To(Succeed())

		lsc := generateLocalStorageClass(lscName, rp, vbm, controller.LVMThinType, []slv.LocalStorageClassLVG{
			{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{poolKey: poolVal}},
				Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "wanted-pool"},
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
})
