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
	"net/http"
	"os"
	"os/signal"
	"syscall"

	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/config"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/driver"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/kubutils"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/monitoring"
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

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, err := fmt.Fprint(w, "OK")
	if err != nil {
		klog.Fatalf("Error while generating healthcheck, err: %s", err.Error())
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	cfgParams, err := config.NewConfig()
	if err != nil {
		klog.Fatalf("unable to create NewConfig, err: %s", err.Error())
		os.Exit(1)
	}

	log, err := logger.NewLogger(cfgParams.Loglevel)
	if err != nil {
		fmt.Printf("unable to create NewLogger, err: %v\n", err)
		os.Exit(1)
	}

	log.Info("starting sds-local-volume-csi", "version", cfgParams.Version)

	kConfig, err := kubutils.KubernetesDefaultConfigCreate()
	if err != nil {
		log.Error("unable to KubernetesDefaultConfigCreate", logger.Err(err))
		os.Exit(1)
	}
	log.Info("kubernetes config has been successfully created.")

	scheme := apiruntime.NewScheme()
	for _, f := range resourcesSchemeFuncs {
		err := f(scheme)
		if err != nil {
			log.Error("unable to add scheme to func", logger.Err(err))
			os.Exit(1)
		}
	}
	log.Info("successfully read scheme CR")

	cl, err := client.New(kConfig, client.Options{
		Scheme: scheme,
	})

	http.HandleFunc("/healthz", healthHandler)
	http.HandleFunc("/readyz", healthHandler)
	go func() {
		err = http.ListenAndServe(cfgParams.HealthProbeBindAddress, nil)
		if err != nil {
			log.Error("create probes", logger.Err(err))
		}
	}()

	// The driver runs no controller-runtime manager, so it keeps its own registry
	// and serves it on a dedicated listener. A separate mux keeps /metrics off the
	// default one used by the health probes above.
	metricStorage := metricsstorage.NewMetricStorage(metricsstorage.WithNewRegistry())
	if err = monitoring.Register(metricStorage); err != nil {
		log.Error("unable to register the metrics", logger.Err(err))
		os.Exit(1)
	}
	go func() {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", metricStorage.Handler())
		if err := http.ListenAndServe(cfgParams.MetricsBindAddress, metricsMux); err != nil {
			log.Error("unable to serve the metrics", logger.Err(err))
		}
	}()
	log.Info("metrics registered", "address", cfgParams.MetricsBindAddress+"/metrics")

	drv, err := driver.NewDriver(cfgParams.CsiAddress, cfgParams.DriverName, cfgParams.Address, &cfgParams.NodeName, log, monitoring.NewRecorder(metricStorage), cl)
	if err != nil {
		log.Error("create NewDriver", logger.Err(err))
	}

	defer cancel()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-c
		cancel()
	}()

	if err := drv.Run(ctx); err != nil {
		log.Error("[dev.Run]", logger.Err(err))
	}
}
