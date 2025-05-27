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

package thinprovisioning

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	crdGroup      = "storage.deckhouse.io"
	crdNamePlural = "localstorageclasses"
	crdVersion    = "v1alpha1"
)

func init() {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			_, err := fmt.Fprintf(os.Stderr, "Failed to load Kubernetes config: %v\n", err)
			if err != nil {
				return
			}
			os.Exit(1)
		}
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "Failed to create dynamic client: %v\n", err)
		if err != nil {
			return
		}
		os.Exit(1)
	}

	localStorageClassGVR := schema.GroupVersionResource{
		Group:    crdGroup,
		Version:  crdVersion,
		Resource: crdNamePlural,
	}

	list, err := dynamicClient.Resource(localStorageClassGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "Failed to list custom objects: %v\n", err)
		if err != nil {
			return
		}
		os.Exit(1)
	}

	thinPoolExistence := false
	for _, item := range list.Items {
		lvmType, found, err := unstructured.NestedString(item.Object, "spec", "lvm", "type")
		if err != nil || !found {
			continue
		}
		if lvmType == "Thin" {
			thinPoolExistence = true
			break
		}
	}

	if thinPoolExistence {
		moduleConfigGVR := schema.GroupVersionResource{
			Group:    "deckhouse.io",
			Version:  "v1alpha1",
			Resource: "moduleconfigs",
		}

		applyConfig := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "deckhouse.io/v1alpha1",
				"kind":       "ModuleConfig",
				"metadata": map[string]interface{}{
					"name": "sds-local-volume",
				},
				"spec": map[string]interface{}{
					"version": 1,
					"settings": map[string]interface{}{
						"enableThinProvisioning": true,
					},
				},
			},
		}

		apiResponse, err := dynamicClient.Resource(moduleConfigGVR).Apply(
			context.Background(),
			"sds-local-volume",
			applyConfig,
			metav1.ApplyOptions{
				FieldManager: "sds-hook",
			},
		)

		if err != nil {
			_, err := fmt.Fprintf(os.Stderr, "Failed to patch moduleconfig: %v\n", err)
			if err != nil {
				return
			}
			os.Exit(1)
		}

		_, err = yaml.Marshal(apiResponse.Object)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to marshal API response to YAML: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Thin pools present, switching enableThinProvisioning on")
	}
}
