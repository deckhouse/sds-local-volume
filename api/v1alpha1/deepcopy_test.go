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

package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeepCopyInto used to stop at TypeMeta and ObjectMeta, leaving the Status
// pointer shared with the original. Anything that snapshots an object, mutates
// the copy and compares the two — which is how a status writer decides whether
// it needs to write at all — would see no difference and skip the write forever.
func TestLocalStorageClassDeepCopyIsolatesStatus(t *testing.T) {
	orig := &LocalStorageClass{
		Status: &LocalStorageClassStatus{
			Phase: "Created",
			Conditions: []metav1.Condition{
				{Type: ConditionTypeReady, Status: metav1.ConditionTrue},
			},
		},
	}

	cp := orig.DeepCopy()
	if cp.Status == orig.Status {
		t.Fatal("the Status pointer is shared with the original")
	}

	cp.Status.Phase = "Failed"
	cp.Status.Conditions[0].Status = metav1.ConditionFalse

	if orig.Status.Phase != "Created" {
		t.Errorf("phase is aliased: %q", orig.Status.Phase)
	}
	if orig.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Errorf("conditions are aliased: %q", orig.Status.Conditions[0].Status)
	}
}

// Spec was left on the shallow *out = *in for the same reason Status was, and
// it is the more dangerous half: the controller-runtime cache deep-copies the
// indexed object to hand it to the reconciler, so an aliased Spec.LVM points
// into the informer store that the watch goroutines read concurrently. Nothing
// writes lsc.Spec today; this test is what keeps that from becoming a data race
// the first time something does.
func TestLocalStorageClassDeepCopyIsolatesSpec(t *testing.T) {
	contiguous := true
	orig := &LocalStorageClass{
		Spec: LocalStorageClassSpec{
			LVM: &LocalStorageClassLVMSpec{
				Type:  "Thin",
				Thick: &LocalStorageClassLVMThickSpec{Contiguous: &contiguous},
				LVMVolumeGroups: []LocalStorageClassLVG{{
					Name:          "lvg-1",
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"zone": "a"}},
					Thin:          &LocalStorageClassLVMThinPoolSpec{PoolName: "pool-1"},
				}},
			},
		},
	}

	cp := orig.DeepCopy()
	if cp.Spec.LVM == orig.Spec.LVM {
		t.Fatal("the Spec.LVM pointer is shared with the original")
	}

	cp.Spec.LVM.Type = "Thick"
	*cp.Spec.LVM.Thick.Contiguous = false
	cp.Spec.LVM.LVMVolumeGroups[0].Name = "lvg-2"
	cp.Spec.LVM.LVMVolumeGroups[0].Thin.PoolName = "pool-2"
	cp.Spec.LVM.LVMVolumeGroups[0].LabelSelector.MatchLabels["zone"] = "b"

	if orig.Spec.LVM.Type != "Thin" {
		t.Errorf("lvm.type is aliased: %q", orig.Spec.LVM.Type)
	}
	if !*orig.Spec.LVM.Thick.Contiguous {
		t.Error("lvm.thick.contiguous is aliased")
	}

	origLVG := orig.Spec.LVM.LVMVolumeGroups[0]
	if origLVG.Name != "lvg-1" {
		t.Errorf("lvmVolumeGroups is aliased: %q", origLVG.Name)
	}
	if origLVG.Thin.PoolName != "pool-1" {
		t.Errorf("lvmVolumeGroups[].thin is aliased: %q", origLVG.Thin.PoolName)
	}
	if origLVG.LabelSelector.MatchLabels["zone"] != "a" {
		t.Errorf("lvmVolumeGroups[].labelSelector is aliased: %q", origLVG.LabelSelector.MatchLabels["zone"])
	}
}
