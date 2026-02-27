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

package consts

const (
	ModuleName       string = "sdsLocalVolume"
	ModuleNamespace  string = "d8-sds-local-volume"
	ModulePluralName string = "sds-local-volume"
	WebhookCertCn    string = "webhooks"
	SchedulerCertCn  string = "scheduler-extender"
)

var AllowedProvisioners = []string{
	"local.csi.storage.deckhouse.io",
}

var WebhookConfigurationsToDelete = []string{}

var CRGVKsForFinalizerRemoval = []CRGVK{
	{Group: "storage.deckhouse.io", Version: "v1alpha1", Kind: "LocalStorageClass", Namespaced: false},
}

type CRGVK struct {
	Group      string
	Version    string
	Kind       string
	Namespaced bool
}
