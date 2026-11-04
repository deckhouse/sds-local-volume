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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
)

// Messages of the CEL rules of crds/storage.deckhouse.io_localstorageclasses.yaml.
// Five rules share "Value is immutable." and two share the contiguous message,
// so those are asserted together with the path the apiserver reports them on.
const (
	celEntryExactlyOne    = "Exactly one of name or labelSelector must be set."
	celEachEntryOne       = "Each spec.lvm.lvmVolumeGroups entry must set exactly one of name or labelSelector."
	celNoMix              = "spec.lvm.lvmVolumeGroups must use either name entries or labelSelector entries, not a mix."
	celThickForbiddenThin = "Field spec.lvm.thick is forbidden for Thin type."
	celThinPoolRequired   = "Field thin.poolName is required for every spec.lvm.lvmVolumeGroups entry when type is Thin."
	celThinForbiddenThick = "Field thin is forbidden for spec.lvm.lvmVolumeGroups entries when type is Thick."
	celThickRemoved       = "Field 'thick' cannot be removed if 'thick.contiguous' is specified."
	celContiguousRemoved  = "Field 'contiguous' cannot be removed."
	celContiguousAdded    = "Field 'contiguous' is immutable and cannot be added if not specified at creation."
	celImmutable          = "Value is immutable."
)

// celLSC is an admitted Thick LocalStorageClass with name entries, which each
// case changes in one place.
func celLSC(name string) *slv.LocalStorageClass {
	return &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: slv.LocalStorageClassSpec{
			ReclaimPolicy:     "Delete",
			VolumeBindingMode: "WaitForFirstConsumer",
			FSType:            "ext4",
			LVM: &slv.LocalStorageClassLVMSpec{
				Type:            controller.LVMThickType,
				LVMVolumeGroups: []slv.LocalStorageClassLVG{{Name: "cel-lvg-a"}},
			},
		},
	}
}

func celThin() *slv.LocalStorageClassLVMThinPoolSpec {
	return &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "cel-pool"}
}

func celBool(v bool) *bool {
	return &v
}

func celSelector() *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{poolLabelKey: "cel"}}
}

// expectAdmission checks that err is nil when no messages are given, and
// otherwise that the apiserver rejected the object as invalid with every one of
// messages in the error.
func expectAdmission(err error, messages ...string) {
	GinkgoHelper()
	if len(messages) == 0 {
		Expect(err).NotTo(HaveOccurred())
		return
	}
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "want an Invalid error with %q, got %v", messages, err)
	for _, message := range messages {
		Expect(err.Error()).To(ContainSubstring(message))
	}
}

// createCelLSC creates lsc and deletes it when the spec ends; the controller
// drops its finalizer.
func createCelLSC(ctx SpecContext, lsc *slv.LocalStorageClass) error {
	GinkgoHelper()
	err := k8sClient.Create(ctx, lsc)
	if err == nil {
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, lsc))).To(Succeed())
		})
	}
	return err
}

// patchCelLSC sends the change mutate makes to the created object as a merge
// patch. A patch carries no resourceVersion, so the writes of the running
// controller cannot turn the admission verdict into a conflict.
func patchCelLSC(ctx SpecContext, lsc *slv.LocalStorageClass, mutate func(*slv.LocalStorageClass)) error {
	GinkgoHelper()
	changed := lsc.DeepCopy()
	mutate(changed)
	return k8sClient.Patch(ctx, changed, client.MergeFrom(lsc))
}

var _ = Describe("LocalStorageClass CEL admission", func() {
	DescribeTable("on create",
		func(ctx SpecContext, name string, mutate func(*slv.LocalStorageClass), messages ...string) {
			lsc := celLSC(name)
			mutate(lsc)
			expectAdmission(createCelLSC(ctx, lsc), messages...)
		},
		Entry("an entry with neither name nor labelSelector", "cel-entry-empty",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{}}
			},
			"spec.lvm.lvmVolumeGroups[0]: Invalid value: \"object\": "+celEntryExactlyOne,
			"spec.lvm: Invalid value: \"object\": "+celEachEntryOne),
		Entry("an entry with both name and labelSelector", "cel-entry-both",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{Name: "cel-lvg-a", LabelSelector: celSelector()}}
			},
			"spec.lvm.lvmVolumeGroups[0]: Invalid value: \"object\": "+celEntryExactlyOne,
			"spec.lvm: Invalid value: \"object\": "+celEachEntryOne),
		Entry("labelSelector entries", "cel-entry-selector",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{LabelSelector: celSelector()}, {LabelSelector: celSelector()}}
			}),
		Entry("name and labelSelector entries mixed", "cel-entry-mix",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{Name: "cel-lvg-a"}, {LabelSelector: celSelector()}}
			},
			celNoMix),
		Entry("thick on a Thin class", "cel-thin-thick",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Type = controller.LVMThinType
				l.Spec.LVM.LVMVolumeGroups[0].Thin = celThin()
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{}
			},
			celThickForbiddenThin),
		Entry("thick on a Thick class", "cel-thick-thick",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: celBool(true)}
			}),
		Entry("a Thin entry without thin", "cel-thin-no-pool",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Type = controller.LVMThinType
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{Name: "cel-lvg-a", Thin: celThin()}, {Name: "cel-lvg-b"}}
			},
			celThinPoolRequired),
		Entry("Thin entries with thin.poolName", "cel-thin-pool",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Type = controller.LVMThinType
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{Name: "cel-lvg-a", Thin: celThin()}, {Name: "cel-lvg-b", Thin: celThin()}}
			}),
		Entry("a Thick entry with thin", "cel-thick-pool",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{Name: "cel-lvg-a"}, {Name: "cel-lvg-b", Thin: celThin()}}
			},
			celThinForbiddenThick),
	)

	DescribeTable("on update",
		func(ctx SpecContext, name string, create, mutate func(*slv.LocalStorageClass), messages ...string) {
			lsc := celLSC(name)
			create(lsc)
			Expect(createCelLSC(ctx, lsc)).To(Succeed())
			expectAdmission(patchCelLSC(ctx, lsc, mutate), messages...)
		},
		Entry("fsType changed", "cel-up-fstype",
			func(*slv.LocalStorageClass) {},
			func(l *slv.LocalStorageClass) { l.Spec.FSType = "xfs" },
			"spec.fsType: Invalid value: \"string\": "+celImmutable),
		Entry("reclaimPolicy changed", "cel-up-reclaim",
			func(*slv.LocalStorageClass) {},
			func(l *slv.LocalStorageClass) { l.Spec.ReclaimPolicy = "Retain" },
			"spec.reclaimPolicy: Invalid value: \"string\": "+celImmutable),
		Entry("volumeBindingMode changed", "cel-up-binding",
			func(*slv.LocalStorageClass) {},
			func(l *slv.LocalStorageClass) { l.Spec.VolumeBindingMode = "Immediate" },
			"spec.volumeBindingMode: Invalid value: \"string\": "+celImmutable),
		Entry("lvm.type changed, with the entries made valid for the new type", "cel-up-type",
			func(*slv.LocalStorageClass) {},
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Type = controller.LVMThinType
				l.Spec.LVM.LVMVolumeGroups[0].Thin = celThin()
			},
			"spec.lvm.type: Invalid value: \"string\": "+celImmutable),
		Entry("thick.contiguous changed", "cel-up-contiguous-changed",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: celBool(true)}
			},
			func(l *slv.LocalStorageClass) { l.Spec.LVM.Thick.Contiguous = celBool(false) },
			"spec.lvm.thick.contiguous: Invalid value: \"boolean\": "+celImmutable,
			"spec.lvm.thick: Invalid value: \"object\": "+celContiguousAdded,
			"spec.lvm: Invalid value: \"object\": "+celContiguousAdded),
		Entry("thick.contiguous removed", "cel-up-contiguous-removed",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: celBool(true)}
			},
			func(l *slv.LocalStorageClass) { l.Spec.LVM.Thick.Contiguous = nil },
			"spec.lvm.thick: Invalid value: \"object\": "+celContiguousRemoved),
		Entry("thick.contiguous added to an existing thick", "cel-up-contiguous-added",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{}
			},
			func(l *slv.LocalStorageClass) { l.Spec.LVM.Thick.Contiguous = celBool(true) },
			"spec.lvm.thick: Invalid value: \"object\": no such key: contiguous evaluating rule: "+celContiguousAdded,
			"spec.lvm: Invalid value: \"object\": no such key: contiguous evaluating rule: "+celContiguousAdded),
		Entry("thick with contiguous added where there was no thick", "cel-up-thick-added",
			func(*slv.LocalStorageClass) {},
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: celBool(true)}
			},
			"spec.lvm: Invalid value: \"object\": no such key: thick evaluating rule: "+celContiguousAdded),
		Entry("thick removed while it sets contiguous", "cel-up-thick-removed",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: celBool(true)}
			},
			func(l *slv.LocalStorageClass) { l.Spec.LVM.Thick = nil },
			celThickRemoved),
		Entry("thick without contiguous added and removed is admitted", "cel-up-thick-empty",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{}
			},
			func(l *slv.LocalStorageClass) { l.Spec.LVM.Thick = nil }),
		Entry("volumeCleanup and lvmVolumeGroups stay mutable", "cel-up-mutable",
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.Thick = &slv.LocalStorageClassLVMThickSpec{Contiguous: celBool(true)}
			},
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.VolumeCleanup = "Discard"
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{Name: "cel-lvg-b"}, {Name: "cel-lvg-c"}}
			}),
		Entry("name entries switched to labelSelector entries", "cel-up-selector",
			func(*slv.LocalStorageClass) {},
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.LVMVolumeGroups = []slv.LocalStorageClassLVG{{LabelSelector: celSelector()}}
			}),
		Entry("a name entry mixed in on update", "cel-up-mix",
			func(*slv.LocalStorageClass) {},
			func(l *slv.LocalStorageClass) {
				l.Spec.LVM.LVMVolumeGroups = append(l.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{LabelSelector: celSelector()})
			},
			celNoMix),
	)
})
