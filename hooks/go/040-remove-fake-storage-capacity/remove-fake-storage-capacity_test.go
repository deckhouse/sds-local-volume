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

package hooks_common

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/deckhouse/sds-local-volume/hooks/go/consts"
)

func testCapacity(name, namespace, driver string) *storagev1.CSIStorageCapacity {
	return &storagev1.CSIStorageCapacity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"csi.storage.k8s.io/drivername": driver},
		},
		StorageClassName: "sc",
	}
}

func TestRemoveStorageCapacities(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, storagev1.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		testCapacity("ours-1", consts.ModuleNamespace, consts.CSIDriverName),
		testCapacity("ours-2", consts.ModuleNamespace, consts.CSIDriverName),
		testCapacity("other-driver", consts.ModuleNamespace, "other.csi.example.com"),
		testCapacity("other-namespace", "other", consts.CSIDriverName),
	).Build()

	deleted, err := removeStorageCapacities(context.Background(), cl)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)

	left := &storagev1.CSIStorageCapacityList{}
	require.NoError(t, cl.List(context.Background(), left))
	names := []string{}
	for _, c := range left.Items {
		names = append(names, c.Name)
	}
	assert.ElementsMatch(t, []string{"other-driver", "other-namespace"}, names)

	deleted, err = removeStorageCapacities(context.Background(), cl)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}
