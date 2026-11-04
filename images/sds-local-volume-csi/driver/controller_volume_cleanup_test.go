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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/deckhouse/sds-common-lib/logger"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/lib/go/common/pkg/feature"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	cleanupTestNode    = "worker-1"
	cleanupTestLVG     = "vg-worker-1"
	cleanupTestLSC     = "local-thick"
	cleanupTestVolume  = "pvc-cleanup"
	cleanupTestRefused = "not supported in your edition"
)

func newCleanupTestClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, slv.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if llv, ok := obj.(*v1alpha1.LVMLogicalVolume); ok {
					llv.Status = &v1alpha1.LVMLogicalVolumeStatus{Phase: "Created", ActualSize: resource.MustParse(llv.Spec.Size)}
				}
				return cl.Create(ctx, obj, opts...)
			},
		}).
		Build()
}

func TestCreateVolumeAcceptsVolumeCleanupWhereEnabled(t *testing.T) {
	ctx := context.Background()
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
	cl := newCleanupTestClient(t, lvg)
	d := &Driver{log: logger.NewNop(), cl: cl}

	_, err := d.CreateVolume(ctx, &csi.CreateVolumeRequest{
		Name:               cleanupTestVolume,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: 64 << 20},
		VolumeCapabilities: []*csi.VolumeCapability{{}},
		Parameters: map[string]string{
			internal.TypeKey:           internal.Lvm,
			internal.LvmTypeKey:        internal.LVMTypeThick,
			internal.BindingModeKey:    internal.BindingModeWFFC,
			internal.LVMVolumeGroupKey: "- name: " + cleanupTestLVG + "\n",
			LVMVolumeCleanupParamKey:   v1alpha1.VolumeCleanupRandomFillSinglePass,
		},
		AccessibilityRequirements: &csi.TopologyRequirement{
			Preferred: []*csi.Topology{{Segments: map[string]string{internal.TopologyKey: cleanupTestNode}}},
		},
	})

	var llv v1alpha1.LVMLogicalVolume
	getErr := cl.Get(ctx, client.ObjectKey{Name: cleanupTestVolume}, &llv)

	if !feature.VolumeCleanupEnabled() {
		require.ErrorContains(t, err, cleanupTestRefused)
		assert.Error(t, getErr, "no LVMLogicalVolume is created when volumeCleanup is refused")
		return
	}

	require.NoError(t, err)
	require.NoError(t, getErr)
	require.NotNil(t, llv.Spec.VolumeCleanup)
	assert.Equal(t, v1alpha1.VolumeCleanupRandomFillSinglePass, *llv.Spec.VolumeCleanup)
}

func TestDeleteVolumePassesVolumeCleanupWhereEnabled(t *testing.T) {
	ctx := context.Background()
	cl := newCleanupTestClient(t,
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: cleanupTestVolume},
			Spec:       corev1.PersistentVolumeSpec{StorageClassName: cleanupTestLSC},
		},
		&slv.LocalStorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: cleanupTestLSC},
			Spec: slv.LocalStorageClassSpec{LVM: &slv.LocalStorageClassLVMSpec{
				Type:          internal.LVMTypeThick,
				VolumeCleanup: v1alpha1.VolumeCleanupRandomFillSinglePass,
			}},
		},
		&v1alpha1.LVMLogicalVolume{ObjectMeta: metav1.ObjectMeta{
			Name:       cleanupTestVolume,
			Finalizers: []string{"storage.deckhouse.io/sds-node-configurator"},
		}},
	)
	d := &Driver{log: logger.NewNop(), cl: cl}

	_, err := d.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: cleanupTestVolume})

	var llv v1alpha1.LVMLogicalVolume
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: cleanupTestVolume}, &llv))

	if !feature.VolumeCleanupEnabled() {
		require.ErrorContains(t, err, cleanupTestRefused)
		assert.Nil(t, llv.DeletionTimestamp)
		assert.Nil(t, llv.Spec.VolumeCleanup)
		return
	}

	require.NoError(t, err)
	assert.NotNil(t, llv.DeletionTimestamp)
	require.NotNil(t, llv.Spec.VolumeCleanup)
	assert.Equal(t, v1alpha1.VolumeCleanupRandomFillSinglePass, *llv.Spec.VolumeCleanup)
}
