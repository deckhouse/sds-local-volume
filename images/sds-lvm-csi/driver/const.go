/*
Copyright 2023 Flant JSC

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

const (
	lvmType          = "lvm.csi.storage.deckhouse.io/lvm-type"
	lvmBindingMode   = "lvm.csi.storage.deckhouse.io/volume-binding-mode"
	lvmVolumeGroup   = "lvm.csi.storage.deckhouse.io/lvm-volume-groups"
	topologyKey      = "topology.sds-lvm-csi/node"
	subPath          = "subPath"
	VGNameKey        = "vgname"
	LLVTypeThin      = "Thin"
	LLVTypeThick     = "Thick"
	LLVStatusCreated = "Created"
	BindingModeWFFC  = "WaitForFirstConsumer"
	BindingModeI     = "Immediate"
)
