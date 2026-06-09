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
	"sigs.k8s.io/controller-runtime/pkg/client"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/internal"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
)

// defaultIgnoredPrefixes mirrors the union of the system list (internal values)
// and the default user list (openapi config-values) shipped with the module.
// Keeping it in sync here is intentional so that the controller behaviour is
// covered for the exact prefixes that real clusters will see.
var defaultIgnoredPrefixes = []string{
	"app.kubernetes.io/managed-by",
	"app.kubernetes.io/instance",
	"kubernetes.io/",
	"k8s.io/",
	"storage.deckhouse.io/managed-by",
	"argocd.argoproj.io/",
	"kustomize.toolkit.fluxcd.io/",
	"helm.toolkit.fluxcd.io/",
	"fleet.cattle.io/",
}

var _ = Describe("local-storage-class-controller label filtering", Ordered, func() {
	const (
		lscName  = "sds-local-volume-storage-class-label-filter"
		lvg1Name = "test-thick-vg-lf1"
		lvg2Name = "test-thick-vg-lf2"
	)

	var (
		cl  client.Client
		log = logger.NewLoggerFromLogr(GinkgoLogr)

		reclaimPolicy     = string(corev1.PersistentVolumeReclaimDelete)
		volumeBindingMode = string(v1.VolumeBindingWaitForFirstConsumer)

		lvgSpec = []slv.LocalStorageClassLVG{
			{Name: lvg1Name},
			{Name: lvg2Name},
		}
	)

	BeforeAll(func() {
		cl = NewFakeClient()
	})

	It("creates StorageClass with ignored labels dropped and managed-by enforced", func(ctx SpecContext) {
		Expect(cl.Create(ctx, generateLVMVolumeGroup(lvg1Name, []string{}))).To(Succeed())
		Expect(cl.Create(ctx, generateLVMVolumeGroup(lvg2Name, []string{}))).To(Succeed())

		lsc := generateLocalStorageClass(lscName, reclaimPolicy, volumeBindingMode, controller.LVMThickType, lvgSpec)
		lsc.Labels = map[string]string{
			"team":                   "storage",
			"env":                    "prod",
			"my.example.com/keep-me": "yes",

			"app.kubernetes.io/managed-by":    "helm",
			"app.kubernetes.io/instance":      "sds-local-volume",
			"kubernetes.io/legacy":            "true",
			"k8s.io/legacy":                   "true",
			"storage.deckhouse.io/managed-by": "someone-else",

			"argocd.argoproj.io/instance":      "infra",
			"kustomize.toolkit.fluxcd.io/name": "infra",
			"helm.toolkit.fluxcd.io/name":      "infra",
			"fleet.cattle.io/cluster":          "edge",
		}
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc, defaultIgnoredPrefixes)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())

		// Managed-by label must always be present and owned by the controller,
		// even when the LSC tried to override it via storage.deckhouse.io/managed-by.
		Expect(sc.Labels).To(HaveKeyWithValue(internal.SLVStorageManagedLabelKey, internal.SLVStorageClassCtrlName))

		Expect(sc.Labels).To(HaveKeyWithValue("team", "storage"))
		Expect(sc.Labels).To(HaveKeyWithValue("env", "prod"))
		Expect(sc.Labels).To(HaveKeyWithValue("my.example.com/keep-me", "yes"))

		Expect(sc.Labels).NotTo(HaveKey("app.kubernetes.io/managed-by"))
		Expect(sc.Labels).NotTo(HaveKey("app.kubernetes.io/instance"))
		Expect(sc.Labels).NotTo(HaveKey("kubernetes.io/legacy"))
		Expect(sc.Labels).NotTo(HaveKey("k8s.io/legacy"))

		Expect(sc.Labels).NotTo(HaveKey("argocd.argoproj.io/instance"))
		Expect(sc.Labels).NotTo(HaveKey("kustomize.toolkit.fluxcd.io/name"))
		Expect(sc.Labels).NotTo(HaveKey("helm.toolkit.fluxcd.io/name"))
		Expect(sc.Labels).NotTo(HaveKey("fleet.cattle.io/cluster"))

		// 3 propagated + 1 managed-by = 4 total.
		Expect(sc.Labels).To(HaveLen(4))
	})

	It("does not recreate StorageClass when LSC labels differ only in ignored prefixes", func(ctx SpecContext) {
		lsc := &slv.LocalStorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())

		// Mutate only ignored labels. After filtering, both sides match, so the
		// controller MUST consider the resource in sync and MUST NOT recreate
		// the StorageClass.
		lsc.Labels["argocd.argoproj.io/instance"] = "changed"
		lsc.Labels["helm.toolkit.fluxcd.io/name"] = "changed"
		lsc.Labels["kubernetes.io/some-thing"] = "changed"
		Expect(cl.Update(ctx, lsc)).To(Succeed())

		scBefore := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, scBefore)).To(Succeed())
		rvBefore := scBefore.ResourceVersion

		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc, defaultIgnoredPrefixes)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		scAfter := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, scAfter)).To(Succeed())
		Expect(scAfter.ResourceVersion).To(Equal(rvBefore))
	})

	It("recreates StorageClass when a non-ignored label changes", func(ctx SpecContext) {
		lsc := &slv.LocalStorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())

		lsc.Labels["team"] = "platform"
		Expect(cl.Update(ctx, lsc)).To(Succeed())

		scBefore := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, scBefore)).To(Succeed())
		rvBefore := scBefore.ResourceVersion

		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc, defaultIgnoredPrefixes)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		scAfter := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, scAfter)).To(Succeed())
		Expect(scAfter.ResourceVersion).NotTo(Equal(rvBefore))
		Expect(scAfter.Labels).To(HaveKeyWithValue("team", "platform"))
		Expect(scAfter.Labels).To(HaveKeyWithValue(internal.SLVStorageManagedLabelKey, internal.SLVStorageClassCtrlName))
		Expect(scAfter.Labels).NotTo(HaveKey("argocd.argoproj.io/instance"))
	})

	It("creates StorageClass with only the managed-by label when all LSC labels are ignored", func(ctx SpecContext) {
		const onlyIgnoredName = "sds-local-volume-only-ignored"
		const ignoredVG = "test-thick-vg-only-ignored"

		Expect(cl.Create(ctx, generateLVMVolumeGroup(ignoredVG, []string{}))).To(Succeed())

		lsc := generateLocalStorageClass(onlyIgnoredName, reclaimPolicy, volumeBindingMode, controller.LVMThickType, []slv.LocalStorageClassLVG{{Name: ignoredVG}})
		lsc.Labels = map[string]string{
			"argocd.argoproj.io/instance": "infra",
			"helm.toolkit.fluxcd.io/name": "infra",
			"k8s.io/foo":                  "bar",
		}
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc, defaultIgnoredPrefixes)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: onlyIgnoredName}, sc)).To(Succeed())
		Expect(sc.Labels).To(HaveLen(1))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.SLVStorageManagedLabelKey, internal.SLVStorageClassCtrlName))

		Expect(cl.Delete(ctx, lsc)).To(Succeed())
		Expect(cl.List(ctx, scList)).To(Succeed())
		shouldRequeue, err = controller.RunEventReconcile(ctx, cl, log, scList, lsc, defaultIgnoredPrefixes)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())
	})

	It("ignores empty-string entries inside the ignored prefixes list", func(ctx SpecContext) {
		const safeName = "sds-local-volume-empty-prefix-safe"
		const safeVG = "test-thick-vg-empty-prefix-safe"

		Expect(cl.Create(ctx, generateLVMVolumeGroup(safeVG, []string{}))).To(Succeed())

		lsc := generateLocalStorageClass(safeName, reclaimPolicy, volumeBindingMode, controller.LVMThickType, []slv.LocalStorageClassLVG{{Name: safeVG}})
		lsc.Labels = map[string]string{
			"team": "storage",
			"env":  "prod",
		}
		Expect(cl.Create(ctx, lsc)).To(Succeed())

		// Empty entries MUST be skipped — otherwise they would match every label
		// key (HasPrefix(x, "") is always true) and drop user labels silently.
		prefixesWithEmpty := []string{"", "argocd.argoproj.io/"}

		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc, prefixesWithEmpty)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: safeName}, sc)).To(Succeed())
		Expect(sc.Labels).To(HaveKeyWithValue("team", "storage"))
		Expect(sc.Labels).To(HaveKeyWithValue("env", "prod"))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.SLVStorageManagedLabelKey, internal.SLVStorageClassCtrlName))

		Expect(cl.Delete(ctx, lsc)).To(Succeed())
		Expect(cl.List(ctx, scList)).To(Succeed())
		_, _ = controller.RunEventReconcile(ctx, cl, log, scList, lsc, prefixesWithEmpty)
	})
})
