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

package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deckhouse/sds-common-lib/logger"
)

// TestNewConfigReadsTheLogLevel checks the wiring to the shared logger and pins the
// fallback, which is this module's choice: the CSI templates always set LOG_LEVEL,
// so an unset value only happens outside the chart, and it has always meant Info.
func TestNewConfigReadsTheLogLevel(t *testing.T) {
	// NewConfig parses os.Args, which under go test carries the test flags.
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"sds-local-volume-csi"}

	t.Setenv(NodeName, "node-0")

	t.Run("unset", func(t *testing.T) {
		t.Setenv(logger.EnvLogLevel, "")
		opts, err := NewConfig()
		require.NoError(t, err)
		assert.Equal(t, logger.InfoLevel, opts.Loglevel)
	})

	t.Run("configured", func(t *testing.T) {
		t.Setenv(logger.EnvLogLevel, "4")
		opts, err := NewConfig()
		require.NoError(t, err)
		assert.Equal(t, logger.TraceLevel, opts.Loglevel)
	})
}
