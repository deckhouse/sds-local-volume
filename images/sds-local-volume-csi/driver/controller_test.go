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

package driver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func TestFitsFreeSpace(t *testing.T) {
	tests := []struct {
		name        string
		bindingMode string
		lvmType     string
		alignedSize string
		freeSpace   string
		want        bool
	}{
		{
			// The case the check was moved for: 35Mi fits, but the node provisions
			// whole 4Mi extents and 36Mi does not. Before the move the raw request
			// passed here and failed on the node with "not enough space", after
			// CreateVolume had already blocked waiting for the LV.
			name:        "aligned_thick_request_no_longer_fits_after_rounding",
			bindingMode: internal.BindingModeI,
			lvmType:     internal.LVMTypeThick,
			alignedSize: "36Mi",
			freeSpace:   "35Mi",
			want:        false,
		},
		{
			name:        "thick_request_fits",
			bindingMode: internal.BindingModeI,
			lvmType:     internal.LVMTypeThick,
			alignedSize: "36Mi",
			freeSpace:   "1Gi",
			want:        true,
		},
		{
			name:        "thick_request_exactly_fills_free_space",
			bindingMode: internal.BindingModeI,
			lvmType:     internal.LVMTypeThick,
			alignedSize: "36Mi",
			freeSpace:   "36Mi",
			want:        true,
		},
		{
			// Thin volumes are over-provisioned by design, the pool is allowed to be
			// smaller than the sum of its volumes.
			name:        "thin_request_is_not_checked",
			bindingMode: internal.BindingModeI,
			lvmType:     internal.LVMTypeThin,
			alignedSize: "1Ti",
			freeSpace:   "1Mi",
			want:        true,
		},
		{
			// With WaitForFirstConsumer the node is picked by the scheduler, which has
			// already accounted for the space; maxFreeSpace is not even collected.
			name:        "wffc_thick_request_is_not_checked_here",
			bindingMode: internal.BindingModeWFFC,
			lvmType:     internal.LVMTypeThick,
			alignedSize: "36Mi",
			freeSpace:   "0",
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fitsFreeSpace(
				tt.bindingMode,
				tt.lvmType,
				resource.MustParse(tt.alignedSize),
				resource.MustParse(tt.freeSpace),
			)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveCapacityBytes(t *testing.T) {
	fallback := resource.MustParse("36Mi")

	t.Run("reports_the_size_that_exists_on_the_node", func(t *testing.T) {
		// A volume restored from a 36Mi source but requested at 52Mi ends up at 52Mi
		// on the node; a plain 35Mi request ends up at 36Mi. Either way the status is
		// the only value that describes the LV that was actually created.
		llv := &v1alpha1.LVMLogicalVolume{
			Status: &v1alpha1.LVMLogicalVolumeStatus{
				ActualSize: resource.MustParse("52Mi"),
			},
		}

		expected := resource.MustParse("52Mi")

		got, fromStatus := resolveCapacityBytes(llv, fallback)
		assert.True(t, fromStatus)
		assert.Equal(t, expected.Value(), got)
	})

	t.Run("falls_back_to_the_aligned_request_when_status_is_missing", func(t *testing.T) {
		got, fromStatus := resolveCapacityBytes(&v1alpha1.LVMLogicalVolume{}, fallback)
		assert.False(t, fromStatus)
		assert.Equal(t, fallback.Value(), got)
	})

	t.Run("falls_back_when_the_resource_itself_is_missing", func(t *testing.T) {
		got, fromStatus := resolveCapacityBytes(nil, fallback)
		assert.False(t, fromStatus)
		assert.Equal(t, fallback.Value(), got)
	})
}
