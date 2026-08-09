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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDurationFromEnv pins the contract the chart relies on: the variable is left
// out of the Deployment entirely when the setting is not configured, so an absent
// value has to mean the default rather than zero. The other branches are what a
// hand-edited Deployment can produce, and none of them may take the controller
// down — it has a working default, and a typo in an override is not worth refusing
// to start over.
func TestDurationFromEnv(t *testing.T) {
	const def = 30 * time.Second

	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{
			name: "an unset variable is the documented way of asking for the default",
			raw:  "",
			want: def,
		},
		{
			name: "whitespace is not a value either",
			raw:  "   ",
			want: def,
		},
		{
			name: "a valid duration is taken as it is",
			raw:  "45s",
			want: 45 * time.Second,
		},
		{
			name: "any unit time.ParseDuration knows is accepted, whatever the schema allows",
			raw:  "1m30s",
			want: 90 * time.Second,
		},
		{
			name: "an unparsable value falls back rather than failing to start",
			raw:  "soon",
			want: def,
		},
		{
			name: "zero would make the grace period no grace period at all",
			raw:  "0s",
			want: def,
		},
		{
			name: "and so would a negative one",
			raw:  "-5s",
			want: def,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_DURATION", tt.raw)
			assert.Equal(t, tt.want, durationFromEnv("TEST_DURATION", def))
		})
	}
}

// TestNewConfigReadsTheGracePeriod checks the wiring rather than the parsing: the
// default lives in this package alone, because the setting carries none in
// openapi/config-values.yaml and the chart omits the variable when it is unset.
func TestNewConfigReadsTheGracePeriod(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv(LLVOrphanGracePeriodEnvName, "")
		assert.Equal(t, DefaultLLVOrphanGracePeriod, NewConfig().LLVOrphanGracePeriod)
	})

	t.Run("configured", func(t *testing.T) {
		t.Setenv(LLVOrphanGracePeriodEnvName, "5m")
		assert.Equal(t, 5*time.Minute, NewConfig().LLVOrphanGracePeriod)
	})
}
