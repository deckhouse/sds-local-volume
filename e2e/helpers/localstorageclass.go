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

package helpers

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// localStorageClassGVR is the GroupVersionResource for LocalStorageClass
var localStorageClassGVR = schema.GroupVersionResource{
	Group:    "storage.deckhouse.io",
	Version:  "v1alpha1",
	Resource: "localstorageclasses",
}

// LocalStorageClassClient provides operations on LocalStorageClass resources
type LocalStorageClassClient struct {
	client dynamic.Interface
}

// NewLocalStorageClassClient creates a new LocalStorageClass client from a rest.Config
func NewLocalStorageClassClient(config *rest.Config) (*LocalStorageClassClient, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	return &LocalStorageClassClient{client: dynamicClient}, nil
}

// LVGConfig represents configuration for an LVMVolumeGroup in LocalStorageClass
type LVGConfig struct {
	Name         string // LVMVolumeGroup name
	ThinPoolName string // Optional: thin pool name for Thin LVM type
}

// Create creates a new LocalStorageClass resource
func (c *LocalStorageClassClient) Create(ctx context.Context, name string, lvmVolumeGroupNames []string, lvmType, reclaimPolicy, volumeBindingMode string) error {
	// Build lvmVolumeGroups list
	lvmVolumeGroups := make([]interface{}, len(lvmVolumeGroupNames))
	for i, lvgName := range lvmVolumeGroupNames {
		lvmVolumeGroups[i] = map[string]interface{}{
			"name": lvgName,
		}
	}

	lsc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "storage.deckhouse.io/v1alpha1",
			"kind":       "LocalStorageClass",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"lvm": map[string]interface{}{
					"lvmVolumeGroups": lvmVolumeGroups,
					"type":            lvmType,
				},
				"reclaimPolicy":     reclaimPolicy,
				"volumeBindingMode": volumeBindingMode,
			},
		},
	}

	_, err := c.client.Resource(localStorageClassGVR).Create(ctx, lsc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create LocalStorageClass %s: %w", name, err)
	}

	return nil
}

// CreateWithThinPool creates a new LocalStorageClass resource with thin pool configuration
func (c *LocalStorageClassClient) CreateWithThinPool(ctx context.Context, name string, lvgConfigs []LVGConfig, lvmType, reclaimPolicy, volumeBindingMode string) error {
	// Build lvmVolumeGroups list
	lvmVolumeGroups := make([]interface{}, len(lvgConfigs))
	for i, lvgConfig := range lvgConfigs {
		lvgEntry := map[string]interface{}{
			"name": lvgConfig.Name,
		}
		// Add thin pool configuration if specified
		if lvgConfig.ThinPoolName != "" {
			lvgEntry["thin"] = map[string]interface{}{
				"poolName": lvgConfig.ThinPoolName,
			}
		}
		lvmVolumeGroups[i] = lvgEntry
	}

	lsc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "storage.deckhouse.io/v1alpha1",
			"kind":       "LocalStorageClass",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"lvm": map[string]interface{}{
					"lvmVolumeGroups": lvmVolumeGroups,
					"type":            lvmType,
				},
				"reclaimPolicy":     reclaimPolicy,
				"volumeBindingMode": volumeBindingMode,
			},
		},
	}

	_, err := c.client.Resource(localStorageClassGVR).Create(ctx, lsc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create LocalStorageClass %s: %w", name, err)
	}

	return nil
}

// Get retrieves a LocalStorageClass by name
func (c *LocalStorageClassClient) Get(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	lsc, err := c.client.Resource(localStorageClassGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get LocalStorageClass %s: %w", name, err)
	}
	return lsc, nil
}

// Delete deletes a LocalStorageClass by name
func (c *LocalStorageClassClient) Delete(ctx context.Context, name string) error {
	err := c.client.Resource(localStorageClassGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete LocalStorageClass %s: %w", name, err)
	}
	return nil
}

// WaitForCreated waits for a LocalStorageClass to reach Created phase
func (c *LocalStorageClassClient) WaitForCreated(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for LocalStorageClass %s to become Created", name)
		}

		lsc, err := c.Get(ctx, name)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		phase, found, _ := unstructured.NestedString(lsc.Object, "status", "phase")
		if found && phase == "Created" {
			return nil
		}

		time.Sleep(2 * time.Second)
	}
}

// WaitForDeletion waits for a LocalStorageClass to be deleted
func (c *LocalStorageClassClient) WaitForDeletion(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for LocalStorageClass %s to be deleted", name)
		}

		_, err := c.Get(ctx, name)
		if err != nil {
			// Assume deleted if we can't get it
			return nil
		}

		time.Sleep(2 * time.Second)
	}
}

// GetPhase returns the current phase of a LocalStorageClass
func (c *LocalStorageClassClient) GetPhase(ctx context.Context, name string) (string, error) {
	lsc, err := c.Get(ctx, name)
	if err != nil {
		return "", err
	}

	phase, _, _ := unstructured.NestedString(lsc.Object, "status", "phase")
	return phase, nil
}

// CreateLocalStorageClass is a convenience function to create a LocalStorageClass
func CreateLocalStorageClass(ctx context.Context, kubeconfig *rest.Config, name string, lvmVolumeGroupNames []string, lvmType, reclaimPolicy, volumeBindingMode string) error {
	client, err := NewLocalStorageClassClient(kubeconfig)
	if err != nil {
		return err
	}
	return client.Create(ctx, name, lvmVolumeGroupNames, lvmType, reclaimPolicy, volumeBindingMode)
}

// CreateLocalStorageClassWithThinPool is a convenience function to create a LocalStorageClass with thin pool configuration
func CreateLocalStorageClassWithThinPool(ctx context.Context, kubeconfig *rest.Config, name string, lvgConfigs []LVGConfig, lvmType, reclaimPolicy, volumeBindingMode string) error {
	client, err := NewLocalStorageClassClient(kubeconfig)
	if err != nil {
		return err
	}
	return client.CreateWithThinPool(ctx, name, lvgConfigs, lvmType, reclaimPolicy, volumeBindingMode)
}

// WaitForLocalStorageClassCreated is a convenience function to wait for a LocalStorageClass to be created
func WaitForLocalStorageClassCreated(ctx context.Context, kubeconfig *rest.Config, name string, timeout time.Duration) error {
	client, err := NewLocalStorageClassClient(kubeconfig)
	if err != nil {
		return err
	}
	return client.WaitForCreated(ctx, name, timeout)
}

// DeleteLocalStorageClass is a convenience function to delete a LocalStorageClass
func DeleteLocalStorageClass(ctx context.Context, kubeconfig *rest.Config, name string) error {
	client, err := NewLocalStorageClassClient(kubeconfig)
	if err != nil {
		return err
	}
	return client.Delete(ctx, name)
}

// WaitForLocalStorageClassDeletion is a convenience function to wait for a LocalStorageClass to be deleted
func WaitForLocalStorageClassDeletion(ctx context.Context, kubeconfig *rest.Config, name string, timeout time.Duration) error {
	client, err := NewLocalStorageClassClient(kubeconfig)
	if err != nil {
		return err
	}
	return client.WaitForDeletion(ctx, name, timeout)
}
