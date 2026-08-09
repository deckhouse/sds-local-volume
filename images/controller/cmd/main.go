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

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	goruntime "runtime"

	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/config"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/kubutils"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

var (
	resourcesSchemeFuncs = []func(*apiruntime.Scheme) error{
		snc.AddToScheme,
		slv.AddToScheme,
		clientgoscheme.AddToScheme,
		extv1.AddToScheme,
		v1.AddToScheme,
		sv1.AddToScheme,
	}
)

func main() {
	ctx := context.Background()
	cfgParams := config.NewConfig()

	log, err := logger.NewLogger(cfgParams.Loglevel)
	if err != nil {
		fmt.Printf("unable to create NewLogger, err: %v\n", err)
		os.Exit(1)
	}

	log.Info("starting the sds-local-volume controller", "goVersion", goruntime.Version(), "os", goruntime.GOOS, "arch", goruntime.GOARCH)

	log.Info("configuration loaded", config.LogLevel, string(cfgParams.Loglevel), config.RequeueInterval, cfgParams.RequeueStorageClassInterval)

	kConfig, err := kubutils.KubernetesDefaultConfigCreate()
	if err != nil {
		log.Error("unable to create the kubernetes config", logger.Err(err))
	}
	log.Info("kubernetes config created")

	scheme := apiruntime.NewScheme()
	for _, f := range resourcesSchemeFuncs {
		err := f(scheme)
		if err != nil {
			log.Error("unable to add a resource to the scheme", logger.Err(err))
			os.Exit(1)
		}
	}
	log.Info("scheme built")

	// The storage keeps its own registry and is then plugged into the
	// controller-runtime registry as a collector, so that a single metrics
	// endpoint serves both our metrics and the controller-runtime ones
	// (workqueue depth, reconcile totals, client-go latency).
	metricStorage := metricsstorage.NewMetricStorage(metricsstorage.WithNewRegistry())
	if err = monitoring.Register(metricStorage); err != nil {
		log.Error("unable to register the metrics", logger.Err(err))
		os.Exit(1)
	}
	if err = ctrlmetrics.Registry.Register(metricStorage.Collector()); err != nil {
		log.Error("unable to add the metrics to the controller-runtime registry", logger.Err(err))
		os.Exit(1)
	}
	metrics := monitoring.NewRecorder(metricStorage)
	log.Info("metrics registered", "address", cfgParams.MetricsBindAddress+"/metrics")

	cacheOpt := cache.Options{
		DefaultNamespaces: map[string]cache.Config{
			cfgParams.ControllerNamespace: {},
		},
		ByObject: map[client.Object]cache.ByObject{
			// The LVMLogicalVolume garbage collector needs every PersistentVolume in
			// the cluster, but only its name and its CSI reference. Caching them whole
			// would grow with the cluster for no reason, so everything else is dropped
			// on the way into the cache.
			&v1.PersistentVolume{}: {Transform: controller.StripPersistentVolume},
		},
	}

	managerOpts := manager.Options{
		Scheme:                  scheme,
		Cache:                   cacheOpt,
		LeaderElection:          true,
		LeaderElectionNamespace: cfgParams.ControllerNamespace,
		LeaderElectionID:        config.ControllerName,
		Logger:                  log.GetLogger(),
		HealthProbeBindAddress:  cfgParams.HealthProbeBindAddress,
		Metrics: metricsserver.Options{
			BindAddress: cfgParams.MetricsBindAddress,
		},
	}

	mgr, err := manager.New(kConfig, managerOpts)
	if err != nil {
		log.Error("unable to create the manager", logger.Err(err))
		os.Exit(1)
	}
	log.Info("manager created")

	if _, err = controller.RunLocalStorageClassWatcherController(mgr, *cfgParams, log, metrics); err != nil {
		log.Error("unable to run the controller", logger.Err(err), slog.String("controller", controller.LocalStorageClassCtrlName))
		os.Exit(1)
	}

	if _, err = controller.RunLocalCSINodeWatcherController(mgr, *cfgParams, log, metrics); err != nil {
		log.Error("unable to run the controller", logger.Err(err), slog.String("controller", controller.LocalCSINodeWatcherCtrl))
		os.Exit(1)
	}

	if _, err = controller.RunLVMLogicalVolumeGCController(mgr, *cfgParams, log, metrics); err != nil {
		log.Error("unable to run the controller", logger.Err(err), slog.String("controller", controller.LVMLogicalVolumeGCCtrlName))
		os.Exit(1)
	}

	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error("unable to add the healthz check", logger.Err(err))
		os.Exit(1)
	}

	if err = mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error("unable to add the readyz check", logger.Err(err))
		os.Exit(1)
	}
	log.Info("health probes registered")

	err = mgr.Start(ctx)
	if err != nil {
		log.Error("the manager stopped with an error", logger.Err(err))
		os.Exit(1)
	}
}
