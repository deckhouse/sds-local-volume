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

package handlers

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func TestLVGsOnNodesWithoutCSI(t *testing.T) {
	lvg := func(name, node string) snc.LVMVolumeGroup {
		return snc.LVMVolumeGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       snc.LVMVolumeGroupSpec{Local: snc.LVMVolumeGroupLocalSpec{NodeName: node}},
		}
	}
	lvgs := []snc.LVMVolumeGroup{
		lvg("lvg-b", "node-2"),
		lvg("lvg-a", "node-1"),
		lvg("lvg-c", "node-3"),
		lvg("lvg-unused", "node-9"),
	}
	csiNodes := []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}}}

	got := lvgsOnNodesWithoutCSI(lvgs, []string{"lvg-a", "lvg-b", "lvg-c"}, csiNodes)
	want := []string{"lvg-a (node-1)", "lvg-c (node-3)"}
	if !slices.Equal(got, want) {
		t.Fatalf("lvgsOnNodesWithoutCSI = %v, want %v", got, want)
	}

	if got := lvgsOnNodesWithoutCSI(lvgs, []string{"lvg-b"}, csiNodes); len(got) != 0 {
		t.Errorf("lvgsOnNodesWithoutCSI for an LVMVolumeGroup on a CSI node = %v, want none", got)
	}
}
