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

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func testLVG(name, node, phase, vgFree string) snc.LVMVolumeGroup {
	return snc.LVMVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: snc.LVMVolumeGroupStatus{
			Phase:  phase,
			VGFree: resource.MustParse(vgFree),
			Nodes:  []snc.LVMVolumeGroupNode{{Name: node}},
		},
	}
}

func TestGetNodeWithMaxFreeSpace_SkipsNotReadyLVGs(t *testing.T) {
	t.Run("the biggest group is skipped when it is not Ready", func(t *testing.T) {
		lvgs := []snc.LVMVolumeGroup{
			testLVG("vg-1", "worker-1", snc.PhaseReady, "10Gi"),
			testLVG("vg-2", "worker-2", snc.PhaseNotReady, "100Gi"),
		}
		node, free, err := GetNodeWithMaxFreeSpace(lvgs, nil, internal.LVMTypeThick)
		require.NoError(t, err)
		assert.Equal(t, "worker-1", node)
		want := resource.MustParse("10Gi")
		assert.Equal(t, want.Value(), free.Value())
	})

	t.Run("no Ready group is an error that names the groups and their phases", func(t *testing.T) {
		lvgs := []snc.LVMVolumeGroup{
			testLVG("vg-1", "worker-1", snc.PhaseNotReady, "10Gi"),
			testLVG("vg-2", "worker-2", "", "100Gi"),
		}
		_, _, err := GetNodeWithMaxFreeSpace(lvgs, nil, internal.LVMTypeThick)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "vg-1 (NotReady)")
		assert.Contains(t, err.Error(), "vg-2 (no phase)")
	})

	t.Run("a thin group that is not Ready does not need its thin pool in the parameters", func(t *testing.T) {
		ready := testLVG("vg-1", "worker-1", snc.PhaseReady, "0")
		ready.Status.ThinPools = []snc.LVMVolumeGroupThinPoolStatus{{Name: "tp", AvailableSpace: resource.MustParse("5Gi")}}
		lvgs := []snc.LVMVolumeGroup{ready, testLVG("vg-2", "worker-2", snc.PhaseNotReady, "0")}

		node, _, err := GetNodeWithMaxFreeSpace(lvgs, map[string]string{"vg-1": "tp"}, internal.LVMTypeThin)
		require.NoError(t, err)
		assert.Equal(t, "worker-1", node)
	})
}

func TestSelectLVG_RefusesNotReadyLVG(t *testing.T) {
	lvgs := []snc.LVMVolumeGroup{
		testLVG("vg-1", "worker-1", snc.PhaseReady, "10Gi"),
		testLVG("vg-2", "worker-2", snc.PhasePending, "10Gi"),
	}

	got, err := SelectLVG(lvgs, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, "vg-1", got.Name)

	_, err = SelectLVG(lvgs, "worker-2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LVMVolumeGroup vg-2 on node worker-2 is not Ready (Pending)")

	_, err = SelectLVG(lvgs, "worker-3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no LVMVolumeGroup found for node worker-3")
}
