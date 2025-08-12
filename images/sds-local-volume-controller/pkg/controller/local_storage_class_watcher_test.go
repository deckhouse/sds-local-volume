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
	"context"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-controller/pkg/controller"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-controller/pkg/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-controller/pkg/logger"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

var _ = Describe(controller.LocalStorageClassCtrlName, func() {
	const (
		nameForLocalStorageClass = "sds-local-volume-storage-class"

		existingThickLVG1Name = "test-thick-vg1"
		existingThickLVG2Name = "test-thick-vg2"
		newThickLVGName       = "test-thick-vg3-new"

		existingThinLVG1Name = "test-thin-vg1"
		existingThinLVG2Name = "test-thin-vg2"
		newThinLVGName       = "test-thin-vg3-new"

		nonExistentLVG1Name = "test-vg4-non-existent"
		nonExistentLVG2Name = "test-vg5-non-existent"
	)

	var (
		ctx = context.Background()
		cl  = NewFakeClient()
		log = logger.Logger{}

		reclaimPolicyDelete = string(corev1.PersistentVolumeReclaimDelete)
		reclaimPolicyRetain = string(corev1.PersistentVolumeReclaimRetain)

		volumeBindingModeWFFC = string(v1.VolumeBindingWaitForFirstConsumer)
		volumeBindingModeIM   = string(v1.VolumeBindingImmediate)

		existingThickLVG1Template = generateLVMVolumeGroup(existingThickLVG1Name, []string{})
		existingThickLVG2Template = generateLVMVolumeGroup(existingThickLVG2Name, []string{})
		newThickLVGTemplate       = generateLVMVolumeGroup(newThickLVGName, []string{})

		existingThinLVG1Template = generateLVMVolumeGroup(existingThinLVG1Name, []string{"thin-pool-1", "thin-pool-2"})
		existingThinLVG2Template = generateLVMVolumeGroup(existingThinLVG2Name, []string{"thin-pool-1", "thin-pool-2"})
		newThinLVGTemplate       = generateLVMVolumeGroup(newThinLVGName, []string{"thin-pool-1", "thin-pool-2"})
	)

	It("Create_local_sc_with_existing_lvgs", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		err := cl.Create(ctx, existingThickLVG1Template)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Create(ctx, existingThickLVG2Template)
		Expect(err).NotTo(HaveOccurred())

		lscTemplate := generateLocalStorageClass(nameForLocalStorageClass, reclaimPolicyDelete, volumeBindingModeWFFC, controller.LVMThickType, lvgSpec)

		err = cl.Create(ctx, lscTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		Expect(lsc).NotTo(BeNil())
		Expect(lsc.Name).To(Equal(nameForLocalStorageClass))
		Expect(lsc.Finalizers).To(HaveLen(0))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, controller.DefaultFSType)
	})

	It("Update_local_sc_add_existing_lvg", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: newThickLVGName},
		}

		err := cl.Create(ctx, newThickLVGTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: newThickLVGName})

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, controller.DefaultFSType)
	})

	It("Check_anotated_sc_after_lsc_update", func() {
		sc := &v1.StorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(sc.Labels).To(HaveLen(1))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.SLVStorageManagedLabelKey, internal.SLVStorageClassCtrlName))
		Expect(sc.Annotations).To(HaveLen(1))
		Expect(sc.Annotations).To(HaveKeyWithValue(internal.SLVStorageClassVolumeSnapshotClassAnnotationKey, internal.SLVStorageClassVolumeSnapshotClassAnnotationValue))
	})

	It("Update_local_sc_remove_existing_lvg", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = delFromSlice(lsc.Spec.LVM.LVMVolumeGroups, newThickLVGName)

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, "")
	})

	It("Update_local_sc_add_non_existing_lvg", func() {
		lvgSpecOld := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: nonExistentLVG1Name},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: nonExistentLVG1Name})

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpecOld, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, controller.DefaultFSType)
	})

	It("Remove_local_sc_with_non_existing_lvg", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: nonExistentLVG1Name},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(1))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("Create_local_sc_with_non_existing_lvgs", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: nonExistentLVG1Name},
			{Name: nonExistentLVG2Name},
		}

		lscTemplate := generateLocalStorageClass(nameForLocalStorageClass, reclaimPolicyDelete, volumeBindingModeWFFC, controller.LVMThickType, lvgSpec)

		err := cl.Create(ctx, lscTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(0))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

	})

	It("Update_local_sc_with_all_existing_lvgs", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = lvgSpec

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(0))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, controller.DefaultFSType)
	})

	It("Remove_local_sc_with_existing_lvgs", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(1))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("Create_local_sc_when_sc_with_another_provisioner_exists", func() {
		sc := &v1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: nameForLocalStorageClass,
			},
			Provisioner: "test-provisioner",
		}

		err := cl.Create(ctx, sc)
		Expect(err).NotTo(HaveOccurred())

		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lscTemplate := generateLocalStorageClass(nameForLocalStorageClass, reclaimPolicyDelete, volumeBindingModeWFFC, controller.LVMThickType, lvgSpec)

		err = cl.Create(ctx, lscTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(1))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))

		sc = &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(sc.Provisioner).To(Equal("test-provisioner"))
		Expect(sc.Finalizers).To(HaveLen(0))
	})

	It("Update_local_sc_add_existing_vg_when_sc_with_another_provisioner_exists", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: newThickLVGName},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: newThickLVGName})

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(1))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(sc.Provisioner).To(Equal("test-provisioner"))
		Expect(sc.Finalizers).To(HaveLen(0))
	})

	It("Remove_local_sc_with_existing_vgs_when_sc_with_another_provisioner_exists", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: newThickLVGName},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(1))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(sc.Provisioner).To(Equal("test-provisioner"))
		Expect(sc.Finalizers).To(HaveLen(0))

		err = cl.Delete(ctx, sc)
		Expect(err).NotTo(HaveOccurred())

		sc = &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("Create_local_thin_sc_with_existing_thin_lvgs", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-2"}},
		}

		err := cl.Create(ctx, existingThinLVG1Template)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Create(ctx, existingThinLVG2Template)
		Expect(err).NotTo(HaveOccurred())

		lscTemplate := generateLocalStorageClass(nameForLocalStorageClass, reclaimPolicyRetain, volumeBindingModeIM, controller.LVMThinType, lvgSpec)

		err = cl.Create(ctx, lscTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		Expect(lsc).NotTo(BeNil())
		Expect(lsc.Name).To(Equal(nameForLocalStorageClass))
		Expect(lsc.Finalizers).To(HaveLen(0))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM, controller.DefaultFSType)
	})

	It("Update_local_thin_sc_add_existing_thin_lvg", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-2"}},
			{Name: newThinLVGName, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
		}

		err := cl.Create(ctx, newThinLVGTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: newThinLVGName, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}})

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM, controller.DefaultFSType)
	})

	It("Update_local_thin_sc_remove_existing_thin_lvg", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-2"}},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = delFromSlice(lsc.Spec.LVM.LVMVolumeGroups, newThinLVGName)

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM, controller.DefaultFSType)
	})

	It("Update_local_thin_sc_add_existing_thick_lvg", func() {
		lvgSpecOld := []slv.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-2"}},
		}

		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-2"}},
			{Name: existingThickLVG1Name},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: existingThickLVG1Name})

		err = cl.Update(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))
		Expect(lsc.Status.Phase).To(Equal(controller.FailedStatusPhase))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandartChecksForSC(sc, lvgSpecOld, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM, controller.DefaultFSType)
	})

	It("Remove_local_thin_sc_with_existing_thick_lvg", func() {
		lvgSpec := []slv.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "thin-pool-2"}},
			{Name: existingThickLVG1Name},
		}

		lsc := &slv.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &slv.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Finalizers).To(HaveLen(1))
		Expect(lsc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())
		Expect(scList.Items).To(HaveLen(1))

		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("creates thick sc with contiguous false", func() {
		contigLVG1 := "contig-vg1"
		contigLVG2 := "contig-vg2"
		lscName := nameForLocalStorageClass + "-contig-false"
		lvgSpec := []slv.LocalStorageClassLVG{{Name: contigLVG1}, {Name: contigLVG2}}

		Expect(cl.Create(ctx, generateLVMVolumeGroup(contigLVG1, []string{}))).To(Succeed())
		Expect(cl.Create(ctx, generateLVMVolumeGroup(contigLVG2, []string{}))).To(Succeed())

		lscTemplate := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC, controller.LVMThickType, lvgSpec)
		contiguous := false
		lscTemplate.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: &contiguous}
		Expect(cl.Create(ctx, lscTemplate)).To(Succeed())

		lsc := &slv.LocalStorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())
		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		performStandartChecksForSC(sc, lvgSpec, lscName, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC, controller.DefaultFSType)
		Expect(sc.Parameters).NotTo(HaveKey(controller.LVMThickContiguousParamKey))

		// Cleanup: delete and reconcile
		Expect(cl.Delete(ctx, lsc)).To(Succeed())
		Expect(cl.List(ctx, scList)).To(Succeed())
		shouldRequeue, err = controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())
	})

	It("Create_thick_sc_with_contiguous_true", func() {
		contigLVG1 := "contig2-vg1"
		contigLVG2 := "contig2-vg2"
		lscName := nameForLocalStorageClass + "-contig-true"
		lvgSpec := []slv.LocalStorageClassLVG{{Name: contigLVG1}, {Name: contigLVG2}}

		Expect(cl.Create(ctx, generateLVMVolumeGroup(contigLVG1, []string{}))).To(Succeed())
		Expect(cl.Create(ctx, generateLVMVolumeGroup(contigLVG2, []string{}))).To(Succeed())

		lscTemplate := generateLocalStorageClass(lscName, reclaimPolicyDelete, volumeBindingModeWFFC, controller.LVMThickType, lvgSpec)
		contiguous := true
		lscTemplate.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: &contiguous}
		Expect(cl.Create(ctx, lscTemplate)).To(Succeed())

		lsc := &slv.LocalStorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, lsc)).To(Succeed())
		scList := &v1.StorageClassList{}
		Expect(cl.List(ctx, scList)).To(Succeed())
		shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		sc := &v1.StorageClass{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: lscName}, sc)).To(Succeed())
		Expect(sc.Parameters).To(HaveKeyWithValue(controller.TypeParamKey, controller.LocalStorageClassLvmType))
		Expect(sc.Parameters).To(HaveKeyWithValue(controller.LVMTypeParamKey, controller.LVMThickType))
		Expect(sc.Parameters).To(HaveKeyWithValue(controller.LVMVolumeBindingModeParamKey, volumeBindingModeWFFC))
		Expect(sc.Parameters).To(HaveKey(controller.LVMVolumeGroupsParamKey))
		Expect(sc.Parameters).To(HaveKeyWithValue(controller.FSTypeParamKey, controller.DefaultFSType))
		Expect(sc.Parameters).To(HaveKeyWithValue(controller.LVMThickContiguousParamKey, "true"))

		// Cleanup: delete and reconcile
		Expect(cl.Delete(ctx, lsc)).To(Succeed())
		Expect(cl.List(ctx, scList)).To(Succeed())
		shouldRequeue, err = controller.RunEventReconcile(ctx, cl, log, scList, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())
	})

})

func generateLVMVolumeGroup(name string, thinPoolNames []string) *snc.LVMVolumeGroup {
	lvmType := controller.LVMThickType

	if len(thinPoolNames) > 0 {
		lvmType = controller.LVMThinType
	}

	thinPoolsSpec := make([]snc.LVMVolumeGroupThinPoolSpec, 0)
	thinPoolsStatus := make([]snc.LVMVolumeGroupThinPoolStatus, 0)
	for i := 0; i < len(thinPoolNames); i++ {
		thinPoolsSpec = append(thinPoolsSpec, snc.LVMVolumeGroupThinPoolSpec{
			Name: thinPoolNames[i],
			Size: "10Gi",
		})
		thinPoolsStatus = append(thinPoolsStatus, snc.LVMVolumeGroupThinPoolStatus{
			Name:       thinPoolNames[i],
			ActualSize: resource.MustParse("10Gi"),
			UsedSize:   resource.MustParse("0Gi"),
		})
	}

	return &snc.LVMVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: snc.LVMVolumeGroupSpec{
			ActualVGNameOnTheNode: "vg1",
			ThinPools:             thinPoolsSpec,
			Type:                  lvmType,
		},
		Status: snc.LVMVolumeGroupStatus{
			ThinPools: thinPoolsStatus,
		},
	}
}

//nolint:unparam
func generateLocalStorageClass(lscName, reclaimPolicy, volumeBindingMode, lvmType string, lvgs []slv.LocalStorageClassLVG) *slv.LocalStorageClass {
	return &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: lscName,
		},
		Spec: slv.LocalStorageClassSpec{
			ReclaimPolicy:     reclaimPolicy,
			VolumeBindingMode: volumeBindingMode,
			LVM: &slv.LocalStorageClassLVMSpec{
				Type:            lvmType,
				LVMVolumeGroups: lvgs,
			},
		},
	}
}

//nolint:unparam
func performStandartChecksForSC(
	sc *v1.StorageClass,
	lvgSpec []slv.LocalStorageClassLVG,
	nameForLocalStorageClass,
	lscType,
	lvmType,
	reclaimPolicy,
	volumeBindingMode,
	fsType string,
) {
	expectString := ""
	for i, lvg := range lvgSpec {
		if i != 0 {
			expectString += "\n"
		}
		if lvg.Thin != nil {
			expectString += "- name: " + lvg.Name + "\n  thin:\n    poolName: " + lvg.Thin.PoolName
		} else {
			expectString += "- name: " + lvg.Name
		}
	}
	expectString += "\n"

	expectedFSType := fsType
	if fsType == "" {
		expectedFSType = controller.DefaultFSType
	}

	Expect(sc).NotTo(BeNil())
	Expect(sc.Name).To(Equal(nameForLocalStorageClass))
	Expect(sc.Finalizers).To(HaveLen(1))
	Expect(sc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))

	Expect(sc.Parameters).To(HaveLen(5))
	Expect(sc.Parameters).To(HaveKeyWithValue(controller.TypeParamKey, lscType))
	Expect(sc.Parameters).To(HaveKeyWithValue(controller.LVMTypeParamKey, lvmType))
	Expect(sc.Parameters).To(HaveKeyWithValue(controller.LVMVolumeBindingModeParamKey, volumeBindingMode))
	Expect(sc.Parameters).To(HaveKeyWithValue(controller.FSTypeParamKey, expectedFSType))
	Expect(sc.Parameters).To(HaveKey(controller.LVMVolumeGroupsParamKey))
	Expect(sc.Parameters[controller.LVMVolumeGroupsParamKey]).To(Equal(expectString))

	Expect(sc.Provisioner).To(Equal(controller.LocalStorageClassProvisioner))
	Expect(string(*sc.ReclaimPolicy)).To(Equal(reclaimPolicy))
	Expect(string(*sc.VolumeBindingMode)).To(Equal(volumeBindingMode))
	Expect(*sc.AllowVolumeExpansion).To(BeTrue())
}

func delFromSlice(slice []slv.LocalStorageClassLVG, name string) []slv.LocalStorageClassLVG {
	for i, lvg := range slice {
		if lvg.Name == name {
			// return append(slice[:i], slice[i+1:]...)
			return slices.Delete(slice, i, i+1)
		}
	}
	return slice
}
