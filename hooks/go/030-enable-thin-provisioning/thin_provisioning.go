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
	configVersion = "v1"
	afterHelm     = 10
)

func init() {
	// Load in-cluster Kubernetes configuration
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig for local testing
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load Kubernetes config: %v\n", err)
			os.Exit(1)
		}
	}

	// Create dynamic client for interacting with custom resources
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create dynamic client: %v\n", err)
		os.Exit(1)
	}

	// Define the custom resource GVR (Group, Version, Resource)
	localStorageClassGVR := schema.GroupVersionResource{
		Group:    crdGroup,
		Version:  crdVersion,
		Resource: crdNamePlural,
	}

	// List localstorageclasses custom resources
	list, err := dynamicClient.Resource(localStorageClassGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list custom objects: %v\n", err)
		os.Exit(1)
	}

	thinPoolExistence := false
	for _, item := range list.Items {
		// Extract spec.lvm.type
		lvmType, found, err := unstructured.NestedString(item.Object, "spec", "lvm", "type")
		if err != nil || !found {
			continue
		}
		if lvmType == "Thin" {
			thinPoolExistence = true
			break
		}
	}

	// If thinPoolExistence is true, apply YAML configuration to moduleconfigs
	if thinPoolExistence {
		resp, err := dynamicClient.Resource(schema.GroupVersionResource{
			Group:    "deckhouse.io",
			Version:  "v1alpha1",
			Resource: "moduleconfigs",
		}).Apply(context.Background(), "sds-local-volume", &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "deckhouse.io/v1alpha1",
				"kind":       "ModuleConfig",
				"metadata":   map[string]interface{}{"name": "sds-local-volume"},
				"spec":       map[string]interface{}{"version": 1, "settings": map[string]interface{}{"enableThinProvisioning": true}},
			},
		}, metav1.ApplyOptions{FieldManager: "sds-hook"})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to apply YAML configuration: %v\n", err)
			os.Exit(1)
		}
		yamlResp, err := yaml.Marshal(resp.Object)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to marshal response to YAML: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Thin pools present, switching enableThinProvisioning on\n---\n%s\n", yamlResp)
	}
}
