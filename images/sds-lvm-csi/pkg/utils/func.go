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

package utils

import (
	"context"
	"errors"
	"fmt"
	"sds-lvm-csi/api/v1alpha1"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LLVStatusCreated = "Created"
	LLVTypeThin      = "Thin"
)

func NodeWithMaxSize(vgNameAndSize map[string]int64) (vgName string, maxSize int64) {

	for k, n := range vgNameAndSize {
		if n > maxSize {
			maxSize = n
			vgName = k
		}
	}
	return vgName, maxSize
}

func CreateLVMLogicalVolume(ctx context.Context, kc client.Client, name string, LvmLogicalVolumeSpec v1alpha1.LvmLogicalVolumeSpec) (error, *v1alpha1.LvmLogicalVolume) {
	llv := &v1alpha1.LvmLogicalVolume{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMLogicalVolumeKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			OwnerReferences: []metav1.OwnerReference{},
		},
		Spec: LvmLogicalVolumeSpec,
	}

	err := kc.Create(ctx, llv)
	if err != nil {
		return err, nil
	}
	return nil, llv
}

func DeleteLVMLogicalVolume(ctx context.Context, kc client.Client, LvmLogicalVolumeName string) error {
	llv := &v1alpha1.LvmLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: LvmLogicalVolumeName,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMLogicalVolumeKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
	}
	err := kc.Delete(ctx, llv)
	if err != nil {
		return err
	}
	return nil
}

func WaitForStatusUpdate(ctx context.Context, kc client.Client, LvmLogicalVolumeName, namespace string) (int, error) {
	var newLV v1alpha1.LvmLogicalVolume
	var attemptCounter int
	for {
		attemptCounter++
		select {
		case <-ctx.Done():
			return attemptCounter, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		err := kc.Get(ctx, client.ObjectKey{
			Name:      LvmLogicalVolumeName,
			Namespace: namespace,
		}, &newLV)
		if err != nil {
			return attemptCounter, err
		}

		if newLV.Status != nil && newLV.Status.Phase == LLVStatusCreated {
			return attemptCounter, nil
		}
	}
}

func GetNodeMaxVGSize(ctx context.Context, kc client.Client) (nodeName string, freeSpace string, err error) {
	listLvgs := &v1alpha1.LvmVolumeGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMVolumeGroupKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
		ListMeta: metav1.ListMeta{},
		Items:    []v1alpha1.LvmVolumeGroup{},
	}
	err = kc.List(ctx, listLvgs)
	if err != nil {
		return "", "", err
	}

	nodesVGSize := make(map[string]int64)
	vgNameNodeName := make(map[string]string)
	for _, lvg := range listLvgs.Items {
		obj := &v1alpha1.LvmVolumeGroup{}
		err = kc.Get(ctx, client.ObjectKey{
			Name:      lvg.Name,
			Namespace: lvg.Namespace,
		}, obj)
		if err != nil {
			return "", "", errors.New(fmt.Sprintf("get lvg name: %s", lvg.Name))
		}

		vgSize, err := resource.ParseQuantity(lvg.Status.VGSize)
		if err != nil {
			return "", "", errors.New("parse size vgSize")
		}
		nodesVGSize[lvg.Name] = vgSize.Value()
		vgNameNodeName[lvg.Name] = lvg.Status.Nodes[0].Name
	}

	VGNameWihMaxFreeSpace, _ := NodeWithMaxSize(nodesVGSize)
	fs := resource.NewQuantity(nodesVGSize[VGNameWihMaxFreeSpace], resource.BinarySI)
	freeSpace = fs.String()
	nodeName = vgNameNodeName[VGNameWihMaxFreeSpace]

	return nodeName, freeSpace, nil
}

func GetVGName(ctx context.Context, kc client.Client, lvmVG map[string]string, nodeName, LvmType string) (lvgName, vgName string, err error) {
	listLvgs := &v1alpha1.LvmVolumeGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMVolumeGroupKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
		ListMeta: metav1.ListMeta{},
		Items:    []v1alpha1.LvmVolumeGroup{},
	}
	err = kc.List(ctx, listLvgs)
	if err != nil {
		return "", "", err
	}

	for _, lvg := range listLvgs.Items {
		_, ok := lvmVG[lvg.Spec.ActualVGNameOnTheNode]
		if ok && lvg.Status.Nodes[0].Name == nodeName {
			if LvmType == LLVTypeThin {
				for _, thinPool := range lvg.Status.ThinPools {
					for _, tp := range lvmVG {
						if thinPool.Name == tp {
							return lvg.Name, lvg.Spec.ActualVGNameOnTheNode, nil
						}
					}
				}
			}
			return lvg.Name, lvg.Spec.ActualVGNameOnTheNode, nil
		}
	}
	return "", "", errors.New("there are no matches")
}
