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

package controller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

// LocalStorageClassLVGs returns the listing behind
// monitoring.LocalStorageClassLVGInfo: every LVMVolumeGroup each
// LocalStorageClass in reader resolves to, by the same rules that fill the
// StorageClass parameter.
//
// A class whose entries do not resolve — overlapping selectors that disagree on
// the thin pool — is left out rather than failing the listing. The class itself
// reports that through its Ready condition, and the other classes are still
// worth reading.
func LocalStorageClassLVGs(reader client.Reader) func(context.Context) ([]monitoring.LocalStorageClassLVG, error) {
	return func(ctx context.Context) ([]monitoring.LocalStorageClassLVG, error) {
		lscList := &slv.LocalStorageClassList{}
		if err := reader.List(ctx, lscList); err != nil {
			return nil, fmt.Errorf("list LocalStorageClasses: %w", err)
		}

		lvgList := &snc.LVMVolumeGroupList{}
		for i := range lscList.Items {
			if lscUsesLabelSelector(&lscList.Items[i]) {
				if err := reader.List(ctx, lvgList); err != nil {
					return nil, fmt.Errorf("list LVMVolumeGroups: %w", err)
				}
				break
			}
		}

		var entries []monitoring.LocalStorageClassLVG
		for i := range lscList.Items {
			lsc := &lscList.Items[i]
			if lsc.Spec.LVM == nil {
				continue
			}
			resolved, err := resolveEffectiveLVGs(lsc, lvgList)
			if err != nil {
				continue
			}
			thin := lsc.Spec.LVM.Type == LVMThinType
			for _, lvg := range resolved {
				entry := monitoring.LocalStorageClassLVG{
					StorageClass:   lsc.Name,
					LVMVolumeGroup: lvg.Name,
				}
				if thin {
					entry.ThinPool = thinPoolName(lvg.Thin)
				}
				entries = append(entries, entry)
			}
		}
		return entries, nil
	}
}
