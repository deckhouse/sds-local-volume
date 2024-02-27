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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type LVMStorageClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LVMStorageClassSpec    `json:"spec"`
	Status            *LVMStorageClassStatus `json:"status,omitempty"`
}

// LVMStorageClassList contains a list of empty block device
type LVMStorageClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []LVMStorageClass `json:"items"`
}

type LVMStorageClassSpec struct {
	IsDefault         bool                 `json:"isDefault"`
	Type              string               `json:"type"`
	ReclaimPolicy     string               `json:"reclaimPolicy"`
	VolumeBindingMode string               `json:"volumeBindingMode"`
	LVMVolumeGroups   []LVMStorageClassLVG `json:"lvmVolumeGroups"`
}

type LVMStorageClassStatus struct {
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type LVMStorageClassLVG struct {
	Name string                   `json:"name"`
	Thin *LVMStorageClassThinPool `json:"thin,omitempty"`
}

type LVMStorageClassThinPool struct {
	PoolName string `json:"poolName"`
}
