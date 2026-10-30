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

package v1alpha1

import (
	"fmt"
	"strings"
)

// CSINodeLabel marks the nodes the module's CSI node runs on. The controller
// sets it, with an empty value, on the nodes matching
// spec.settings.dataNodes.nodeSelector, and the CSI node DaemonSet selects on
// exactly that key and value.
const CSINodeLabel = "storage.deckhouse.io/sds-local-volume-node"

// LVMVolumeGroupsOutsideCSINodesMessage describes LVMVolumeGroups, given as
// "<lvg> (<node>)", whose nodes do not run the module's CSI node. It is shared by
// the webhook warning and the LocalStorageClass status, so both say the same.
func LVMVolumeGroupsOutsideCSINodesMessage(lvgs []string) string {
	if len(lvgs) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"volumes cannot be used on the LVMVolumeGroups %s: their nodes do not run the sds-local-volume CSI node; "+
			"add the nodes to spec.settings.dataNodes.nodeSelector of the sds-local-volume module",
		strings.Join(lvgs, ", "))
}
