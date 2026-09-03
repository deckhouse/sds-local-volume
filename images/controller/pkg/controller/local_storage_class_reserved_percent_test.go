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

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/deckhouse/sds-common-lib/fsreserve"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
)

// The reserved share is an annotation on the LocalStorageClass, and a CSI
// driver only ever sees StorageClass.parameters. These tests pin the hand-over
// between the two.
func TestConfigureStorageClass_ReservedPercent(t *testing.T) {
	lvgs := []slv.LocalStorageClassLVG{{Name: "lvg-1"}}

	newLSC := func(annotations map[string]string) *slv.LocalStorageClass {
		return &slv.LocalStorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: testLSCName, Annotations: annotations},
			Spec: slv.LocalStorageClassSpec{
				ReclaimPolicy:     "Delete",
				VolumeBindingMode: "WaitForFirstConsumer",
				LVM: &slv.LocalStorageClassLVMSpec{
					Type:            LVMThickType,
					LVMVolumeGroups: lvgs,
				},
			},
		}
	}

	t.Run("no annotation leaves the parameter out", func(t *testing.T) {
		sc, err := configureStorageClass(newLSC(nil), lvgs, nil)
		if err != nil {
			t.Fatalf("configureStorageClass: %v", err)
		}
		if value, ok := sc.Parameters[fsreserve.ParameterKey]; ok {
			t.Errorf("parameter %s = %q, want it absent", fsreserve.ParameterKey, value)
		}
	})

	t.Run("an explicit zero also leaves the parameter out", func(t *testing.T) {
		sc, err := configureStorageClass(newLSC(map[string]string{fsreserve.AnnotationKey: "0"}), lvgs, nil)
		if err != nil {
			t.Fatalf("configureStorageClass: %v", err)
		}
		if value, ok := sc.Parameters[fsreserve.ParameterKey]; ok {
			t.Errorf("parameter %s = %q, want it absent", fsreserve.ParameterKey, value)
		}
	})

	t.Run("a percentage reaches the parameters", func(t *testing.T) {
		sc, err := configureStorageClass(newLSC(map[string]string{fsreserve.AnnotationKey: "5"}), lvgs, nil)
		if err != nil {
			t.Fatalf("configureStorageClass: %v", err)
		}
		if got := sc.Parameters[fsreserve.ParameterKey]; got != "5" {
			t.Errorf("parameter %s = %q, want %q", fsreserve.ParameterKey, got, "5")
		}
	})

	t.Run("an invalid value fails the StorageClass instead of every volume", func(t *testing.T) {
		if _, err := configureStorageClass(newLSC(map[string]string{fsreserve.AnnotationKey: "300"}), lvgs, nil); err == nil {
			t.Error("configureStorageClass accepted a percentage above the bound")
		}
	})

	t.Run("unrelated annotations are ignored", func(t *testing.T) {
		sc, err := configureStorageClass(newLSC(map[string]string{"example.com/other": "nonsense"}), lvgs, nil)
		if err != nil {
			t.Fatalf("configureStorageClass: %v", err)
		}
		if _, ok := sc.Parameters[fsreserve.ParameterKey]; ok {
			t.Error("an unrelated annotation produced the reserve parameter")
		}
	})
}
