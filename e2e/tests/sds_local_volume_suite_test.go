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

package tests

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/storage-e2e/pkg/cluster"
	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

const (
	moduleName = "sds-local-volume"

	// nodeLabelKey marks worker nodes the sds-node-configurator agent must run
	// on (so it discovers block devices) and where local volumes are placed.
	nodeLabelKey   = "storage.deckhouse.io/sds-local-volume-node"
	nodeLabelValue = ""

	// poolLabelKey is set on every LVMVolumeGroup the suite creates so the
	// matchLabels LocalStorageClass spec can select them all.
	poolLabelKey   = "sds-local-volume-e2e.storage.deckhouse.io/pool"
	poolLabelValue = "e2e"

	// tierLabelKey differentiates the created LVMVolumeGroups so the
	// matchExpressions inclusion/exclusion specs can select a subset: the first
	// LVMVolumeGroup gets tierFast, the rest tierSlow.
	tierLabelKey = "sds-local-volume-e2e.storage.deckhouse.io/tier"
	tierFast     = "fast"
	tierSlow     = "slow"

	actualVGName = "sds-local-volume-e2e"

	probeContainerName = "probe"
	probeImage         = "busybox:1.36"
)

const (
	pollInterval        = 5 * time.Second
	bdDiscoveryTimeout  = 5 * time.Minute
	lvgReadyTimeout     = 5 * time.Minute
	lscCreatedTimeout   = 2 * time.Minute
	pvcBindTimeout      = 5 * time.Minute
	podRunningTimeout   = 5 * time.Minute
	defaultModuleReady  = 15 * time.Minute
	defaultDiskSize     = "20Gi"
	defaultDisksPerNode = 1
)

type e2eConfig struct {
	namespace        string
	pvcSize          string
	moduleReadyTO    time.Duration
	diskSize         string
	disksPerWorker   int
	vmNamespace      string
	baseStorageClass string
}

var (
	suiteCfg              e2eConfig
	suiteRestCfg          *rest.Config
	suiteK8s              client.Client
	suiteDyn              dynamic.Interface
	suiteClusterResources *cluster.TestClusterResources

	// suiteLVGs is the set of LVMVolumeGroups the suite created (one per worker
	// with a consumable block device), all labelled with poolLabelKey.
	suiteLVGs []string
)

func TestSdsLocalVolume(t *testing.T) {
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	if os.Getenv("CI") != "" {
		suiteConfig.FailFast = true
		suiteConfig.Timeout = 60 * time.Minute
	}
	suiteConfig.RandomizeAllSpecs = false
	reporterConfig.Verbose = true

	RunSpecs(t, "sds-local-volume E2E Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteCfg = loadConfig()

	GinkgoWriter.Printf("E2E config:\n")
	GinkgoWriter.Printf("  TEST_CLUSTER_CREATE_MODE: %q\n", os.Getenv("TEST_CLUSTER_CREATE_MODE"))
	GinkgoWriter.Printf("  namespace:                %q\n", suiteCfg.namespace)
	GinkgoWriter.Printf("  module ready timeout:     %s\n", suiteCfg.moduleReadyTO)

	ensureNestedTestCluster()

	var err error
	suiteRestCfg = suiteClusterResources.Kubeconfig
	suiteK8s, err = client.New(suiteRestCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred(), "build controller-runtime client")
	suiteDyn, err = dynamic.NewForConfig(suiteRestCfg)
	Expect(err).NotTo(HaveOccurred(), "build dynamic client")

	ctx, cancel := context.WithTimeout(context.Background(), suiteCfg.moduleReadyTO+30*time.Minute)
	defer cancel()

	By("Waiting for the sds-local-volume module to become Ready")
	Expect(storagekube.WaitForModuleReady(ctx, suiteRestCfg, moduleName, suiteCfg.moduleReadyTO)).
		To(Succeed(), "sds-local-volume module readiness")

	By("Ensuring the test namespace exists")
	_, err = storagekube.CreateNamespaceIfNotExists(ctx, suiteRestCfg, suiteCfg.namespace)
	Expect(err).NotTo(HaveOccurred(), "create test namespace")

	prepareStorage(ctx)
})

var _ = AfterSuite(func() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	for _, name := range suiteLVGs {
		if err := storagekube.DeleteLVMVolumeGroup(ctx, suiteRestCfg, name); err != nil {
			GinkgoWriter.Printf("  warning: LVMVolumeGroup %s cleanup failed: %v\n", name, err)
		}
	}
	cleanupNestedTestCluster()
})

// prepareStorage labels the worker nodes, ensures each has a consumable block
// device (attaching one at runtime when a base cluster is available), and turns
// the first consumable device on each worker into a labelled LVMVolumeGroup.
func prepareStorage(ctx context.Context) {
	By("Labelling worker nodes for sds-local-volume")
	workers, err := storagekube.GetWorkerNodes(ctx, suiteRestCfg)
	Expect(err).NotTo(HaveOccurred(), "list worker nodes")
	Expect(workers).NotTo(BeEmpty(), "at least one worker node is required")

	names := make([]string, 0, len(workers))
	for i := range workers {
		names = append(names, workers[i].Name)
	}
	Expect(storagekube.LabelNodes(ctx, suiteRestCfg, names, nodeLabelKey, nodeLabelValue)).
		To(Succeed(), "label worker nodes")

	attachRawDisks(ctx, names)

	By("Waiting for consumable BlockDevices to appear")
	var bds []storagekube.BlockDevice
	Eventually(func() (int, error) {
		var e error
		bds, e = storagekube.GetConsumableBlockDevices(ctx, suiteRestCfg)
		return len(bds), e
	}).WithTimeout(bdDiscoveryTimeout).WithPolling(pollInterval).
		Should(BeNumerically(">=", 1), "at least one consumable BlockDevice is required")

	By("Creating a labelled LVMVolumeGroup per worker with a consumable BlockDevice")
	seenNodes := map[string]struct{}{}
	for _, bd := range bds {
		if bd.NodeName == "" {
			continue
		}
		if _, done := seenNodes[bd.NodeName]; done {
			continue // one LVMVolumeGroup per node (avoids same-node conflicts)
		}
		seenNodes[bd.NodeName] = struct{}{}

		lvgName := "e2e-lvg-" + bd.NodeName
		Expect(storagekube.CreateLVMVolumeGroup(ctx, suiteRestCfg, lvgName, bd.NodeName, []string{bd.Name}, actualVGName)).
			To(Succeed(), "create LVMVolumeGroup %s", lvgName)
		Expect(labelLVG(ctx, lvgName, poolLabelKey, poolLabelValue)).
			To(Succeed(), "label LVMVolumeGroup %s", lvgName)
		Expect(storagekube.WaitForLVMVolumeGroupReady(ctx, suiteRestCfg, lvgName, lvgReadyTimeout)).
			To(Succeed(), "LVMVolumeGroup %s readiness", lvgName)
		suiteLVGs = append(suiteLVGs, lvgName)
	}
	Expect(suiteLVGs).NotTo(BeEmpty(), "no LVMVolumeGroups could be created")

	// Deterministic order so the specs can reason about the expected match set;
	// tag the first LVMVolumeGroup as "fast" and the rest as "slow" for the
	// matchExpressions inclusion/exclusion specs.
	sort.Strings(suiteLVGs)
	for i, name := range suiteLVGs {
		tier := tierSlow
		if i == 0 {
			tier = tierFast
		}
		Expect(labelLVG(ctx, name, tierLabelKey, tier)).To(Succeed(), "tier-label LVMVolumeGroup %s", name)
	}
}

// attachRawDisks attaches suiteCfg.disksPerWorker raw VirtualDisks to every
// worker VM on the base cluster so sds-node-configurator surfaces them as
// consumable BlockDevices. No-op when BaseKubeconfig is nil (Commander template
// / existing cluster where disks are pre-provisioned).
func attachRawDisks(ctx context.Context, workers []string) {
	if suiteClusterResources.BaseKubeconfig == nil {
		GinkgoWriter.Printf("  BaseKubeconfig is nil; skipping VirtualDisk attach (disks assumed pre-provisioned)\n")
		return
	}
	Expect(suiteCfg.baseStorageClass).NotTo(BeEmpty(), "TEST_CLUSTER_STORAGE_CLASS must be set to attach raw disks")

	for _, w := range workers {
		for d := 0; d < suiteCfg.disksPerWorker; d++ {
			diskName := fmt.Sprintf("%s-slv-%d", w, d)
			res, err := storagekube.AttachVirtualDiskToVM(ctx, suiteClusterResources.BaseKubeconfig, storagekube.VirtualDiskAttachmentConfig{
				VMName:           w,
				Namespace:        suiteCfg.vmNamespace,
				DiskName:         diskName,
				DiskSize:         suiteCfg.diskSize,
				StorageClassName: suiteCfg.baseStorageClass,
			})
			Expect(err).NotTo(HaveOccurred(), "attach disk %s to %s", diskName, w)
			Expect(storagekube.WaitForVirtualDiskAttached(ctx, suiteClusterResources.BaseKubeconfig, suiteCfg.vmNamespace, res.AttachmentName, 30*time.Second)).
				To(Succeed(), "wait disk %s attach on %s", diskName, w)
		}
	}
}

// lvmVolumeGroupGVR is the sds-node-configurator LVMVolumeGroup resource.
var lvmVolumeGroupGVR = storagekube.LocalStorageClassGVR.GroupVersion().WithResource("lvmvolumegroups")

// labelLVG sets a label on an LVMVolumeGroup CR via a JSON merge patch. A patch
// (rather than get+update) avoids optimistic-concurrency conflicts with the
// sds-node-configurator controller, which reconciles the object concurrently.
func labelLVG(ctx context.Context, name, key, value string) error {
	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value))
	_, err := suiteDyn.Resource(lvmVolumeGroupGVR).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func ensureNestedTestCluster() {
	if strings.TrimSpace(os.Getenv("TEST_CLUSTER_CREATE_MODE")) == "" {
		Fail("TEST_CLUSTER_CREATE_MODE must be set: this suite only supports storage-e2e nested clusters")
	}
	if suiteClusterResources != nil {
		return
	}
	suiteClusterResources = cluster.CreateOrConnectToTestCluster()
	if suiteClusterResources == nil || suiteClusterResources.Kubeconfig == nil {
		Fail("storage-e2e returned a nil cluster handle")
	}
}

func cleanupNestedTestCluster() {
	if suiteClusterResources == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := cluster.CleanupTestCluster(ctx, suiteClusterResources); err != nil {
		GinkgoWriter.Printf("  warning: nested cluster cleanup failed: %v\n", err)
	}
	suiteClusterResources = nil
}

func loadConfig() e2eConfig {
	cfg := e2eConfig{
		namespace:        strings.TrimSpace(os.Getenv("TEST_CLUSTER_NAMESPACE")),
		pvcSize:          strings.TrimSpace(os.Getenv("E2E_PVC_SIZE")),
		diskSize:         strings.TrimSpace(os.Getenv("E2E_DISK_SIZE")),
		vmNamespace:      strings.TrimSpace(os.Getenv("TEST_CLUSTER_NAMESPACE")),
		baseStorageClass: strings.TrimSpace(os.Getenv("TEST_CLUSTER_STORAGE_CLASS")),
	}
	if cfg.namespace == "" {
		cfg.namespace = "e2e-sds-local-volume"
		cfg.vmNamespace = cfg.namespace
	}
	if cfg.pvcSize == "" {
		cfg.pvcSize = "1Gi"
	}
	if cfg.diskSize == "" {
		cfg.diskSize = defaultDiskSize
	}
	cfg.disksPerWorker = defaultDisksPerNode
	cfg.moduleReadyTO = defaultModuleReady
	if raw := strings.TrimSpace(os.Getenv("E2E_MODULE_READY_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.moduleReadyTO = d
		}
	}
	return cfg
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
