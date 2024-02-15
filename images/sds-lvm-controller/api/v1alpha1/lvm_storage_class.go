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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type LvmStorageClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LvmStorageClassSpec    `json:"spec"`
	Status            *LvmStorageClassStatus `json:"status,omitempty"`
}

// LvmStorageClassList contains a list of empty block device
type LvmStorageClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []LvmStorageClass `json:"items"`
}

type LvmStorageClassSpec struct {
	IsDefault            bool                 `json:"isDefault"`
	Type                 string               `json:"type"`
	ReclaimPolicy        string               `json:"reclaimPolicy"`
	VolumeBindingMode    string               `json:"volumeBindingMode"`
	AllowVolumeExpansion bool                 `json:"allowVolumeExpansion"`
	LVMVolumeGroups      []LvmStorageClassLVG `json:"lvmVolumeGroups"`
}

type LvmStorageClassStatus struct {
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type LvmStorageClassLVG struct {
	Name string                   `json:"name"`
	Thin *LvmStorageClassThinPool `json:"thin,omitempty"`
}

type LvmStorageClassThinPool struct {
	PoolName string `json:"poolName"`
}
