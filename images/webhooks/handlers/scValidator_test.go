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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/deckhouse/sds-common-lib/logger"
)

func storageClassRaw(t *testing.T, mutate func(*storagev1.StorageClass)) []byte {
	t.Helper()
	sc := storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "local"},
		Provisioner: "local.csi.storage.deckhouse.io",
	}
	if mutate != nil {
		mutate(&sc)
	}
	raw, err := json.Marshal(sc)
	require.NoError(t, err)
	return raw
}

func TestIsStorageClassChangedExceptAnnotationsUnsetPointers(t *testing.T) {
	wffc := storagev1.VolumeBindingWaitForFirstConsumer

	tests := []struct {
		name    string
		old     func(*storagev1.StorageClass)
		new     func(*storagev1.StorageClass)
		changed bool
	}{
		{
			name:    "both unset",
			changed: false,
		},
		{
			name:    "volumeBindingMode set only in the new object",
			new:     func(sc *storagev1.StorageClass) { sc.VolumeBindingMode = &wffc },
			changed: true,
		},
		{
			name:    "reclaimPolicy set only in the old object",
			old:     func(sc *storagev1.StorageClass) { sc.ReclaimPolicy = ptr.To(corev1.PersistentVolumeReclaimDelete) },
			changed: true,
		},
		{
			name:    "allowVolumeExpansion set only in the new object",
			new:     func(sc *storagev1.StorageClass) { sc.AllowVolumeExpansion = ptr.To(true) },
			changed: true,
		},
		{
			name: "same values set in both",
			old: func(sc *storagev1.StorageClass) {
				sc.VolumeBindingMode = &wffc
				sc.AllowVolumeExpansion = ptr.To(true)
			},
			new: func(sc *storagev1.StorageClass) {
				sc.VolumeBindingMode = &wffc
				sc.AllowVolumeExpansion = ptr.To(true)
			},
			changed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, err := isStorageClassChangedExceptAnnotations(logger.NewNop(), storageClassRaw(t, tt.old), storageClassRaw(t, tt.new))
			require.NoError(t, err)
			assert.Equal(t, tt.changed, changed)
		})
	}
}
