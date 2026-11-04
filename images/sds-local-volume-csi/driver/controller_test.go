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
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func TestCheckFreeSpace(t *testing.T) {
	const thinPoolName = "tp"

	// The thin pool is 1Gi with allocationLimit 150%, and 1Gi of its 1.5Gi is
	// already handed out, so the agent reports 512Mi available.
	lvg := func(vgFree string) *v1alpha1.LVMVolumeGroup {
		return &v1alpha1.LVMVolumeGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "vg-worker-1"},
			Status: v1alpha1.LVMVolumeGroupStatus{
				VGFree: resource.MustParse(vgFree),
				ThinPools: []v1alpha1.LVMVolumeGroupThinPoolStatus{{
					Name:            thinPoolName,
					ActualSize:      resource.MustParse("1Gi"),
					AllocationLimit: "150%",
					AllocatedSize:   resource.MustParse("1Gi"),
					AvailableSpace:  resource.MustParse("512Mi"),
				}},
			},
		}
	}

	tests := []struct {
		name         string
		bindingMode  string
		lvmType      string
		alignedSize  string
		vgFree       string
		thinPoolName string
		// wantMsg is empty when the request fits, otherwise the substrings the
		// ResourceExhausted message must carry.
		wantMsg []string
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
			vgFree:      "35Mi",
			wantMsg:     []string{"requested size 36Mi", "free space of the volume group: 35Mi"},
		},
		{
			name:        "thick_request_fits",
			bindingMode: internal.BindingModeI,
			lvmType:     internal.LVMTypeThick,
			alignedSize: "36Mi",
			vgFree:      "1Gi",
		},
		{
			name:        "thick_request_exactly_fills_free_space",
			bindingMode: internal.BindingModeI,
			lvmType:     internal.LVMTypeThick,
			alignedSize: "36Mi",
			vgFree:      "36Mi",
		},
		{
			// AvailableSpace already counts allocationLimit, and the agent fails an
			// LVMLogicalVolume bigger than it with "not enough space".
			name:         "thin_request_bigger_than_available_space",
			bindingMode:  internal.BindingModeI,
			lvmType:      internal.LVMTypeThin,
			alignedSize:  "516Mi",
			vgFree:       "0",
			thinPoolName: thinPoolName,
			wantMsg:      []string{"requested size 516Mi", "thin pool tp: 512Mi", "allocationLimit 150%", "1Gi already allocated"},
		},
		{
			name:         "thin_request_exactly_fills_available_space",
			bindingMode:  internal.BindingModeI,
			lvmType:      internal.LVMTypeThin,
			alignedSize:  "512Mi",
			vgFree:       "0",
			thinPoolName: thinPoolName,
		},
		{
			// VGFree has nothing to do with a thin volume.
			name:         "thin_request_fits",
			bindingMode:  internal.BindingModeI,
			lvmType:      internal.LVMTypeThin,
			alignedSize:  "36Mi",
			vgFree:       "0",
			thinPoolName: thinPoolName,
		},
		{
			// With WaitForFirstConsumer the node is picked by the scheduler, which has
			// already accounted for the space.
			name:        "wffc_thick_request_is_not_checked_here",
			bindingMode: internal.BindingModeWFFC,
			lvmType:     internal.LVMTypeThick,
			alignedSize: "36Mi",
			vgFree:      "0",
		},
		{
			name:         "wffc_thin_request_is_not_checked_here",
			bindingMode:  internal.BindingModeWFFC,
			lvmType:      internal.LVMTypeThin,
			alignedSize:  "1Ti",
			vgFree:       "0",
			thinPoolName: thinPoolName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFreeSpace(tt.bindingMode, tt.lvmType, resource.MustParse(tt.alignedSize), lvg(tt.vgFree), tt.thinPoolName)
			if len(tt.wantMsg) == 0 {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, codes.ResourceExhausted, status.Code(err))
			for _, s := range tt.wantMsg {
				assert.Contains(t, status.Convert(err).Message(), s)
			}
		})
	}

	t.Run("thin_pool_missing_from_the_status", func(t *testing.T) {
		err := checkFreeSpace(internal.BindingModeI, internal.LVMTypeThin, resource.MustParse("36Mi"), lvg("0"), "other")
		assert.Equal(t, codes.Internal, status.Code(err))
		assert.Contains(t, status.Convert(err).Message(), "thin pool other not found in the status of LVMVolumeGroup vg-worker-1")
	})

	t.Run("keeps_resource_exhausted_through_errOnLVG", func(t *testing.T) {
		l := lvg("35Mi")
		l.Spec.Local.NodeName = "worker-1"
		err := errOnLVG(l, checkFreeSpace(internal.BindingModeI, internal.LVMTypeThick, resource.MustParse("36Mi"), l, ""))
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		assert.Contains(t, status.Convert(err).Message(), "LVMVolumeGroup vg-worker-1 on node worker-1")
	})
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

func TestErrOnLVG(t *testing.T) {
	lvg := &v1alpha1.LVMVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "vg-worker-1"},
		Spec:       v1alpha1.LVMVolumeGroupSpec{Local: v1alpha1.LVMVolumeGroupLocalSpec{NodeName: "worker-1"}},
	}

	t.Run("names_the_lvg_and_the_node", func(t *testing.T) {
		err := errOnLVG(lvg, errors.New("failed to create the logical volume of LVMLogicalVolume pvc-1, reason: no space"))
		assert.EqualError(t, err, "LVMVolumeGroup vg-worker-1 on node worker-1: failed to create the logical volume of LVMLogicalVolume pvc-1, reason: no space")
	})

	t.Run("keeps_the_grpc_code_of_a_status_error", func(t *testing.T) {
		err := errOnLVG(lvg, status.Errorf(codes.Internal, "requested size: 2Gi is greater than free space: 1Gi"))
		assert.Equal(t, codes.Internal, status.Code(err))
		assert.Contains(t, status.Convert(err).Message(), "on node worker-1")
	})

	t.Run("keeps_a_context_error_recognizable", func(t *testing.T) {
		// grpc-go maps a handler error that is not a status through
		// status.FromContextError, which looks through the wrapping.
		err := errOnLVG(lvg, fmt.Errorf("wait: %w", context.DeadlineExceeded))
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, codes.DeadlineExceeded, status.FromContextError(err).Code())
	})
}
