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

	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func TestCreateVolumeRejectsUnknownContentSource(t *testing.T) {
	lvg := &v1alpha1.LVMVolumeGroup{
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
	d := &Driver{log: logger.NewNop(), cl: newCleanupTestClient(t, lvg)}

	_, err := d.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:                cleanupTestVolume,
		CapacityRange:       &csi.CapacityRange{RequiredBytes: 64 << 20},
		VolumeCapabilities:  []*csi.VolumeCapability{{}},
		VolumeContentSource: &csi.VolumeContentSource{},
		Parameters: map[string]string{
			internal.TypeKey:           internal.Lvm,
			internal.LvmTypeKey:        internal.LVMTypeThin,
			internal.BindingModeKey:    internal.BindingModeWFFC,
			internal.LVMVolumeGroupKey: "- name: " + cleanupTestLVG + "\n  thin:\n    poolName: tp\n",
		},
	})

	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
