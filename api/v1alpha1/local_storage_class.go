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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type LocalStorageClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LocalStorageClassSpec    `json:"spec"`
	Status            *LocalStorageClassStatus `json:"status,omitempty"`
}

// LocalStorageClassList contains a list of empty block device
type LocalStorageClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []LocalStorageClass `json:"items"`
}

type LocalStorageClassSpec struct {
	ReclaimPolicy     string                    `json:"reclaimPolicy"`
	VolumeBindingMode string                    `json:"volumeBindingMode"`
	LVM               *LocalStorageClassLVMSpec `json:"lvm,omitempty"`
	FSType            string                    `json:"fsType,omitempty"`
}

type LocalStorageClassLVMSpec struct {
	Type            string                         `json:"type"`
	Thick           *LocalStorageClassLVMThickSpec `json:"thick,omitempty"`
	VolumeCleanup   string                         `json:"volumeCleanup,omitempty"`
	LVMVolumeGroups []LocalStorageClassLVG         `json:"lvmVolumeGroups"`
}

type LocalStorageClassStatus struct {
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type LocalStorageClassLVG struct {
	Name string                            `json:"name"`
	Thin *LocalStorageClassLVMThinPoolSpec `json:"thin,omitempty"`
}

type LocalStorageClassLVMThinPoolSpec struct {
	PoolName string `json:"poolName"`
}

type LocalStorageClassLVMThickSpec struct {
	Contiguous bool `json:"contiguous,omitempty"`
}
