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
	"fmt"
	"sort"
	"strings"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/deckhouse/sds-common-lib/logger"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

// LVGValidate returns the LVMVolumeGroup validator, bound to log.
//
// It keeps a thin pool that a LocalStorageClass uses in spec.thinPools.
// sds-node-configurator removes a thin pool dropped from the spec as soon as it
// holds no volumes, and the class would then provision into a pool that is gone.
func LVGValidate(log logger.Logger) kwhvalidating.ValidatorFunc {
	log = log.Named("lvg-validator")

	return func(ctx context.Context, ar *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
		lvg, ok := obj.(*snc.LVMVolumeGroup)
		if !ok || ar.Operation != model.OperationUpdate || lvg.GetDeletionTimestamp() != nil {
			return &kwhvalidating.ValidatorResult{Valid: true}, nil
		}

		oldLVG := &snc.LVMVolumeGroup{}
		if err := json.Unmarshal(ar.OldObjectRaw, oldLVG); err != nil {
			log.Error("unable to decode the old LVMVolumeGroup", logger.Err(err))
			return nil, err
		}

		removed := removedThinPools(oldLVG, lvg)
		if len(removed) == 0 {
			return &kwhvalidating.ValidatorResult{Valid: true}, nil
		}

		cl, err := NewKubeClient("")
		if err != nil {
			log.Error("unable to build a kube client", logger.Err(err))
			return nil, err
		}

		lscs := &slv.LocalStorageClassList{}
		if err := cl.List(ctx, lscs); err != nil {
			log.Error("unable to list the LocalStorageClasses", logger.Err(err))
			return nil, err
		}

		uses, err := thinPoolUses(lvg, removed, lscs.Items)
		if err != nil {
			log.Error("unable to match the LocalStorageClasses", logger.Err(err))
			return nil, err
		}
		if len(uses) > 0 {
			msg := fmt.Sprintf("thin pools of the LVMVolumeGroup %s are used by LocalStorageClasses: %s; remove them from the classes first",
				lvg.Name, strings.Join(uses, "; "))
			log.Info(msg)
			return &kwhvalidating.ValidatorResult{Valid: false, Message: msg}, nil
		}

		return &kwhvalidating.ValidatorResult{Valid: true}, nil
	}
}

// removedThinPools returns the names of the thin pools in oldLVG's spec that
// newLVG's spec no longer has.
func removedThinPools(oldLVG, newLVG *snc.LVMVolumeGroup) map[string]struct{} {
	kept := make(map[string]struct{}, len(newLVG.Spec.ThinPools))
	for _, tp := range newLVG.Spec.ThinPools {
		kept[tp.Name] = struct{}{}
	}

	removed := make(map[string]struct{})
	for _, tp := range oldLVG.Spec.ThinPools {
		if _, ok := kept[tp.Name]; !ok {
			removed[tp.Name] = struct{}{}
		}
	}
	return removed
}

// thinPoolUses returns "pool used by class" for every class that is not being
// deleted and takes one of the removed pools of lvg, whether it names lvg or
// selects it by labels.
func thinPoolUses(lvg *snc.LVMVolumeGroup, removed map[string]struct{}, lscs []slv.LocalStorageClass) ([]string, error) {
	var uses []string
	for _, lsc := range lscs {
		if lsc.GetDeletionTimestamp() != nil || lsc.Spec.LVM == nil {
			continue
		}
		for _, entry := range lsc.Spec.LVM.LVMVolumeGroups {
			if entry.Thin == nil {
				continue
			}
			if _, ok := removed[entry.Thin.PoolName]; !ok {
				continue
			}
			selects, err := entrySelects(entry, lvg)
			if err != nil {
				return nil, fmt.Errorf("LocalStorageClass %s: %w", lsc.Name, err)
			}
			if selects {
				uses = append(uses, fmt.Sprintf("thin pool %s by %s", entry.Thin.PoolName, lsc.Name))
			}
		}
	}
	sort.Strings(uses)
	return uses, nil
}

func entrySelects(entry slv.LocalStorageClassLVG, lvg *snc.LVMVolumeGroup) (bool, error) {
	if entry.Name != "" {
		return entry.Name == lvg.Name, nil
	}
	if entry.LabelSelector == nil {
		return false, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(entry.LabelSelector)
	if err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(lvg.Labels)), nil
}
