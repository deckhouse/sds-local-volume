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

const (
	SLVStorageClassCtrlName                           = "d8-sds-local-volume-storage-class-controller"
	SLVStorageClassVolumeSnapshotClassAnnotationKey   = "storage.deckhouse.io/volumesnapshotclass"
	SLVStorageClassVolumeSnapshotClassAnnotationValue = "sds-local-volume-snapshot-class"
	SLVStorageManagedLabelKey                         = "storage.deckhouse.io/managed-by"
)
