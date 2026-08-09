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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// This file is maintained by hand: the package carries no +k8s:deepcopy-gen
// markers and no generator is wired up. Every pointer, slice and map reachable
// from a type below has to be copied explicitly — a plain *out = *in leaves the
// copy sharing memory with the original, which breaks the runtime.Object
// contract that DeepCopyObject returns an independent object.
//
// That is not academic here: the controller-runtime cache hands objects to the
// reconciler through DeepCopyObject, so anything left aliased points straight
// into the informer store, which the watch goroutines read concurrently.
// deepcopy_test.go asserts the independence of both Spec and Status.

// DeepCopyInto is a deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClass) DeepCopyInto(out *LocalStorageClass) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	if in.Status != nil {
		in, out := &in.Status, &out.Status
		*out = new(LocalStorageClassStatus)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopyInto is a deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClassSpec) DeepCopyInto(out *LocalStorageClassSpec) {
	*out = *in
	if in.LVM != nil {
		in, out := &in.LVM, &out.LVM
		*out = new(LocalStorageClassLVMSpec)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopy is a deepcopy function, copying the receiver, creating a new LocalStorageClassSpec.
func (in *LocalStorageClassSpec) DeepCopy() *LocalStorageClassSpec {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClassSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is a deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClassLVMSpec) DeepCopyInto(out *LocalStorageClassLVMSpec) {
	*out = *in
	if in.Thick != nil {
		in, out := &in.Thick, &out.Thick
		*out = new(LocalStorageClassLVMThickSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.LVMVolumeGroups != nil {
		in, out := &in.LVMVolumeGroups, &out.LVMVolumeGroups
		*out = make([]LocalStorageClassLVG, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is a deepcopy function, copying the receiver, creating a new LocalStorageClassLVMSpec.
func (in *LocalStorageClassLVMSpec) DeepCopy() *LocalStorageClassLVMSpec {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClassLVMSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is a deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClassLVG) DeepCopyInto(out *LocalStorageClassLVG) {
	*out = *in
	if in.LabelSelector != nil {
		in, out := &in.LabelSelector, &out.LabelSelector
		*out = new(metav1.LabelSelector)
		(*in).DeepCopyInto(*out)
	}
	if in.Thin != nil {
		in, out := &in.Thin, &out.Thin
		*out = new(LocalStorageClassLVMThinPoolSpec)
		**out = **in
	}
}

// DeepCopy is a deepcopy function, copying the receiver, creating a new LocalStorageClassLVG.
func (in *LocalStorageClassLVG) DeepCopy() *LocalStorageClassLVG {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClassLVG)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is a deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClassLVMThickSpec) DeepCopyInto(out *LocalStorageClassLVMThickSpec) {
	*out = *in
	if in.Contiguous != nil {
		in, out := &in.Contiguous, &out.Contiguous
		*out = new(bool)
		**out = **in
	}
}

// DeepCopy is a deepcopy function, copying the receiver, creating a new LocalStorageClassLVMThickSpec.
func (in *LocalStorageClassLVMThickSpec) DeepCopy() *LocalStorageClassLVMThickSpec {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClassLVMThickSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is a deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClassLVMThinPoolSpec) DeepCopyInto(out *LocalStorageClassLVMThinPoolSpec) {
	*out = *in
}

// DeepCopy is a deepcopy function, copying the receiver, creating a new LocalStorageClassLVMThinPoolSpec.
func (in *LocalStorageClassLVMThinPoolSpec) DeepCopy() *LocalStorageClassLVMThinPoolSpec {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClassLVMThinPoolSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is a deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClassStatus) DeepCopyInto(out *LocalStorageClassStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LocalStorageClassStatus.
func (in *LocalStorageClassStatus) DeepCopy() *LocalStorageClassStatus {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClassStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EmptyBlockDevice.
func (in *LocalStorageClass) DeepCopy() *LocalStorageClass {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClass)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *LocalStorageClass) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalStorageClassList) DeepCopyInto(out *LocalStorageClassList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]LocalStorageClass, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new GuestbookList.
func (in *LocalStorageClassList) DeepCopy() *LocalStorageClassList {
	if in == nil {
		return nil
	}
	out := new(LocalStorageClassList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *LocalStorageClassList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
