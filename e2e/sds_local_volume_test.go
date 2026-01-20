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

package sds_local_volume

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "k8s.io/client-go/kubernetes"

	"github.com/deckhouse/sds-local-volume/e2e/helpers"
	"github.com/deckhouse/storage-e2e/pkg/cluster"
	"github.com/deckhouse/storage-e2e/pkg/kubernetes"
	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

var _ = Describe("Sds Local Volume", Ordered, func() {
	var (
		testClusterResources *cluster.TestClusterResources
	)

	BeforeAll(func() {
		By("Outputting environment variables", func() {
			cluster.OutputEnvironmentVariables()
		})
	})

	AfterAll(func() {
		cluster.CleanupTestClusterResources(testClusterResources)
	})

	// ---=== TEST CLUSTER IS CREATED OR CONNECTED HERE ===--- //

	It("should create or connect to test cluster and wait for it to become ready", func() {
		testClusterResources = cluster.CreateOrConnectToTestCluster()
	})

	////////////////////////////////////
	// ---=== TESTS START HERE ===--- //
	////////////////////////////////////

	// Storage class names for sds-local-volume with random suffix
	randomSuffix := cluster.GenerateRandomSuffix(6)
	storageClassNameThick := "lsc-thick-" + randomSuffix
	storageClassNameThin := "lsc-thin-" + randomSuffix

	// Thin pool name constant
	const thinPoolName = "thin-pool-test"

	It("should enable sds-local-volume module with dependencies", func() {
		ctx := context.Background()

		// Ensure testClusterResources is not nil (previous test must have set it)
		Expect(testClusterResources).NotTo(BeNil(), "testClusterResources must be set by previous test")

		By("Waiting for Deckhouse webhook to be ready", func() {
			GinkgoWriter.Printf("    ▶️ Waiting for Deckhouse webhook to be ready...\n")
			webhookTimeout := 5 * time.Minute
			err := cluster.WaitForWebhookHandler(
				ctx,
				testClusterResources.Kubeconfig,
				webhookTimeout,
			)
			Expect(err).NotTo(HaveOccurred(), "Deckhouse webhook is not ready")
			GinkgoWriter.Printf("    ✅ Deckhouse webhook is ready\n")
		})

		By("Enabling snapshot-controller, sds-node-configurator and sds-local-volume modules", func() {
			GinkgoWriter.Printf("    ▶️ Enabling modules: snapshot-controller, sds-node-configurator and sds-local-volume...\n")

			// Define modules to enable
			// sds-local-volume depends on sds-node-configurator
			// snapshot-controller is enabled before sds-node-configurator
			modules := []kubernetes.ModuleSpec{
				{
					Name:               "snapshot-controller",
					Version:            1,
					Enabled:            true,
					ModulePullOverride: "main",
				},
				{
					Name:               "sds-node-configurator",
					Version:            1,
					Enabled:            true,
					Dependencies:       []string{"snapshot-controller"},
					ModulePullOverride: "main",
				},
				{
					Name:               "sds-local-volume",
					Version:            1,
					Enabled:            true,
					Dependencies:       []string{"sds-node-configurator"},
					ModulePullOverride: "main",
				},
			}

			// Enable modules and wait for them to become ready
			err := kubernetes.EnableModulesAndWait(ctx, testClusterResources.Kubeconfig, testClusterResources.SSHClient, testClusterResources.ClusterDefinition, modules, 10*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "Failed to enable modules")

			GinkgoWriter.Printf("    ✅ Modules enabled successfully\n")
		})

		By("Waiting for all pods in module namespaces to be ready", func() {
			GinkgoWriter.Printf("    ▶️ Waiting for all pods to be ready in module namespaces...\n")

			namespacesToWait := []string{
				"d8-snapshot-controller",
				"d8-sds-node-configurator",
				"d8-sds-local-volume",
			}

			podReadyTimeout := 10 * time.Minute
			for _, ns := range namespacesToWait {
				GinkgoWriter.Printf("      ▶️ Waiting for pods in namespace %s...\n", ns)
				err := kubernetes.WaitForAllPodsReadyInNamespace(ctx, testClusterResources.Kubeconfig, ns, podReadyTimeout)
				Expect(err).NotTo(HaveOccurred(), "Failed waiting for pods in namespace %s to be ready", ns)
				GinkgoWriter.Printf("      ✅ All pods in namespace %s are ready\n", ns)
			}

			GinkgoWriter.Printf("    ✅ All pods in module namespaces are ready\n")
		})

		By("Waiting additional 30 seconds for stabilization", func() {
			GinkgoWriter.Printf("    ▶️ Waiting 30 seconds for stabilization...\n")
			time.Sleep(30 * time.Second)
			GinkgoWriter.Printf("    ✅ Stabilization wait completed\n")
		})
	})

	It("should create LVMVolumeGroups and LocalStorageClass", func() {
		ctx := context.Background()

		// Store LVMVolumeGroup names and node names for use across By blocks
		var lvmVolumeGroupNames []string
		var nodeNames []string

		By("Discovering available BlockDevices and creating LVMVolumeGroups", func() {
			GinkgoWriter.Printf("    ▶️ Discovering available BlockDevices...\n")

			// Get all consumable BlockDevices from the cluster
			blockDevices, err := kubernetes.GetConsumableBlockDevices(ctx, testClusterResources.Kubeconfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to get BlockDevices")
			Expect(len(blockDevices)).To(BeNumerically(">", 0), "No consumable BlockDevices found")

			GinkgoWriter.Printf("    ✅ Found %d consumable BlockDevices\n", len(blockDevices))

			// Group BlockDevices by node
			blockDevicesByNode := make(map[string][]string)
			for _, bd := range blockDevices {
				blockDevicesByNode[bd.NodeName] = append(blockDevicesByNode[bd.NodeName], bd.Name)
			}

			GinkgoWriter.Printf("    📋 BlockDevices by node:\n")
			for node, devices := range blockDevicesByNode {
				GinkgoWriter.Printf("      %s: %v\n", node, devices)
			}

			// Create LVMVolumeGroups for each node with thin pool
			GinkgoWriter.Printf("    ▶️ Creating LVMVolumeGroups with thin pool...\n")

			for nodeName, deviceNames := range blockDevicesByNode {
				lvgName := "vg-test-on-" + nodeName
				lvmVolumeGroupNames = append(lvmVolumeGroupNames, lvgName)
				nodeNames = append(nodeNames, nodeName)

				// Create LVMVolumeGroup with thin pool using 50% of the space
				thinPools := []kubernetes.ThinPoolSpec{
					{
						Name:            thinPoolName,
						Size:            "50%",
						AllocationLimit: "150%",
					},
				}

				err := kubernetes.CreateLVMVolumeGroupWithThinPool(
					ctx,
					testClusterResources.Kubeconfig,
					lvgName,
					nodeName,
					deviceNames,
					"vg-test", // actualVGNameOnTheNode
					thinPools,
				)
				Expect(err).NotTo(HaveOccurred(), "Failed to create LVMVolumeGroup for node %s", nodeName)
				GinkgoWriter.Printf("      ✅ Created LVMVolumeGroup %s for node %s with thin pool (%s)\n", lvgName, nodeName, thinPoolName)
			}

			// Wait for all LVMVolumeGroups to become Ready
			GinkgoWriter.Printf("    ▶️ Waiting for LVMVolumeGroups to become Ready...\n")
			for _, lvgName := range lvmVolumeGroupNames {
				err := kubernetes.WaitForLVMVolumeGroupReady(ctx, testClusterResources.Kubeconfig, lvgName, 5*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "LVMVolumeGroup %s did not become Ready", lvgName)
				GinkgoWriter.Printf("      ✅ LVMVolumeGroup %s is Ready\n", lvgName)
			}

			GinkgoWriter.Printf("    ✅ All LVMVolumeGroups with thin pools created successfully\n")
		})

		By("Creating LocalStorageClass for Thick LVM", func() {
			GinkgoWriter.Printf("    ▶️ Creating LocalStorageClass (Thick): %s...\n", storageClassNameThick)
			GinkgoWriter.Printf("      Using LVMVolumeGroups: %v\n", lvmVolumeGroupNames)

			// Create LocalStorageClass for Thick LVM using stored LVMVolumeGroup names
			err := helpers.CreateLocalStorageClass(
				ctx,
				testClusterResources.Kubeconfig,
				storageClassNameThick,
				lvmVolumeGroupNames,
				"Thick",                // LVM type
				"Delete",               // reclaimPolicy
				"WaitForFirstConsumer", // volumeBindingMode
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to create LocalStorageClass (Thick)")

			GinkgoWriter.Printf("    ✅ LocalStorageClass (Thick) created successfully: %s\n", storageClassNameThick)
		})

		By("Creating LocalStorageClass for Thin LVM", func() {
			GinkgoWriter.Printf("    ▶️ Creating LocalStorageClass (Thin): %s...\n", storageClassNameThin)

			// Build LVGConfig with thin pool for Thin LVM using stored LVMVolumeGroup names
			lvgConfigs := make([]helpers.LVGConfig, len(lvmVolumeGroupNames))
			for i, lvgName := range lvmVolumeGroupNames {
				lvgConfigs[i] = helpers.LVGConfig{
					Name:         lvgName,
					ThinPoolName: thinPoolName,
				}
			}
			GinkgoWriter.Printf("      Using LVMVolumeGroups with thin pools: %v\n", lvmVolumeGroupNames)

			// Create LocalStorageClass for Thin LVM with thin pool
			err := helpers.CreateLocalStorageClassWithThinPool(
				ctx,
				testClusterResources.Kubeconfig,
				storageClassNameThin,
				lvgConfigs,
				"Thin",                 // LVM type
				"Delete",               // reclaimPolicy
				"WaitForFirstConsumer", // volumeBindingMode
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to create LocalStorageClass (Thin)")

			GinkgoWriter.Printf("    ✅ LocalStorageClass (Thin) created successfully: %s\n", storageClassNameThin)
		})

		By("Waiting for StorageClasses to become available", func() {
			GinkgoWriter.Printf("    ▶️ Waiting for StorageClass %s...\n", storageClassNameThick)
			err := kubernetes.WaitForStorageClass(ctx, testClusterResources.Kubeconfig, storageClassNameThick, 10*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "StorageClass %s not available", storageClassNameThick)
			GinkgoWriter.Printf("    ✅ StorageClass %s is available\n", storageClassNameThick)

			GinkgoWriter.Printf("    ▶️ Waiting for StorageClass %s...\n", storageClassNameThin)
			err = kubernetes.WaitForStorageClass(ctx, testClusterResources.Kubeconfig, storageClassNameThin, 10*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "StorageClass %s not available", storageClassNameThin)
			GinkgoWriter.Printf("    ✅ StorageClass %s is available\n", storageClassNameThin)
		})
	})

	It("should run flog stress test with Thick storage class", func() {
		// Use a timeout context for the stress test (30 minutes should be enough)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		By("Running flog stress test with PVC resize (Thick)", func() {
			GinkgoWriter.Printf("    ▶️ Running flog stress test with Thick storage class: %s...\n", storageClassNameThick)

			// Configure stress test (values from environment variables with defaults)
			stressConfig := testkit.DefaultConfig()
			stressConfig.Namespace = "stress-test-flog-thick"
			stressConfig.StorageClassName = storageClassNameThick
			stressConfig.Mode = testkit.ModeFlog

			// Create and run stress test
			runner, err := testkit.NewStressTestRunner(stressConfig, testClusterResources.Kubeconfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to create stress test runner")

			err = runner.Run(ctx)
			Expect(err).NotTo(HaveOccurred(), "Stress test failed")

			GinkgoWriter.Printf("    ✅ Stress test (Thick) completed successfully\n")
		})
	})

	It("should run flog stress test with Thin storage class", func() {
		// Use a timeout context for the stress test (30 minutes should be enough)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		By("Running flog stress test with PVC resize (Thin)", func() {
			GinkgoWriter.Printf("    ▶️ Running flog stress test with Thin storage class: %s...\n", storageClassNameThin)

			// Configure stress test (values from environment variables with defaults)
			stressConfig := testkit.DefaultConfig()
			stressConfig.Namespace = "stress-test-flog-thin"
			stressConfig.StorageClassName = storageClassNameThin
			stressConfig.Mode = testkit.ModeFlog

			// Create and run stress test
			runner, err := testkit.NewStressTestRunner(stressConfig, testClusterResources.Kubeconfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to create stress test runner")

			err = runner.Run(ctx)
			Expect(err).NotTo(HaveOccurred(), "Stress test failed")

			GinkgoWriter.Printf("    ✅ Stress test (Thin) completed successfully\n")
		})
	})

	It("should run snapshot/resize/clone stress test with Thin storage class", func() {
		// Use a timeout context for the stress test (45 minutes for complex test)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()

		By("Running snapshot, resize, and clone stress test (Thin)", func() {
			GinkgoWriter.Printf("    ▶️ Running complex stress test with Thin storage class: %s...\n", storageClassNameThin)

			// Configure comprehensive stress test (values from environment variables with defaults)
			stressConfig := testkit.DefaultConfig()
			stressConfig.Namespace = "stress-test-complex-thin"
			stressConfig.StorageClassName = storageClassNameThin
			stressConfig.Mode = testkit.ModeSnapshotResizeCloning

			// Create and run stress test
			runner, err := testkit.NewStressTestRunner(stressConfig, testClusterResources.Kubeconfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to create stress test runner")

			err = runner.Run(ctx)
			Expect(err).NotTo(HaveOccurred(), "Complex stress test failed")

			GinkgoWriter.Printf("    ✅ Complex stress test (Thin) completed successfully\n")
		})
	})

	It("should cleanup LocalStorageClasses and LVMVolumeGroups", func() {
		ctx := context.Background()

		By("Deleting LocalStorageClasses", func() {
			GinkgoWriter.Printf("    ▶️ Deleting LocalStorageClass (Thick): %s...\n", storageClassNameThick)
			err := helpers.DeleteLocalStorageClass(ctx, testClusterResources.Kubeconfig, storageClassNameThick)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete LocalStorageClass (Thick)")
			GinkgoWriter.Printf("    ✅ LocalStorageClass (Thick) deletion initiated\n")

			GinkgoWriter.Printf("    ▶️ Deleting LocalStorageClass (Thin): %s...\n", storageClassNameThin)
			err = helpers.DeleteLocalStorageClass(ctx, testClusterResources.Kubeconfig, storageClassNameThin)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete LocalStorageClass (Thin)")
			GinkgoWriter.Printf("    ✅ LocalStorageClass (Thin) deletion initiated\n")
		})

		By("Waiting for LocalStorageClasses to be deleted", func() {
			GinkgoWriter.Printf("    ▶️ Waiting for LocalStorageClass (Thick) to be deleted: %s...\n", storageClassNameThick)
			err := helpers.WaitForLocalStorageClassDeletion(ctx, testClusterResources.Kubeconfig, storageClassNameThick, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "LocalStorageClass (Thick) was not deleted")
			GinkgoWriter.Printf("    ✅ LocalStorageClass (Thick) deleted: %s\n", storageClassNameThick)

			GinkgoWriter.Printf("    ▶️ Waiting for LocalStorageClass (Thin) to be deleted: %s...\n", storageClassNameThin)
			err = helpers.WaitForLocalStorageClassDeletion(ctx, testClusterResources.Kubeconfig, storageClassNameThin, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "LocalStorageClass (Thin) was not deleted")
			GinkgoWriter.Printf("    ✅ LocalStorageClass (Thin) deleted: %s\n", storageClassNameThin)
		})

		By("Waiting for StorageClasses to be deleted", func() {
			GinkgoWriter.Printf("    ▶️ Waiting for StorageClass (Thick) to be deleted: %s...\n", storageClassNameThick)
			err := kubernetes.WaitForStorageClassDeletion(ctx, testClusterResources.Kubeconfig, storageClassNameThick, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "StorageClass (Thick) was not deleted")
			GinkgoWriter.Printf("    ✅ StorageClass (Thick) deleted: %s\n", storageClassNameThick)

			GinkgoWriter.Printf("    ▶️ Waiting for StorageClass (Thin) to be deleted: %s...\n", storageClassNameThin)
			err = kubernetes.WaitForStorageClassDeletion(ctx, testClusterResources.Kubeconfig, storageClassNameThin, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "StorageClass (Thin) was not deleted")
			GinkgoWriter.Printf("    ✅ StorageClass (Thin) deleted: %s\n", storageClassNameThin)
		})

		By("Deleting LVMVolumeGroups", func() {
			GinkgoWriter.Printf("    ▶️ Deleting LVMVolumeGroups...\n")

			// Get all nodes from the cluster to construct LVMVolumeGroup names
			clientset, err := k8sclient.NewForConfig(testClusterResources.Kubeconfig)
			Expect(err).NotTo(HaveOccurred(), "Failed to create clientset")

			nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			Expect(err).NotTo(HaveOccurred(), "Failed to list nodes")

			// Delete LVMVolumeGroups for each node
			lvmVolumeGroupNames := []string{}
			for _, node := range nodes.Items {
				lvgName := "vg-test-on-" + node.Name
				lvmVolumeGroupNames = append(lvmVolumeGroupNames, lvgName)

				GinkgoWriter.Printf("      ▶️ Deleting LVMVolumeGroup: %s...\n", lvgName)
				err := kubernetes.DeleteLVMVolumeGroup(ctx, testClusterResources.Kubeconfig, lvgName)
				if err != nil {
					GinkgoWriter.Printf("      ⚠️  Warning: Failed to delete LVMVolumeGroup %s: %v\n", lvgName, err)
				} else {
					GinkgoWriter.Printf("      ✅ LVMVolumeGroup %s deletion initiated\n", lvgName)
				}
			}

			// Wait for all LVMVolumeGroups to be deleted
			GinkgoWriter.Printf("    ▶️ Waiting for LVMVolumeGroups to be deleted...\n")
			for _, lvgName := range lvmVolumeGroupNames {
				err := kubernetes.WaitForLVMVolumeGroupDeletion(ctx, testClusterResources.Kubeconfig, lvgName, 5*time.Minute)
				if err != nil {
					GinkgoWriter.Printf("      ⚠️  Warning: LVMVolumeGroup %s deletion timeout: %v\n", lvgName, err)
				} else {
					GinkgoWriter.Printf("      ✅ LVMVolumeGroup %s deleted\n", lvgName)
				}
			}

			GinkgoWriter.Printf("    ✅ All LVMVolumeGroups deleted successfully\n")
		})
	})

	///////////////////////////////////////////////////// ---=== TESTS END HERE ===--- /////////////////////////////////////////////////////

}) // Describe: Sds Local Volume
