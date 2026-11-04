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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/deckhouse/sds-common-lib/logger"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func TestWaitForStatusUpdate_FailedLLVNamesTheReason(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, snc.AddToScheme(scheme))

	llv := &snc.LVMLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1"},
		Status: &snc.LVMLogicalVolumeStatus{
			Phase:  LLVStatusFailed,
			Reason: "not enough space",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(llv).Build()

	_, _, err := WaitForStatusUpdate(context.Background(), cl, logger.NewNop(), "pvc-1", "",
		resource.MustParse("1Gi"), resource.MustParse("4Mi"))

	// The caller prefixes the LVMVolumeGroup and its node; the message must not
	// claim a node it does not name.
	assert.EqualError(t, err, "failed to create the logical volume of LVMLogicalVolume pvc-1, reason: not enough space")
}
