/*
Copyright 2023 Flant JSC

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
	v1alpha1 "sds-local-volume-controller/api/v1alpha1"
	"sds-local-volume-controller/pkg/controller"
	"sds-local-volume-controller/pkg/logger"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(controller.LocalStorageClassCtrlName, func() {
	const (
		controllerNamespace      = "test-namespace"
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

		existingThickLVG1Template = generateLVMVolumeGroup(existingThickLVG1Name, []string{"dev-1111", "dev-2222"}, []string{})
		existingThickLVG2Template = generateLVMVolumeGroup(existingThickLVG2Name, []string{"dev-3333", "dev-4444"}, []string{})
		newThickLVGTemplate       = generateLVMVolumeGroup(newThickLVGName, []string{"dev-5555", "dev-6666"}, []string{})

		existingThinLVG1Template = generateLVMVolumeGroup(existingThinLVG1Name, []string{"dev-7777", "dev-8888"}, []string{"thin-pool-1", "thin-pool-2"})
		existingThinLVG2Template = generateLVMVolumeGroup(existingThinLVG2Name, []string{"dev-9999", "dev-1010"}, []string{"thin-pool-1", "thin-pool-2"})
		newThinLVGTemplate       = generateLVMVolumeGroup(newThinLVGName, []string{"dev-1111", "dev-1212"}, []string{"thin-pool-1", "thin-pool-2"})
	)

	It("Create_local_sc_with_existing_lvgs", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
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

		lsc := &v1alpha1.LocalStorageClass{}
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
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC)
	})

	It("Update_local_sc_add_existing_lvg", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: newThickLVGName},
		}

		err := cl.Create(ctx, newThickLVGTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &v1alpha1.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, v1alpha1.LocalStorageClassLVG{Name: newThickLVGName})

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
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC)
	})

	It("Update_local_sc_remove_existing_lvg", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lsc := &v1alpha1.LocalStorageClass{}
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
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC)
	})

	It("Update_local_sc_add_non_existing_lvg", func() {
		lvgSpecOld := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: nonExistentLVG1Name},
		}

		lsc := &v1alpha1.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, v1alpha1.LocalStorageClassLVG{Name: nonExistentLVG1Name})

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
		performStandartChecksForSC(sc, lvgSpecOld, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC)
	})

	It("Remove_local_sc_with_non_existing_lvg", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: nonExistentLVG1Name},
		}

		lsc := &v1alpha1.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(lsc.Spec.LVM.LVMVolumeGroups).To(Equal(lvgSpec))

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &v1alpha1.LocalStorageClass{}
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
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: nonExistentLVG1Name},
			{Name: nonExistentLVG2Name},
		}

		lscTemplate := generateLocalStorageClass(nameForLocalStorageClass, reclaimPolicyDelete, volumeBindingModeWFFC, controller.LVMThickType, lvgSpec)

		err := cl.Create(ctx, lscTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &v1alpha1.LocalStorageClass{}
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
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lsc := &v1alpha1.LocalStorageClass{}
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
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThickType, reclaimPolicyDelete, volumeBindingModeWFFC)
	})

	It("Remove_local_sc_with_existing_lvgs", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lsc := &v1alpha1.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &v1alpha1.LocalStorageClass{}
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

		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
		}

		lscTemplate := generateLocalStorageClass(nameForLocalStorageClass, reclaimPolicyDelete, volumeBindingModeWFFC, controller.LVMThickType, lvgSpec)

		err = cl.Create(ctx, lscTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &v1alpha1.LocalStorageClass{}
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
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: newThickLVGName},
		}

		lsc := &v1alpha1.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, v1alpha1.LocalStorageClassLVG{Name: newThickLVGName})

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
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThickLVG1Name},
			{Name: existingThickLVG2Name},
			{Name: newThickLVGName},
		}

		lsc := &v1alpha1.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &v1alpha1.LocalStorageClass{}
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
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-2"}},
		}

		err := cl.Create(ctx, existingThinLVG1Template)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Create(ctx, existingThinLVG2Template)
		Expect(err).NotTo(HaveOccurred())

		lscTemplate := generateLocalStorageClass(nameForLocalStorageClass, reclaimPolicyRetain, volumeBindingModeIM, controller.LVMThinType, lvgSpec)

		err = cl.Create(ctx, lscTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &v1alpha1.LocalStorageClass{}
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
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM)
	})

	It("Update_local_thin_sc_add_existing_thin_lvg", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-2"}},
			{Name: newThinLVGName, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}},
		}

		err := cl.Create(ctx, newThinLVGTemplate)
		Expect(err).NotTo(HaveOccurred())

		lsc := &v1alpha1.LocalStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, v1alpha1.LocalStorageClassLVG{Name: newThinLVGName, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}})

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
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM)
	})

	It("Update_local_thin_sc_remove_existing_thin_lvg", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-2"}},
		}

		lsc := &v1alpha1.LocalStorageClass{}
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
		performStandartChecksForSC(sc, lvgSpec, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM)
	})

	It("Update_local_thin_sc_add_existing_thick_lvg", func() {
		lvgSpecOld := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-2"}},
		}

		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-2"}},
			{Name: existingThickLVG1Name},
		}

		lsc := &v1alpha1.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc.Spec.LVM.LVMVolumeGroups = append(lsc.Spec.LVM.LVMVolumeGroups, v1alpha1.LocalStorageClassLVG{Name: existingThickLVG1Name})

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
		performStandartChecksForSC(sc, lvgSpecOld, nameForLocalStorageClass, controller.LocalStorageClassLvmType, controller.LVMThinType, reclaimPolicyRetain, volumeBindingModeIM)
	})

	It("Remove_local_thin_sc_with_existing_thick_lvg", func() {
		lvgSpec := []v1alpha1.LocalStorageClassLVG{
			{Name: existingThinLVG1Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-1"}},
			{Name: existingThinLVG2Name, Thin: &v1alpha1.LocalStorageClassThinPool{PoolName: "thin-pool-2"}},
			{Name: existingThickLVG1Name},
		}

		lsc := &v1alpha1.LocalStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForLocalStorageClass}, lsc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, lsc)
		Expect(err).NotTo(HaveOccurred())

		lsc = &v1alpha1.LocalStorageClass{}
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

})

func generateLVMVolumeGroup(name string, devices, thinPoolNames []string) *v1alpha1.LvmVolumeGroup {
	lvmType := controller.LVMThickType

	if len(thinPoolNames) > 0 {
		lvmType = controller.LVMThinType
	}

	thinPoolsSpec := make([]v1alpha1.SpecThinPool, 0)
	thinPoolsStatus := make([]v1alpha1.StatusThinPool, 0)
	for i := 0; i < len(thinPoolNames); i++ {
		thinPoolsSpec = append(thinPoolsSpec, v1alpha1.SpecThinPool{
			Name: thinPoolNames[i],
			Size: resource.MustParse("10Gi"),
		})
		thinPoolsStatus = append(thinPoolsStatus, v1alpha1.StatusThinPool{
			Name:       thinPoolNames[i],
			ActualSize: resource.MustParse("10Gi"),
			UsedSize:   resource.MustParse("0Gi"),
		})
	}

	return &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.LvmVolumeGroupSpec{
			ActualVGNameOnTheNode: "vg1",
			BlockDeviceNames:      devices,
			ThinPools:             thinPoolsSpec,
			Type:                  lvmType,
		},
		Status: v1alpha1.LvmVolumeGroupStatus{
			ThinPools: thinPoolsStatus,
		},
	}
}

func generateLocalStorageClass(lscName, reclaimPolicy, volumeBindingMode, lvmType string, lvgs []v1alpha1.LocalStorageClassLVG) *v1alpha1.LocalStorageClass {

	return &v1alpha1.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: lscName,
		},
		Spec: v1alpha1.LocalStorageClassSpec{
			ReclaimPolicy:     reclaimPolicy,
			VolumeBindingMode: volumeBindingMode,
			LVM: &v1alpha1.LocalStorageClassLVM{
				Type:            lvmType,
				LVMVolumeGroups: lvgs,
			},
		},
	}

}

func performStandartChecksForSC(sc *v1.StorageClass, lvgSpec []v1alpha1.LocalStorageClassLVG, nameForLocalStorageClass, LSCType, LVMType, reclaimPolicy, volumeBindingMode string) {
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

	Expect(sc).NotTo(BeNil())
	Expect(sc.Name).To(Equal(nameForLocalStorageClass))
	Expect(sc.Finalizers).To(HaveLen(1))
	Expect(sc.Finalizers).To(ContainElement(controller.LocalStorageClassFinalizerName))

	Expect(sc.Parameters).To(HaveLen(4))
	Expect(sc.Parameters).To(HaveKeyWithValue(controller.TypeParamKey, LSCType))
	Expect(sc.Parameters).To(HaveKeyWithValue(controller.LVMTypeParamKey, LVMType))
	Expect(sc.Parameters).To(HaveKeyWithValue(controller.LVMVolumeBindingModeParamKey, volumeBindingMode))
	Expect(sc.Parameters).To(HaveKey(controller.LVMVolumeGroupsParamKey))
	Expect(sc.Parameters[controller.LVMVolumeGroupsParamKey]).To(Equal(expectString))

	Expect(sc.Provisioner).To(Equal(controller.LocalStorageClassProvisioner))
	Expect(string(*sc.ReclaimPolicy)).To(Equal(reclaimPolicy))
	Expect(string(*sc.VolumeBindingMode)).To(Equal(volumeBindingMode))
	Expect(*sc.AllowVolumeExpansion).To(BeTrue())

}

func delFromSlice(slice []v1alpha1.LocalStorageClassLVG, name string) []v1alpha1.LocalStorageClassLVG {
	for i, lvg := range slice {
		if lvg.Name == name {
			// return append(slice[:i], slice[i+1:]...)
			return slices.Delete(slice, i, i+1)
		}
	}
	return slice
}
