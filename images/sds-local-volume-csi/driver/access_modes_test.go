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

package driver

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func mountCapability(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
	}
}

func blockCapability(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
	}
}

type accessModeCase struct {
	name         string
	capabilities []*csi.VolumeCapability
	wantErr      bool
}

func accessModeCases() []accessModeCase {
	var cases []accessModeCase

	modes := []struct {
		mode    csi.VolumeCapability_AccessMode_Mode
		allowed bool
	}{
		{csi.VolumeCapability_AccessMode_UNKNOWN, false},
		{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, true},
		{csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY, true},
		{csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY, false},
		{csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER, false},
		{csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER, false},
		{csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER, false},
		{csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER, false},
	}

	for _, m := range modes {
		cases = append(cases,
			accessModeCase{"mount " + m.mode.String(), []*csi.VolumeCapability{mountCapability(m.mode)}, !m.allowed},
			accessModeCase{"block " + m.mode.String(), []*csi.VolumeCapability{blockCapability(m.mode)}, !m.allowed},
		)
	}

	cases = append(cases,
		accessModeCase{
			"no access type",
			[]*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
			true,
		},
		accessModeCase{
			"no access mode",
			[]*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}},
			true,
		},
		accessModeCase{"nil capability", []*csi.VolumeCapability{nil}, true},
		accessModeCase{"empty list", []*csi.VolumeCapability{}, true},
		accessModeCase{
			"all capabilities single node",
			[]*csi.VolumeCapability{
				mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
				blockCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY),
			},
			false,
		},
		accessModeCase{
			"one multi node capability among valid ones",
			[]*csi.VolumeCapability{
				mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
				mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER),
			},
			true,
		},
	)

	return cases
}

func TestValidateVolumeCapabilitiesFunc(t *testing.T) {
	for _, tc := range accessModeCases() {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVolumeCapabilities(tc.capabilities)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateVolumeCapabilitiesErrorNamesMode(t *testing.T) {
	err := validateVolumeCapabilities([]*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MULTI_NODE_MULTI_WRITER")
	assert.Contains(t, err.Error(), "local to a single node")
}

func accessModeTestLVG() *v1alpha1.LVMVolumeGroup {
	return &v1alpha1.LVMVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{Name: cleanupTestLVG},
		Spec: v1alpha1.LVMVolumeGroupSpec{
			ActualVGNameOnTheNode: "vg",
			Local:                 v1alpha1.LVMVolumeGroupLocalSpec{NodeName: cleanupTestNode},
		},
		Status: v1alpha1.LVMVolumeGroupStatus{
			Phase:      v1alpha1.PhaseReady,
			Nodes:      []v1alpha1.LVMVolumeGroupNode{{Name: cleanupTestNode}},
			ExtentSize: resource.MustParse("4Mi"),
			VGFree:     resource.MustParse("1Gi"),
		},
	}
}

func TestCreateVolumeAccessModes(t *testing.T) {
	for _, tc := range accessModeCases() {
		t.Run(tc.name, func(t *testing.T) {
			cl := newCleanupTestClient(t, accessModeTestLVG())
			d := &Driver{log: logger.NewNop(), cl: cl}

			_, err := d.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
				Name:               cleanupTestVolume,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: 64 << 20},
				VolumeCapabilities: tc.capabilities,
				Parameters: map[string]string{
					internal.TypeKey:           internal.Lvm,
					internal.LvmTypeKey:        internal.LVMTypeThick,
					internal.BindingModeKey:    internal.BindingModeWFFC,
					internal.LVMVolumeGroupKey: "- name: " + cleanupTestLVG + "\n",
				},
				AccessibilityRequirements: &csi.TopologyRequirement{
					Preferred: []*csi.Topology{{Segments: map[string]string{internal.TopologyKey: cleanupTestNode}}},
				},
			})

			var llv v1alpha1.LVMLogicalVolume
			getErr := cl.Get(context.Background(), client.ObjectKey{Name: cleanupTestVolume}, &llv)

			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, codes.InvalidArgument, status.Code(err))
				assert.Error(t, getErr, "no LVMLogicalVolume is created for a rejected request")
				return
			}
			require.NoError(t, err)
			require.NoError(t, getErr)
		})
	}
}

func TestValidateVolumeCapabilitiesRPC(t *testing.T) {
	llv := &v1alpha1.LVMLogicalVolume{ObjectMeta: metav1.ObjectMeta{Name: cleanupTestVolume}}

	for _, tc := range accessModeCases() {
		t.Run(tc.name, func(t *testing.T) {
			d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t, llv)}

			resp, err := d.ValidateVolumeCapabilities(context.Background(), &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           cleanupTestVolume,
				VolumeCapabilities: tc.capabilities,
			})

			require.NoError(t, err)
			require.NotNil(t, resp)
			if tc.wantErr {
				assert.Nil(t, resp.Confirmed)
				assert.NotEmpty(t, resp.Message)
				return
			}
			require.NotNil(t, resp.Confirmed)
			assert.Equal(t, tc.capabilities, resp.Confirmed.VolumeCapabilities)
			assert.Empty(t, resp.Message)
		})
	}
}

func TestValidateVolumeCapabilitiesRequiresVolumeID(t *testing.T) {
	d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t)}

	_, err := d.ValidateVolumeCapabilities(context.Background(), &csi.ValidateVolumeCapabilitiesRequest{
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)},
	})

	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestValidateVolumeCapabilitiesUnknownVolume(t *testing.T) {
	d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t)}

	_, err := d.ValidateVolumeCapabilities(context.Background(), &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId:           "missing",
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)},
	})

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
