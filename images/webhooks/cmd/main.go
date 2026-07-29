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
	"flag"
	"fmt"
	"net/http"
	"os"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/webhooks/handlers"
	"github.com/deckhouse/sds-local-volume/images/webhooks/pkg/logger"
)

type config struct {
	certFile string
	keyFile  string
	logLevel logger.Verbosity
}

//goland:noinspection SpellCheckingInspection
func httpHandlerHealthz(w http.ResponseWriter, _ *http.Request) {
	_, err := fmt.Fprint(w, "Ok.")
	if err != nil {
		w.WriteHeader(500)
	}
}

func initFlags() (config, error) {
	cfg := config{}

	// The level arrives as a flag rather than as LOG_LEVEL, because lib-helm's
	// helm_lib_module_controller_manifests injects no env into the webhooks
	// container but does allow its command to be overridden. The accepted values
	// are the same numeric 0..4 verbosities the other components take.
	var logLevel string

	fl := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fl.StringVar(&cfg.certFile, "tls-cert-file", "", "TLS certificate file")
	fl.StringVar(&cfg.keyFile, "tls-key-file", "", "TLS key file")
	fl.StringVar(&logLevel, "log-level", string(logger.InfoLevel), "Log verbosity: 0 error, 1 warn, 2 info, 3 debug, 4 trace")

	err := fl.Parse(os.Args[1:])
	if err != nil {
		return cfg, err
	}

	cfg.logLevel = logger.Verbosity(logLevel)
	if _, err := cfg.logLevel.Level(); err != nil {
		return cfg, err
	}

	return cfg, nil
}

const (
	port                  = ":8443"
	PodSchedulerMutatorID = "PodSchedulerMutation"
	LSCValidatorID        = "LSCValidator"
	SCValidatorID         = "SCValidator"
)

func main() {
	cfg, err := initFlags()
	if err != nil {
		fmt.Printf("unable to parse config: err: %s", err.Error())
		os.Exit(1)
	}

	baseLog, err := logger.NewLogger(cfg.logLevel)
	if err != nil {
		fmt.Printf("unable to create NewLogger, err: %v\n", err)
		os.Exit(1)
	}
	log := baseLog.Named("webhooks")

	// kubewebhook takes its own logger interface; this adapter routes the
	// library's own messages into the same output and format as ours.
	kwhLogger := logger.NewKubewebhookLogger(log)

	podSchedulerMutatingWebHookHandler, err := handlers.GetMutatingWebhookHandler(handlers.PodSchedulerMutate, PodSchedulerMutatorID, &corev1.Pod{}, kwhLogger)
	if err != nil {
		log.Error("unable to create the pod scheduler mutating webhook handler", logger.Err(err))
		os.Exit(1)
	}

	lscValidatingWebhookHandler, err := handlers.GetValidatingWebhookHandler(handlers.LSCValidate(log), LSCValidatorID, &slv.LocalStorageClass{}, kwhLogger)
	if err != nil {
		log.Error("unable to create the LocalStorageClass validating webhook handler", logger.Err(err))
		os.Exit(1)
	}

	scValidatingWebhookHandler, err := handlers.GetValidatingWebhookHandler(handlers.SCValidate(log), SCValidatorID, &storagev1.StorageClass{}, kwhLogger)
	if err != nil {
		log.Error("unable to create the StorageClass validating webhook handler", logger.Err(err))
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/pod-scheduler-mutate", podSchedulerMutatingWebHookHandler)
	mux.Handle("/lsc-validate", lscValidatingWebhookHandler)
	mux.Handle("/sc-validate", scValidatingWebhookHandler)
	mux.HandleFunc("/healthz", httpHandlerHealthz)

	log.Info("listening", "port", port)
	if err = http.ListenAndServeTLS(port, cfg.certFile, cfg.keyFile, mux); err != nil {
		log.Error("unable to serve the webhook", logger.Err(err))
		os.Exit(1)
	}
}
