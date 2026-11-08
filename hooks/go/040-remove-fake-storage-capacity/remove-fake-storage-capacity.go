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
	"errors"
	"fmt"

	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
	storagev1 "k8s.io/api/storage/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/sds-common-lib/kubeclient"
	"github.com/deckhouse/sds-local-volume/hooks/go/consts"
)

const hookName = "remove-fake-storage-capacity"

var _ = registry.RegisterFunc(configRemoveFakeStorageCapacity, handlerRemoveFakeStorageCapacity)

var configRemoveFakeStorageCapacity = &pkg.HookConfig{
	OnAfterHelm: &pkg.OrderedConfig{Order: 10},
}

func removeStorageCapacities(ctx context.Context, cl client.Client) (int, error) {
	list := &storagev1.CSIStorageCapacityList{}
	if err := cl.List(ctx, list,
		client.InNamespace(consts.ModuleNamespace),
		client.MatchingLabels{"csi.storage.k8s.io/drivername": consts.CSIDriverName},
	); err != nil {
		return 0, fmt.Errorf("failed to list CSIStorageCapacity: %w", err)
	}

	var resultErr error
	deleted := 0
	for i := range list.Items {
		if err := cl.Delete(ctx, &list.Items[i]); err != nil {
			if kerrors.IsNotFound(err) {
				continue
			}
			resultErr = errors.Join(resultErr, fmt.Errorf("failed to delete CSIStorageCapacity %s: %w", list.Items[i].Name, err))
			continue
		}
		deleted++
	}
	return deleted, resultErr
}

func handlerRemoveFakeStorageCapacity(ctx context.Context, input *pkg.HookInput) error {
	cl, err := kubeclient.New(storagev1.AddToScheme)
	if err != nil {
		return fmt.Errorf("[%s]: failed to initialize kube client: %w", hookName, err)
	}

	deleted, err := removeStorageCapacities(ctx, cl)
	if deleted > 0 {
		input.Logger.Info(fmt.Sprintf("[%s]: removed %d CSIStorageCapacity objects", hookName, deleted))
	}
	if err != nil {
		return fmt.Errorf("[%s]: %w", hookName, err)
	}
	return nil
}
