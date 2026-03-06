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

package internal

import "os"

const (
	TypeKey                     = "local.csi.storage.deckhouse.io/type"
	Lvm                         = "lvm"
	RawFile                     = "rawfile"
	LvmTypeKey                  = "local.csi.storage.deckhouse.io/lvm-type"
	BindingModeKey              = "local.csi.storage.deckhouse.io/volume-binding-mode"
	LVMVolumeGroupKey           = "local.csi.storage.deckhouse.io/lvm-volume-groups"
	LVMVThickContiguousParamKey = "local.csi.storage.deckhouse.io/lvm-thick-contiguous"
	ActualNameOnTheNodeKey      = "local.csi.storage.deckhouse.io/actualNameOnTheNode"
	TopologyKey                 = "topology.sds-local-volume-csi/node"
	SubPath                     = "subPath"
	VGNameKey                   = "vgname"
	ThinPoolNameKey             = "thinPoolName"
	LVMTypeThin                 = "Thin"
	LVMTypeThick                = "Thick"
	LLVStatusCreated            = "Created"
	LLVSStatusCreated           = "Created"
	BindingModeWFFC             = "WaitForFirstConsumer"
	BindingModeI                = "Immediate"
	ResizeDelta                 = "32Mi"

	FSTypeKey = "csi.storage.k8s.io/fstype"

	// supported filesystem types
	FSTypeExt4 = "ext4"
	FSTypeXfs  = "xfs"

	// RawFile volume type constants
	RawFileDataDirKey        = "local.csi.storage.deckhouse.io/rawfile-data-dir"
	RawFileDefaultDir        = "/var/lib/sds-local-volume/rawfile"
	RawFileDefaultDataDirEnv = "RAWFILE_DEFAULT_DATA_DIR"
	RawFileDevicePathKey     = "rawfileDevicePath"
	RawFilePathKey           = "rawfilePath"
	RawFileSparseKey         = "local.csi.storage.deckhouse.io/rawfile-sparse"
	RawFileSizeKey           = "rawfileSize"
	RawFilePVFinalizer       = "storage.deckhouse.io/rawfile-pv-protection"
)

// GetRawFileDataDir returns the data directory for RawFile volumes.
// It first checks the environment variable, then falls back to the default.
func GetRawFileDataDir() string {
	if dir := os.Getenv(RawFileDefaultDataDirEnv); dir != "" {
		return dir
	}
	return RawFileDefaultDir
}
