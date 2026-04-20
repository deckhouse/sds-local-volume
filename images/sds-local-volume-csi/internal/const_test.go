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

package internal

import "testing"

// TestTopologyKeyPin pins the literal value of TopologyKey. It MUST stay in
// sync with controller/pkg/controller.TopologyKey, but because the controller
// and CSI driver live in separate Go modules they cannot import each other.
// If you change this value here, you MUST also change it in the controller
// package (and update its mirror test).
func TestTopologyKeyPin(t *testing.T) {
	const expected = "topology.sds-local-volume-csi/node"
	if TopologyKey != expected {
		t.Fatalf("TopologyKey drifted: got %q, want %q (also update controller.TopologyKey)", TopologyKey, expected)
	}
}
