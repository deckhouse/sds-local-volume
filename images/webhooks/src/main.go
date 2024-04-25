/*
Copyright 2024 Flant JSC

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
	"webhooks/handlers"
	"webhooks/v1alpha1"

	"github.com/sirupsen/logrus"
	kwhlogrus "github.com/slok/kubewebhook/v2/pkg/log/logrus"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

type config struct {
	certFile string
	keyFile  string
}

//goland:noinspection SpellCheckingInspection
func httpHandlerHealthz(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Ok.")
}

func initFlags() config {
	cfg := config{}

	fl := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fl.StringVar(&cfg.certFile, "tls-cert-file", "", "TLS certificate file")
	fl.StringVar(&cfg.keyFile, "tls-key-file", "", "TLS key file")

	fl.Parse(os.Args[1:])
	return cfg
}

const (
	port                  = ":8443"
	PodSchedulerMutatorID = "PodSchedulerMutation"
	LSCValidatorId        = "LSCValidator"
	SCValidatorId         = "SCValidator"
)

func main() {
	logrusLogEntry := logrus.NewEntry(logrus.New())
	logrusLogEntry.Logger.SetLevel(logrus.DebugLevel)
	logger := kwhlogrus.NewLogrus(logrusLogEntry)

	cfg := initFlags()

	podSchedulerMutatingWebHookHandler, err := handlers.GetMutatingWebhookHandler(handlers.PodSchedulerMutate, PodSchedulerMutatorID, &corev1.Pod{}, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating podSchedulerMutatingWebHookHandler: %s", err)
		os.Exit(1)
	}

	lscValidatingWebhookHandler, err := handlers.GetValidatingWebhookHandler(handlers.LSCValidate, LSCValidatorId, &v1alpha1.LocalStorageClass{}, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating lscValidatingWebhookHandler: %s", err)
		os.Exit(1)
	}

	scValidatingWebhookHandler, err := handlers.GetValidatingWebhookHandler(handlers.SCValidate, SCValidatorId, &storagev1.StorageClass{}, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating scValidatingWebhookHandler: %s", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/pod-scheduler-mutate", podSchedulerMutatingWebHookHandler)
	mux.Handle("/lsc-validate", lscValidatingWebhookHandler)
	mux.Handle("/sc-validate", scValidatingWebhookHandler)
	mux.HandleFunc("/healthz", httpHandlerHealthz)

	logger.Infof("Listening on %s", port)
	err = http.ListenAndServeTLS(port, cfg.certFile, cfg.keyFile, mux)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error serving webhook: %s", err)
		os.Exit(1)
	}
}
