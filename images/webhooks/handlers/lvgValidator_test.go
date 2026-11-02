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
	"context"
	"encoding/json"
	"testing"

	"github.com/slok/kubewebhook/v2/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/webhooks/pkg/logger"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func lvgWithPools(lbls map[string]string, pools ...string) *snc.LVMVolumeGroup {
	lvg := &snc.LVMVolumeGroup{ObjectMeta: metav1.ObjectMeta{Name: "vg-a", Labels: lbls}}
	for _, p := range pools {
		lvg.Spec.ThinPools = append(lvg.Spec.ThinPools, snc.LVMVolumeGroupThinPoolSpec{Name: p, Size: "1Gi"})
	}
	return lvg
}

func thinClass(name string, entries ...slv.LocalStorageClassLVG) slv.LocalStorageClass {
	return slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       slv.LocalStorageClassSpec{LVM: &slv.LocalStorageClassLVMSpec{Type: "Thin", LVMVolumeGroups: entries}},
	}
}

func TestRemovedThinPools(t *testing.T) {
	removed := removedThinPools(lvgWithPools(nil, "kept", "gone"), lvgWithPools(nil, "kept", "added"))
	assert.Equal(t, map[string]struct{}{"gone": {}}, removed)
}

// A removed pool counts as used when a class takes it from this LVMVolumeGroup,
// whether by name or by labels; the same pool name on another group, a pool that
// stays, or a class being deleted does not count.
func TestThinPoolUses(t *testing.T) {
	lvg := lvgWithPools(map[string]string{"tier": "fast"})
	removed := map[string]struct{}{"gone": {}}
	now := metav1.Now()
	deleting := thinClass("deleting", slv.LocalStorageClassLVG{Name: "vg-a", Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "gone"}})
	deleting.DeletionTimestamp = &now

	uses, err := thinPoolUses(lvg, removed, []slv.LocalStorageClass{
		thinClass("by-name", slv.LocalStorageClassLVG{Name: "vg-a", Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "gone"}}),
		thinClass("by-labels", slv.LocalStorageClassLVG{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "fast"}},
			Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "gone"},
		}),
		thinClass("other-group", slv.LocalStorageClassLVG{Name: "vg-b", Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "gone"}}),
		thinClass("other-pool", slv.LocalStorageClassLVG{Name: "vg-a", Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "kept"}}),
		deleting,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"thin pool gone by by-labels", "thin pool gone by by-name"}, uses)
}

// An update that removes no thin pool, a create, and a group being deleted are
// let through without looking at the classes.
func TestLVGValidate_PassesWhatRemovesNoPool(t *testing.T) {
	validate := LVGValidate(logger.NewNop())
	oldRaw, err := json.Marshal(lvgWithPools(nil, "kept"))
	require.NoError(t, err)

	res, err := validate(context.Background(), &model.AdmissionReview{Operation: model.OperationUpdate, OldObjectRaw: oldRaw}, lvgWithPools(nil, "kept", "added"))
	require.NoError(t, err)
	assert.True(t, res.Valid)

	res, err = validate(context.Background(), &model.AdmissionReview{Operation: model.OperationCreate}, lvgWithPools(nil))
	require.NoError(t, err)
	assert.True(t, res.Valid)

	now := metav1.Now()
	deleting := lvgWithPools(nil)
	deleting.DeletionTimestamp = &now
	res, err = validate(context.Background(), &model.AdmissionReview{Operation: model.OperationUpdate, OldObjectRaw: oldRaw}, deleting)
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

func TestNewScheme_KnowsTheTypesTheValidatorsList(t *testing.T) {
	scheme, err := newScheme()
	require.NoError(t, err)

	for _, obj := range []runtime.Object{&slv.LocalStorageClassList{}, &snc.LVMVolumeGroupList{}} {
		_, _, err := scheme.ObjectKinds(obj)
		assert.NoError(t, err, "%T", obj)
	}
}
