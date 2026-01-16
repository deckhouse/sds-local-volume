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

package sds_local_volume

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/deckhouse/storage-e2e/pkg/config"
	"github.com/deckhouse/storage-e2e/pkg/logger"
)

var _ = BeforeSuite(func() {
	// Validate environment first to set defaults (including LogLevel)
	err := config.ValidateEnvironment()
	Expect(err).NotTo(HaveOccurred(), "Failed to validate environment")

	// Initialize logger with configured log level (from LOG_LEVEL env var or default)
	err = logger.Initialize()
	Expect(err).NotTo(HaveOccurred(), "Failed to initialize logger")
})

var _ = AfterSuite(func() {
	// Close logger and any open log files
	if err := logger.Close(); err != nil {
		GinkgoWriter.Printf("Warning: Failed to close logger: %v\n", err)
	}
})

func TestSdsLocalVolume(t *testing.T) {
	RegisterFailHandler(Fail)
	// Configure Ginkgo to show verbose output
	suiteConfig, reporterConfig := GinkgoConfiguration()
	reporterConfig.Verbose = true
	reporterConfig.ShowNodeEvents = false
	RunSpecs(t, "Sds Local Volume Suite", suiteConfig, reporterConfig)
}
