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

package config

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
)

const (
	LogLevel                             = "LOG_LEVEL"
	RequeueInterval                      = "REQUEUE_INTERVAL"
	ConfigSecretName                     = "d8-sds-local-volume-controller-config"
	ControllerNamespaceEnv               = "CONTROLLER_NAMESPACE"
	HardcodedControllerNS                = "d8-sds-local-volume"
	ControllerName                       = "d8-controller"
	DefaultHealthProbeBindAddressEnvName = "HEALTH_PROBE_BIND_ADDRESS"
	DefaultHealthProbeBindAddress        = ":8081"
	MetricsBindAddressEnvName            = "METRICS_BIND_ADDRESS"
	DefaultMetricsBindAddress            = ":8080"

	// DefaultLLVSweepInterval is how often the LVMLogicalVolume garbage collector
	// re-scans on its own. The scan is event-driven, so this only bounds how stale
	// the orphan metrics can get if an event is ever missed; it is deliberately not
	// configurable, because there is nothing an operator would gain by tuning it.
	DefaultLLVSweepInterval = time.Minute

	// LLVOrphanGracePeriodEnvName overrides how long a deleted LVMLogicalVolume
	// whose PersistentVolume is missing — or a deleted LVMLogicalVolumeSnapshot — is
	// left alone before its CSI finalizer is removed. The delay lets an in-flight
	// DeleteVolume or DeleteSnapshot finish on its own.
	//
	// The chart passes this from the module's llvOrphanGracePeriod setting, so the
	// value is reachable through a ModuleConfig rather than only from the code.
	//
	// DefaultLLVOrphanGracePeriod is the only place the default lives: the setting
	// carries no default in openapi/config-values.yaml, so the chart omits the
	// variable entirely when nothing is configured, and the two cannot drift apart.
	LLVOrphanGracePeriodEnvName = "LLV_ORPHAN_GRACE_PERIOD"
	DefaultLLVOrphanGracePeriod = 30 * time.Second
)

type Options struct {
	Loglevel                    logger.Verbosity
	RequeueStorageClassInterval time.Duration
	RequeueSecretInterval       time.Duration
	ConfigSecretName            string
	ControllerNamespace         string
	HealthProbeBindAddress      string
	MetricsBindAddress          string

	// LLVSweepInterval and LLVOrphanGracePeriod are whole durations, unlike the
	// two Requeue* fields above, which are bare numbers the call sites multiply by
	// time.Second.
	LLVSweepInterval     time.Duration
	LLVOrphanGracePeriod time.Duration
}

func NewConfig() *Options {
	var opts Options

	loglevel := os.Getenv(LogLevel)
	if loglevel == "" {
		opts.Loglevel = logger.DebugLevel
	} else {
		opts.Loglevel = logger.Verbosity(loglevel)
	}

	opts.HealthProbeBindAddress = os.Getenv(DefaultHealthProbeBindAddressEnvName)
	if opts.HealthProbeBindAddress == "" {
		opts.HealthProbeBindAddress = DefaultHealthProbeBindAddress
	}

	opts.MetricsBindAddress = os.Getenv(MetricsBindAddressEnvName)
	if opts.MetricsBindAddress == "" {
		opts.MetricsBindAddress = DefaultMetricsBindAddress
	}

	opts.ControllerNamespace = os.Getenv(ControllerNamespaceEnv)
	if opts.ControllerNamespace == "" {
		namespace, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err != nil {
			log.Printf("Failed to get namespace from filesystem: %v", err)
			log.Printf("Using hardcoded namespace: %s", HardcodedControllerNS)
			opts.ControllerNamespace = HardcodedControllerNS
		} else {
			log.Printf("Got namespace from filesystem: %s", string(namespace))
			opts.ControllerNamespace = string(namespace)
		}
	}

	opts.RequeueStorageClassInterval = 10
	opts.RequeueSecretInterval = 10
	opts.ConfigSecretName = ConfigSecretName

	opts.LLVSweepInterval = DefaultLLVSweepInterval
	opts.LLVOrphanGracePeriod = durationFromEnv(LLVOrphanGracePeriodEnvName, DefaultLLVOrphanGracePeriod)

	return &opts
}

// durationFromEnv reads a Go duration (for example "45s") from the environment,
// falling back to def when the variable is unset, unparsable or not positive. A
// bad value is logged and ignored rather than fatal: the controller has a working
// default and a typo in an override should not keep it from starting.
func durationFromEnv(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}

	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("Ignoring %s=%q, using %s", name, raw, def)
		return def
	}

	return d
}

// SdsLocalVolumeConfig mirrors the structure of the controller-config Secret
// (stringData."config"). The Secret is rendered by Helm from values.yaml /
// config-values.yaml and consumed by the controller at runtime.
//
// Struct tags are `json:` (not `yaml:`) because the controller uses
// sigs.k8s.io/yaml, which converts YAML to JSON and then uses encoding/json
// — `yaml:` tags have no effect there.
type SdsLocalVolumeConfig struct {
	NodeSelector map[string]string `json:"nodeSelector"`
	// StorageClassLabelIgnoredPrefixes holds the union of the system list (from
	// internal values) and the user-configured list (from ModuleConfig). Labels
	// on a LocalStorageClass whose keys start with any of these prefixes are
	// NOT propagated to the managed Kubernetes StorageClass.
	StorageClassLabelIgnoredPrefixes []string `json:"storageClassLabelIgnoredPrefixes"`
}
