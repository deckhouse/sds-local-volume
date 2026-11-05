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
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/sds-common-lib/logger"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
)

// snapshotClassMarkerUID tags a StorageClass, so "was it recreated" is a
// question about its identity: the fake client numbers resourceVersions per
// object, and a recreated StorageClass can repeat the one it replaced.
const snapshotClassMarkerUID = types.UID("before-the-reconcile")

// The LocalStorageClass of the snapshot class specs and its LVMVolumeGroups.
const (
	snapshotClassLSCName    = "sds-local-volume-snapshot-class"
	snapshotClassLVGName    = "test-thick-vg-snapshot-class"
	snapshotClassNewLVGName = "test-thick-vg-snapshot-class-new"
)

// reconcileSnapshotClassLSC runs one reconcile pass of the LocalStorageClass
// against the current StorageClasses.
func reconcileSnapshotClassLSC(ctx SpecContext, cl client.Client, log logger.Logger) {
	GinkgoHelper()

	lsc := &slv.LocalStorageClass{}
	Expect(cl.Get(ctx, client.ObjectKey{Name: snapshotClassLSCName}, lsc)).To(Succeed())

	scList := &v1.StorageClassList{}
	Expect(cl.List(ctx, scList)).To(Succeed())

	shouldRequeue, err := controller.RunEventReconcile(ctx, cl, log, scList, lsc, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(shouldRequeue).To(BeFalse())

	Expect(cl.Get(ctx, client.ObjectKey{Name: snapshotClassLSCName}, lsc)).To(Succeed())
	Expect(lsc.Status).NotTo(BeNil())
	Expect(lsc.Status.Phase).To(Equal(controller.CreatedStatusPhase))
}

// createSnapshotClassLSC creates a Thick LocalStorageClass on a new
// LVMVolumeGroup and reconciles it into its StorageClass.
func createSnapshotClassLSC(ctx SpecContext, cl client.Client, log logger.Logger) {
	GinkgoHelper()

	Expect(cl.Create(ctx, generateLVMVolumeGroup(snapshotClassLVGName, []string{}))).To(Succeed())
	lsc := generateLocalStorageClass(snapshotClassLSCName, string(corev1.PersistentVolumeReclaimDelete), string(v1.VolumeBindingWaitForFirstConsumer), controller.LVMThickType, []slv.LocalStorageClassLVG{{Name: snapshotClassLVGName}})
	Expect(cl.Create(ctx, lsc)).To(Succeed())

	reconcileSnapshotClassLSC(ctx, cl, log)
}

// setSnapshotClassAnnotations replaces the annotations of the StorageClass and
// tags it with snapshotClassMarkerUID.
func setSnapshotClassAnnotations(ctx SpecContext, cl client.Client, annotations map[string]string) {
	GinkgoHelper()

	sc := &v1.StorageClass{}
	Expect(cl.Get(ctx, client.ObjectKey{Name: snapshotClassLSCName}, sc)).To(Succeed())
	sc.UID = snapshotClassMarkerUID
	sc.Annotations = annotations
	Expect(cl.Update(ctx, sc)).To(Succeed())
}

// getSnapshotClassSC reads the StorageClass of the LocalStorageClass.
func getSnapshotClassSC(ctx SpecContext, cl client.Client) *v1.StorageClass {
	GinkgoHelper()

	sc := &v1.StorageClass{}
	Expect(cl.Get(ctx, client.ObjectKey{Name: snapshotClassLSCName}, sc)).To(Succeed())

	return sc
}
