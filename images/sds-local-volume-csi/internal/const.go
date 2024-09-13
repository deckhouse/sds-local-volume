/*
Copyright 2024 Flant JSC

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

const (
	TypeKey                     = "local.csi.storage.deckhouse.io/type"
	Lvm                         = "lvm"
	LvmTypeKey                  = "local.csi.storage.deckhouse.io/lvm-type"
	BindingModeKey              = "local.csi.storage.deckhouse.io/volume-binding-mode"
	LvmVolumeGroupKey           = "local.csi.storage.deckhouse.io/lvm-volume-groups"
	LVMVThickContiguousParamKey = "local.csi.storage.deckhouse.io/lvm-thick-contiguous"
	TopologyKey                 = "topology.sds-local-volume-csi/node"
	SubPath                     = "subPath"
	VGNameKey                   = "vgname"
	ThinPoolNameKey             = "thinPoolName"
	LVMTypeThin                 = "Thin"
	LVMTypeThick                = "Thick"
	LLVStatusCreated            = "Created"
	BindingModeWFFC             = "WaitForFirstConsumer"
	BindingModeI                = "Immediate"
	ResizeDelta                 = "32Mi"

	FSTypeKey = "csi.storage.k8s.io/fstype"
	// FSTypeExt4 represents the ext4 filesystem type
	FSTypeExt4 = "ext4"
	FSTypeXfs  = "xfs"

	// BlockSizeKey configures the block size when formatting a volume
	BlockSizeKey = "blocksize"

	// InodeSizeKey configures the inode size when formatting a volume
	InodeSizeKey = "inodesize"

	// BytesPerInodeKey configures the `bytes-per-inode` when formatting a volume
	BytesPerInodeKey = "bytesperinode"

	// NumberOfInodesKey configures the `number-of-inodes` when formatting a volume
	NumberOfInodesKey = "numberofinodes"

	// Ext4ClusterSizeKey enables the bigalloc option when formatting an ext4 volume
	Ext4BigAllocKey = "ext4bigalloc"

	// Ext4ClusterSizeKey configures the cluster size when formatting an ext4 volume with the bigalloc option enabled
	Ext4ClusterSizeKey = "ext4clustersize"
)

type FileSystemConfig struct {
	NotSupportedParams map[string]struct{}
}

func (fsConfig FileSystemConfig) IsParameterSupported(paramName string) bool {
	_, notSupported := fsConfig.NotSupportedParams[paramName]
	return !notSupported
}

var (
	FileSystemConfigs = map[string]FileSystemConfig{
		FSTypeExt4: {
			NotSupportedParams: map[string]struct{}{},
		},
		FSTypeXfs: {
			NotSupportedParams: map[string]struct{}{
				BytesPerInodeKey:   {},
				NumberOfInodesKey:  {},
				Ext4BigAllocKey:    {},
				Ext4ClusterSizeKey: {},
			},
		},
	}
)
