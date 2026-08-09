//go:build integration

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

// Package integration holds envtest-based integration tests for the
// sds-local-volume controller. A real kube-apiserver + etcd is started via
// controller-runtime's envtest, the LocalStorageClass controller (including its
// LVMVolumeGroup watch) is wired onto a manager, and the behaviour is asserted
// end-to-end against the API server.
//
// Run with:
//
//	KUBEBUILDER_ASSETS=$(setup-envtest use -p path) \
//	  go test -tags integration ./test/integration/... -count=1
package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/config"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const controllerNamespace = "d8-sds-local-volume"

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	scheme    *apiruntime.Scheme
	k8sClient client.Client
	// mgrCache is the manager's cache, so that a spec can assert on what the
	// controller actually reads rather than only on what the API server holds.
	mgrCache cache.Cache

	suiteCtx    context.Context
	suiteCancel context.CancelFunc
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "sds-local-volume controller integration suite")
}

var _ = BeforeSuite(func() {
	// Route controller-runtime's own logging through the module logger too, so the
	// suite exercises the same stack the controller uses in production.
	suiteLog, err := logger.NewLoggerToWriter(GinkgoWriter, logger.TraceLevel)
	Expect(err).NotTo(HaveOccurred())
	ctrl.SetLogger(suiteLog.GetLogger())

	suiteCtx, suiteCancel = context.WithCancel(context.Background())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			// The module's own CRDs (LocalStorageClass).
			filepath.Join("..", "..", "..", "..", "crds"),
			// The LVMVolumeGroup, LVMLogicalVolume and LVMLogicalVolumeSnapshot CRDs
			// from sds-node-configurator, vendored as fixtures so the suite does not
			// depend on a sibling checkout.
			filepath.Join("crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	scheme = apiruntime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(slv.AddToScheme(scheme)).To(Succeed())
	Expect(snc.AddToScheme(scheme)).To(Succeed())

	// Uncached client for the test code itself (fresh reads, direct writes).
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Run the real controller on a manager so its LocalStorageClass reconcile
	// and LVMVolumeGroup watch drive the behaviour under test.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
		// Mirrors cmd/main.go. The wiring is the part that is easy to get wrong and
		// that no unit test can reach: the namespace restriction must not keep the
		// cluster-scoped PersistentVolume cache from being cluster-wide, and the
		// transform has to survive the ByObject defaulting. Without this the suite
		// would exercise a cache the production binary does not have, and a
		// controller-runtime bump that changed either could leave every test green
		// while referencingPersistentVolumes silently returned an empty map.
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{controllerNamespace: {}},
			ByObject: map[client.Object]cache.ByObject{
				&corev1.PersistentVolume{}: {Transform: controller.StripPersistentVolume},
			},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	mgrCache = mgr.GetCache()

	cfgParams := config.Options{
		ControllerNamespace:         controllerNamespace,
		ConfigSecretName:            config.ConfigSecretName,
		RequeueStorageClassInterval: 1,
		// Short enough that a spec can wait out the grace period, long enough that a
		// spec asserting the opposite — that a volume is left alone — is not racing
		// the collector.
		LLVOrphanGracePeriod: 2 * time.Second,
		LLVSweepInterval:     time.Second,
	}
	// The zero Recorder discards every observation, so the suite does not need a
	// metrics registry to exercise the reconcile behaviour.
	_, err = controller.RunLocalStorageClassWatcherController(mgr, cfgParams, suiteLog, monitoring.Recorder{})
	Expect(err).NotTo(HaveOccurred())

	// The garbage collector runs on the same manager, so the specs exercise it
	// against a real API server: envtest enforces finalizer semantics, which a fake
	// client only approximates.
	_, err = controller.RunLVMLogicalVolumeGCController(mgr, cfgParams, suiteLog, monitoring.Recorder{})
	Expect(err).NotTo(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(suiteCtx)).To(Succeed())
	}()

	Expect(mgr.GetCache().WaitForCacheSync(suiteCtx)).To(BeTrue())
})

var _ = AfterSuite(func() {
	if suiteCancel != nil {
		suiteCancel()
	}
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
})

// eventuallyTimeout / interval used by the specs.
const (
	eventuallyTimeout  = 20 * time.Second
	eventuallyInterval = 250 * time.Millisecond
)
