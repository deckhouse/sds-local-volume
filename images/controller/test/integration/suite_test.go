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
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/config"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const controllerNamespace = "d8-sds-local-volume"

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	scheme    *apiruntime.Scheme
	k8sClient client.Client

	suiteCtx    context.Context
	suiteCancel context.CancelFunc
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "sds-local-volume controller integration suite")
}

var _ = BeforeSuite(func() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(GinkgoWriter)))
	suiteCtx, suiteCancel = context.WithCancel(context.Background())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			// The module's own CRDs (LocalStorageClass).
			filepath.Join("..", "..", "..", "..", "crds"),
			// The LVMVolumeGroup CRD from sds-node-configurator, vendored as a
			// fixture so the suite does not depend on a sibling checkout.
			filepath.Join("crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
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
	})
	Expect(err).NotTo(HaveOccurred())

	log := logger.NewLoggerFromLogr(GinkgoLogr)
	cfgParams := config.Options{
		ControllerNamespace:         controllerNamespace,
		ConfigSecretName:            config.ConfigSecretName,
		RequeueStorageClassInterval: 1,
	}
	_, err = controller.RunLocalStorageClassWatcherController(mgr, cfgParams, log)
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
