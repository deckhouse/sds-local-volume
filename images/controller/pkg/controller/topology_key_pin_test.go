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

package controller_test

import (
	"testing"

	"github.com/deckhouse/sds-local-volume/images/controller/pkg/controller"
)

// TestTopologyKeyPin pins the literal value of TopologyKey. It MUST stay in
// sync with sds-local-volume-csi/internal.TopologyKey, but because the
// controller and CSI driver live in separate Go modules they cannot import
// each other. If you change this value here, you MUST also change it in the
// CSI internal package (and update its mirror test).
func TestTopologyKeyPin(t *testing.T) {
	const expected = "topology.sds-local-volume-csi/node"
	if controller.TopologyKey != expected {
		t.Fatalf("controller.TopologyKey drifted: got %q, want %q (also update internal.TopologyKey)", controller.TopologyKey, expected)
	}
}
