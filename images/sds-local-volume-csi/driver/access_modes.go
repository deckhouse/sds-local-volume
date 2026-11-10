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

package driver

import (
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

func validateVolumeCapabilities(capabilities []*csi.VolumeCapability) error {
	if len(capabilities) == 0 {
		return fmt.Errorf("volume capabilities cannot be empty")
	}

	for i, capability := range capabilities {
		if capability == nil {
			return fmt.Errorf("volume capability #%d is empty", i)
		}

		switch capability.GetAccessType().(type) {
		case *csi.VolumeCapability_Block, *csi.VolumeCapability_Mount:
		default:
			return fmt.Errorf("volume capability #%d has neither block nor mount access type", i)
		}

		mode := capability.GetAccessMode().GetMode()
		switch mode {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY:
		default:
			return fmt.Errorf("access mode %s is not supported: volumes are local to a single node, only SINGLE_NODE_WRITER (ReadWriteOnce) and SINGLE_NODE_READER_ONLY are allowed", mode)
		}
	}

	return nil
}
