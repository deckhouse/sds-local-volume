//go:build ce

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

package controller_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/internal"
)

// expectModuleSnapshotClassAnnotation checks the snapshot class annotation of
// a StorageClass the controller has just created: CE has no snapshots, so the
// StorageClass names no snapshot class.
func expectModuleSnapshotClassAnnotation(sc *v1.StorageClass) {
	GinkgoHelper()
	Expect(sc.Annotations).NotTo(HaveKey(internal.SLVStorageClassVolumeSnapshotClassAnnotationKey))
}

var _ = Describe("local-storage-class-controller snapshot class annotation (CE)", Ordered, func() {
	var (
		cl  client.Client
		log = NewTestLogger()
	)

	BeforeAll(func() {
		cl = NewFakeClient()
	})

	It("creates the StorageClass without the annotation", func(ctx SpecContext) {
		createSnapshotClassLSC(ctx, cl, log)

		Expect(getSnapshotClassSC(ctx, cl).Annotations).To(BeEmpty())
	})

	It("removes the annotation from an existing StorageClass in place", func(ctx SpecContext) {
		setSnapshotClassAnnotations(ctx, cl, map[string]string{
			internal.SLVStorageClassVolumeSnapshotClassAnnotationKey: internal.SLVStorageClassVolumeSnapshotClassAnnotationValue,
			"example.com/keep": "yes",
		})

		reconcileSnapshotClassLSC(ctx, cl, log)

		sc := getSnapshotClassSC(ctx, cl)
		Expect(sc.UID).To(Equal(snapshotClassMarkerUID))
		Expect(sc.Annotations).To(Equal(map[string]string{"example.com/keep": "yes"}))
	})

	It("removes an annotation naming any other class", func(ctx SpecContext) {
		setSnapshotClassAnnotations(ctx, cl, map[string]string{
			internal.SLVStorageClassVolumeSnapshotClassAnnotationKey: "other-snapshot-class",
		})

		reconcileSnapshotClassLSC(ctx, cl, log)

		sc := getSnapshotClassSC(ctx, cl)
		Expect(sc.UID).To(Equal(snapshotClassMarkerUID))
		Expect(sc.Annotations).To(BeEmpty())
	})

	It("does not write a StorageClass that has no annotation", func(ctx SpecContext) {
		rvBefore := getSnapshotClassSC(ctx, cl).ResourceVersion

		reconcileSnapshotClassLSC(ctx, cl, log)

		Expect(getSnapshotClassSC(ctx, cl).ResourceVersion).To(Equal(rvBefore))
	})

	It("does not carry the annotation over when the StorageClass is recreated", func(ctx SpecContext) {
		setSnapshotClassAnnotations(ctx, cl, map[string]string{
			internal.SLVStorageClassVolumeSnapshotClassAnnotationKey: internal.SLVStorageClassVolumeSnapshotClassAnnotationValue,
			"example.com/keep": "yes",
		})

		Expect(cl.Create(ctx, generateLVMVolumeGroup(snapshotClassNewLVGName, []string{}))).To(Succeed())
		lsc := &slv.LocalStorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: snapshotClassLSCName}, lsc)).To(Succeed())
		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: snapshotClassNewLVGName})
		Expect(cl.Update(ctx, lsc)).To(Succeed())

		reconcileSnapshotClassLSC(ctx, cl, log)

		sc := getSnapshotClassSC(ctx, cl)
		Expect(sc.UID).NotTo(Equal(snapshotClassMarkerUID))
		Expect(sc.Annotations).To(Equal(map[string]string{"example.com/keep": "yes"}))
	})
})
