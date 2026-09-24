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

// LocalStorageClass is a Kubernetes Custom Resource that defines a configuration for a Kubernetes StorageClass.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=lsc
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels={"heritage=deckhouse","module=sds-local-volume","backup.deckhouse.io/cluster-config=true"}
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=".status.reason",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of this resource"
// +crd-enricher:crd:minimal=true
// +crd-enricher:crd:preserveUnknownFields=false
type LocalStorageClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LocalStorageClassSpec    `json:"spec"`
	Status            *LocalStorageClassStatus `json:"status,omitempty"`
}

// LocalStorageClassList contains a list of empty block device
// +kubebuilder:object:root=true
type LocalStorageClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []LocalStorageClass `json:"items"`
}

// Defines a Kubernetes StorageClass configuration.
type LocalStorageClassSpec struct {
	// StorageClass reclaim policy. Possible values:
	//
	// - `Delete`: If the PersistentVolumeClaim is deleted, the PersistentVolume and its associated storage are also deleted
	// - `Retain`: If the PersistentVolumeClaim is deleted, the PersistentVolume and its associated storage remain
	// +kubebuilder:validation:Enum=Delete;Retain
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	ReclaimPolicy string `json:"reclaimPolicy"`

	// StorageClass binding mode. Possible values:
	//
	// - `Immediate`: Creates a PersistentVolume immediately after the PersistentVolumeClaim is created
	// - `WaitForFirstConsumer`: Creates a PersistentVolume only after a Pod that uses the PersistentVolumeClaim is created
	// +kubebuilder:validation:Enum=Immediate;WaitForFirstConsumer
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	VolumeBindingMode string `json:"volumeBindingMode"`

	// LVM configuration.
	// +kubebuilder:validation:Required
	LVM *LocalStorageClassLVMSpec `json:"lvm,omitempty"`

	// StorageClass file system type.
	// +optional
	// +kubebuilder:validation:Enum=ext4;xfs
	// +kubebuilder:default=ext4
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	FSType string `json:"fsType,omitempty"`
}

// +kubebuilder:validation:XValidation:rule=`(self.type == "Thin" && !has(self.thick)) || self.type != "Thin"`,message="Field spec.lvm.thick is forbidden for Thin type."
// +kubebuilder:validation:XValidation:rule=`(!has(self.thick) || !has(self.thick.contiguous) || (has(self.thick.contiguous) && self.thick.contiguous == oldSelf.thick.contiguous))`,message="Field 'contiguous' is immutable and cannot be added if not specified at creation."
// +kubebuilder:validation:XValidation:rule=`(!has(oldSelf.thick) || has(self.thick) || !has(oldSelf.thick.contiguous))`,message="Field 'thick' cannot be removed if 'thick.contiguous' is specified."
// +kubebuilder:validation:XValidation:rule=`self.lvmVolumeGroups.all(g, has(g.name) != has(g.labelSelector))`,message="Each spec.lvm.lvmVolumeGroups entry must set exactly one of name or labelSelector."
// +kubebuilder:validation:XValidation:rule=`self.lvmVolumeGroups.all(g, has(g.name)) || self.lvmVolumeGroups.all(g, has(g.labelSelector))`,message="spec.lvm.lvmVolumeGroups must use either name entries or labelSelector entries, not a mix."
// +kubebuilder:validation:XValidation:rule=`self.type != "Thin" || self.lvmVolumeGroups.all(g, has(g.thin) && has(g.thin.poolName) && size(g.thin.poolName) > 0)`,message="Field thin.poolName is required for every spec.lvm.lvmVolumeGroups entry when type is Thin."
// +kubebuilder:validation:XValidation:rule=`self.type != "Thick" || self.lvmVolumeGroups.all(g, !has(g.thin))`,message="Field thin is forbidden for spec.lvm.lvmVolumeGroups entries when type is Thick."
type LocalStorageClassLVMSpec struct {
	// Provisioning type of the LVM Logical Volumes (LV) created for PersistentVolumes (PV).
	// +kubebuilder:validation:Enum=Thick;Thin
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	Type string `json:"type"`

	// Settings for Logical Volumes in a LVMVolumeGroup resource.
	// +optional
	// +kubebuilder:validation:XValidation:rule=`(!has(oldSelf.contiguous) || has(self.contiguous))`,message="Field 'contiguous' cannot be removed."
	// +kubebuilder:validation:XValidation:rule=`(!has(self.contiguous) || (has(self.contiguous) && self.contiguous == oldSelf.contiguous))`,message="Field 'contiguous' is immutable and cannot be added if not specified at creation."
	Thick *LocalStorageClassLVMThickSpec `json:"thick,omitempty"`

	// Volume cleanup method after the PersistentVolume is deleted.
	// If this parameter is not set, there's no guarantee that the data will be completely deleted.
	//
	// Allowed values:
	//
	// - `RandomFillSinglePass`: The volume will be overwritten with random data once before deletion. This option is not recommended for solid-state drives (SSDs), as overwriting reduces the drive's lifespan.
	// - `RandomFillThreePass`: The volume will be overwritten with random data three times before deletion. This option is also not recommended for SSDs, as overwriting reduces the drive's lifespan.
	// - `Discard`: Before deletion, all blocks of the volume will be marked as free using the `discard` system call. This option is applicable only to SSDs.
	// +optional
	// +kubebuilder:validation:Enum=RandomFillThreePass;RandomFillSinglePass;Discard
	VolumeCleanup string `json:"volumeCleanup,omitempty"`

	// List of entries selecting the LVMVolumeGroup resources where PersistentVolumes will be created.
	//
	// Each entry selects LVMVolumeGroups either by name (`name`) or by labels (`labelSelector`). Exactly one of the two must be set per entry, and the whole list must use one kind consistently — either all `name` entries or all `labelSelector` entries, not a mix. A `labelSelector` entry is resolved by the controller into every matching LVMVolumeGroup and kept in sync as LVMVolumeGroups are added, removed or relabeled. Name-based entries must list each LVMVolumeGroup at most once. Overlapping `labelSelector` entries that resolve to the same LVMVolumeGroup are collapsed when they agree on `thin.poolName` and rejected when they disagree.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	LVMVolumeGroups []LocalStorageClassLVG `json:"lvmVolumeGroups"`
}

// Current state of the StorageClass.
type LocalStorageClassStatus struct {
	// StorageClass current state. Possible values:
	//
	// - `Failed`: The controller received incorrect resource configuration or errors occurred during the operation
	// - `Created`: All operations completed successfully
	// +kubebuilder:validation:Enum=Failed;Created
	Phase string `json:"phase,omitempty"`

	// Additional information about the current state of the StorageClass.
	Reason string `json:"reason,omitempty"`

	// The value of `metadata.generation` the controller has last acted on. When it trails
	// `metadata.generation`, the controller has not yet processed the latest spec, and the
	// conditions below still describe the previous one.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// The latest observations of the resource state. Condition type: `Ready`.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +crd-enricher:unset:items.description
	// +crd-enricher:raw:items.properties.type.description="Condition type. `Ready` is `True` once the StorageClass is in place and the LVMVolumeGroups resolve, and `False` when a reconcile pass failed (`reason: ReconcileFailed`) or the resource is being deleted (`reason: Deleting`). Until the controller's first pass over the resource the condition is absent rather than `Unknown`."
	// +crd-enricher:raw:items.properties.status.description=Current status of the condition.
	// +crd-enricher:raw:items.properties.observedGeneration.description=The value of `metadata.generation` this condition was set against.
	// +crd-enricher:raw:items.properties.lastTransitionTime.description="Timestamp of the last status transition for this condition. It is not bumped\nwhen only the reason or the message changes."
	// +crd-enricher:raw:items.properties.reason.description="Machine-readable reason for the current status, suitable for log filtering\nand alerting."
	// +crd-enricher:raw:items.properties.message.description=Human-readable explanation of the current status.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:validation:XValidation:rule=`has(self.name) != has(self.labelSelector)`,message="Exactly one of name or labelSelector must be set."
type LocalStorageClassLVG struct {
	// LVMVolumeGroup resource name. Mutually exclusive with `labelSelector`.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`

	// The selector subtree is a vendored metav1.LabelSelector, so every description
	// under it comes from apimachinery unless overridden here. The four overrides
	// below restore, verbatim, the text the hand-written manifest carried: the
	// upstream wording opens each sentence with the name of the Go field and adds
	// "This array is replaced during a strategic merge patch", which means nothing
	// for a field of a CRD. items.description has no curated original -- the
	// hand-written manifest had no such key -- and cannot be removed with raw:, so
	// it is pinned to our own sentence and translated in doc-ru.
	//
	// Kept detached from the doc comment below by a blank line on purpose: plain
	// text touching the field is its doc comment, and controller-gen renders that
	// into the CRD description.

	// Label selector that selects one or more LVMVolumeGroup resources by their labels. Mutually exclusive with `name`.
	// +optional
	// +crd-enricher:raw:properties.matchLabels.description="Map of {key,value} pairs. A single {key,value} in the matchLabels map is equivalent to an element of matchExpressions, whose key field is \"key\", the operator is \"In\", and the values array contains only \"value\"."
	// +crd-enricher:raw:properties.matchExpressions.description=List of label selector requirements. The requirements are ANDed.
	// +crd-enricher:raw:properties.matchExpressions.items.description=A label selector requirement -- a key, an operator and, for some operators, a list of values.
	// +crd-enricher:raw:properties.matchExpressions.items.properties.operator.description=operator represents a key's relationship to a set of values. Valid operators are In, NotIn, Exists and DoesNotExist.
	// +crd-enricher:raw:properties.matchExpressions.items.properties.values.description=values is an array of string values. If the operator is In or NotIn, the values array must be non-empty. If the operator is Exists or DoesNotExist, the values array must be empty.
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// Thin pool used for the LVMVolumeGroups selected by this entry. Required when `type` is `Thin`, forbidden when `type` is `Thick`.
	// +optional
	Thin *LocalStorageClassLVMThinPoolSpec `json:"thin,omitempty"`
}

type LocalStorageClassLVMThinPoolSpec struct {
	// Thin pool name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^.*$`
	PoolName string `json:"poolName"`
}

type LocalStorageClassLVMThickSpec struct {
	// If `true`, the Logical Volume will be created with the contiguous flag.
	//
	// > Note: This flag should be used with caution because it may lead to poor scheduling of pods using the PersistentVolume. Our scheduler checks the free space in VG and selects nodes with the most free space for pods. However, it cannot determine the maximum amount of sequential free space available. Consequently, there may be situations where a pod is scheduled to a node, but the volume cannot be created due to insufficient contiguous free space. If such a situation arises, manual intervention will be required.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	Contiguous *bool `json:"contiguous,omitempty"`
}
