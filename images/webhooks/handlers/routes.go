/*
Copyright 2026 Flant JSC

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

package handlers

import (
	"fmt"
	"net/http"

	storagev1 "k8s.io/api/storage/v1"

	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-common-lib/logger/kwhlogger"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	LSCValidatorID = "LSCValidator"
	SCValidatorID  = "SCValidator"
	LVGValidatorID = "LVGValidator"
)

func Handlers(log logger.Logger) (map[string]http.Handler, error) {
	// kubewebhook takes its own logger interface; this adapter routes the
	// library's own messages into the same output and format as ours.
	kwhLogger := kwhlogger.New(log)

	lsc, err := GetValidatingWebhookHandler(LSCValidate(log), LSCValidatorID, &slv.LocalStorageClass{}, kwhLogger)
	if err != nil {
		return nil, fmt.Errorf("creating the LocalStorageClass validating webhook handler: %w", err)
	}
	sc, err := GetValidatingWebhookHandler(SCValidate(log), SCValidatorID, &storagev1.StorageClass{}, kwhLogger)
	if err != nil {
		return nil, fmt.Errorf("creating the StorageClass validating webhook handler: %w", err)
	}
	lvg, err := GetValidatingWebhookHandler(LVGValidate(log), LVGValidatorID, &snc.LVMVolumeGroup{}, kwhLogger)
	if err != nil {
		return nil, fmt.Errorf("creating the LVMVolumeGroup validating webhook handler: %w", err)
	}
	return map[string]http.Handler{
		"/lsc-validate": lsc,
		"/sc-validate":  sc,
		"/lvg-validate": lvg,
	}, nil
}
