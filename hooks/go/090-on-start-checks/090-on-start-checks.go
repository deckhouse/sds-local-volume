/*
Copyright 2024 Flant JSC

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

package onstartchecks

import (
	"context"
	"encoding/json"

	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = registry.RegisterFunc(
	&pkg.HookConfig{
		OnAfterHelm: &pkg.OrderedConfig{Order: 10},
	},
	onStartChecks,
)

func onStartChecks(ctx context.Context, input *pkg.HookInput) error {
	cl := input.DC.MustGetK8sClient()

	lscList := &unstructured.UnstructuredList{}
	lscList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "storage.deckhouse.io",
		Version: "v1alpha1",
		Kind:    "LocalStorageClassList",
	})

	if err := cl.List(ctx, lscList); err != nil {
		input.Logger.Info("Failed to list LocalStorageClassList", "err", err)
		return nil
	}

	thinPoolExistence := false
	for _, item := range lscList.Items {
		if lvmType, found, _ := unstructured.NestedString(item.Object, "spec", "lvm", "type"); found && lvmType == "Thin" {
			thinPoolExistence = true
			break
		}
	}

	if thinPoolExistence {
		// Try to get existing ModuleConfig
		modCfg := &unstructured.Unstructured{}
		modCfg.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "deckhouse.io",
			Version: "v1alpha1",
			Kind:    "ModuleConfig",
		})
		modCfg.SetName("sds-local-volume")

		err := cl.Get(ctx, client.ObjectKey{Name: "sds-local-volume"}, modCfg)
		if err != nil {

			if client.IgnoreNotFound(err) == nil {
				input.Logger.Info("ModuleConfig not found, creating new one")
			} else {
				input.Logger.Error("Failed to get ModuleConfig", "err", err)
				return err
			}

			// Create new ModuleConfig
			newModCfg := &unstructured.Unstructured{}
			newModCfg.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "deckhouse.io",
				Version: "v1alpha1",
				Kind:    "ModuleConfig",
			})
			newModCfg.SetName("sds-local-volume")
			newModCfg.SetUnstructuredContent(map[string]interface{}{
				"spec": map[string]interface{}{
					"settings": map[string]interface{}{
						"enableThinProvisioning": true,
					},
				},
			})

			if err := cl.Create(ctx, newModCfg); err != nil {
				input.Logger.Error("Failed to create moduleconfig", "err", err)
			} else {
				input.Logger.Info("Created moduleconfig with thin provisioning enabled")
			}
		} else {
			// Update existing ModuleConfig using patch
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"settings": map[string]interface{}{
						"enableThinProvisioning": true,
					},
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				input.Logger.Error("Failed to marshal patch for moduleconfig", "err", err)
			} else {
				if err := cl.Patch(ctx, modCfg, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
					input.Logger.Error("Failed to patch moduleconfig", "err", err)
				} else {
					input.Logger.Info("Patched moduleconfig with thin provisioning enabled")
				}
			}
		}

		return nil

	} else {
		input.Logger.Info("No thin pool granularity found, checking if thin provisioning should be disabled")

		// Check existing ModuleConfig for enableThinProvisioning setting
		modCfg := &unstructured.Unstructured{}
		modCfg.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "deckhouse.io",
			Version: "v1alpha1",
			Kind:    "ModuleConfig",
		})
		modCfg.SetName("sds-local-volume")

		err := cl.Get(ctx, client.ObjectKey{Name: "sds-local-volume"}, modCfg)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				input.Logger.Info("ModuleConfig not found, nothing to disable")
			} else {
				input.Logger.Error("Failed to get ModuleConfig", "err", err)
				return err
			}
		} else {
			// Check if enableThinProvisioning is currently true
			enableThinProvisioning, found, _ := unstructured.NestedBool(modCfg.Object, "spec", "settings", "enableThinProvisioning")

			if found && enableThinProvisioning {

				// Disable thin provisioning

				input.Logger.Info("Thin provisioning in moduleconfig set to True - disabling")

				patch := map[string]interface{}{
					"spec": map[string]interface{}{
						"settings": map[string]interface{}{
							"enableThinProvisioning": false,
						},
					},
				}

				patchBytes, err := json.Marshal(patch)
				if err != nil {
					input.Logger.Info("Failed to marshal patch for moduleconfig", "err", err)
				} else {
					if err := cl.Patch(ctx, modCfg, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
						input.Logger.Info("Failed to patch moduleconfig", "err", err)
					} else {
						input.Logger.Info("Patched moduleconfig with thin provisioning disabled")
					}
				}
			} else {
				input.Logger.Info("Thin provisioning already disabled or not set")
			}
		}
	}

	return nil
}
