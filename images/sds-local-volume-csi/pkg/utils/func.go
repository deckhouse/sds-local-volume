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
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
	"math"
	"sds-local-volume-csi/internal"
	"sds-local-volume-csi/pkg/logger"
	"slices"
	"time"

	"gopkg.in/yaml.v2"
	kerrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LLVStatusCreated            = "Created"
	LLVStatusFailed             = "Failed"
	LLVTypeThin                 = "Thin"
	KubernetesApiRequestLimit   = 3
	KubernetesApiRequestTimeout = 1
	SDSLocalVolumeCSIFinalizer  = "storage.deckhouse.io/sds-local-volume-csi"
)

func CreateLVMLogicalVolume(ctx context.Context, kc client.Client, name string, LVMLogicalVolumeSpec snc.LVMLogicalVolumeSpec) (*snc.LVMLogicalVolume, error) {
	var err error
	llv := &snc.LVMLogicalVolume{
		TypeMeta: metav1.TypeMeta{
			Kind:       snc.LVMLogicalVolumeKind,
			APIVersion: snc.TypeMediaAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			OwnerReferences: []metav1.OwnerReference{},
			Finalizers:      []string{SDSLocalVolumeCSIFinalizer},
		},
		Spec: LVMLogicalVolumeSpec,
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Create(ctx, llv)
		if err == nil {
			return llv, nil
		}

		if kerrors.IsAlreadyExists(err) {
			return nil, err
		}

		time.Sleep(KubernetesApiRequestTimeout)
	}

	if err != nil {
		return nil, fmt.Errorf("after %d attempts of creating LVMLogicalVolume %s, last error: %w", KubernetesApiRequestLimit, name, err)
	}
	return llv, nil
}

func DeleteLVMLogicalVolume(ctx context.Context, kc client.Client, log *logger.Logger, LVMLogicalVolumeName string) error {
	var err error

	llv, err := GetLVMLogicalVolume(ctx, kc, LVMLogicalVolumeName, "")
	if err != nil {
		return fmt.Errorf("get LVMLogicalVolume %s: %w", LVMLogicalVolumeName, err)
	}

	removed, err := removeLLVFinalizerIfExist(ctx, kc, llv, SDSLocalVolumeCSIFinalizer)
	if err != nil {
		return fmt.Errorf("remove finalizers from LVMLogicalVolume %s: %w", LVMLogicalVolumeName, err)
	}
	if removed {
		log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume] finalizer %s removed from LVMLogicalVolume %s", SDSLocalVolumeCSIFinalizer, LVMLogicalVolumeName))
	} else {
		log.Warning(fmt.Sprintf("[DeleteLVMLogicalVolume] finalizer %s not found in LVMLogicalVolume %s", SDSLocalVolumeCSIFinalizer, LVMLogicalVolumeName))
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Delete(ctx, llv)
		if err == nil {
			return nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}

	if err != nil {
		return fmt.Errorf("after %d attempts of deleting LVMLogicalVolume %s, last error: %w", KubernetesApiRequestLimit, LVMLogicalVolumeName, err)
	}
	return nil
}

func WaitForStatusUpdate(ctx context.Context, kc client.Client, log *logger.Logger, LVMLogicalVolumeName, namespace string, llvSize, delta resource.Quantity) (int, error) {
	var attemptCounter int
	sizeEquals := false
	for {
		attemptCounter++
		select {
		case <-ctx.Done():
			return attemptCounter, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		llv, err := GetLVMLogicalVolume(ctx, kc, LVMLogicalVolumeName, namespace)
		if err != nil {
			return attemptCounter, err
		}

		if attemptCounter%10 == 0 {
			log.Info(fmt.Sprintf("[WaitForStatusUpdate] Attempt: %d,LVM Logical Volume: %+v; delta=%s; sizeEquals=%t", attemptCounter, llv, delta.String(), sizeEquals))
		}

		if llv.Status != nil {
			sizeEquals = AreSizesEqualWithinDelta(llvSize, llv.Status.ActualSize, delta)

			if llv.Status.Phase == LLVStatusFailed {
				return attemptCounter, fmt.Errorf("failed to create LVM logical volume on node for LVMLogicalVolume %s, reason: %s", LVMLogicalVolumeName, llv.Status.Reason)

			}

			if llv.Status.Phase == LLVStatusCreated && sizeEquals {
				return attemptCounter, nil
			}
		}
	}
}

func GetLVMLogicalVolume(ctx context.Context, kc client.Client, LVMLogicalVolumeName, namespace string) (*snc.LVMLogicalVolume, error) {
	var llv snc.LVMLogicalVolume
	var err error

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Get(ctx, client.ObjectKey{
			Name:      LVMLogicalVolumeName,
			Namespace: namespace,
		}, &llv)

		if err == nil {
			return &llv, nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}
	return nil, fmt.Errorf("after %d attempts of getting LVMLogicalVolume %s in namespace %s, last error: %w", KubernetesApiRequestLimit, LVMLogicalVolumeName, namespace, err)
}

func AreSizesEqualWithinDelta(leftSize, rightSize, allowedDelta resource.Quantity) bool {
	leftSizeFloat := float64(leftSize.Value())
	rightSizeFloat := float64(rightSize.Value())

	return math.Abs(leftSizeFloat-rightSizeFloat) < float64(allowedDelta.Value())
}

func GetNodeWithMaxFreeSpace(lvgs []snc.LvmVolumeGroup, storageClassLVGParametersMap map[string]string, lvmType string) (nodeName string, freeSpace resource.Quantity, err error) {
	var maxFreeSpace int64
	for _, lvg := range lvgs {

		switch lvmType {
		case internal.LVMTypeThick:
			freeSpace = lvg.Status.VGFree
		case internal.LVMTypeThin:
			thinPoolName, ok := storageClassLVGParametersMap[lvg.Name]
			if !ok {
				return "", freeSpace, fmt.Errorf("thin pool name for lvg %s not found in storage class parameters: %+v", lvg.Name, storageClassLVGParametersMap)
			}
			freeSpace, err = GetLVMThinPoolFreeSpace(lvg, thinPoolName)
			if err != nil {
				return "", freeSpace, fmt.Errorf("get free space for thin pool %s in lvg %s: %w", thinPoolName, lvg.Name, err)
			}
		}

		if freeSpace.Value() > maxFreeSpace {
			nodeName = lvg.Status.Nodes[0].Name
			maxFreeSpace = freeSpace.Value()
		}
	}

	return nodeName, *resource.NewQuantity(maxFreeSpace, resource.BinarySI), nil
}

// TODO: delete the method below?
func GetLVMVolumeGroupParams(ctx context.Context, kc client.Client, log logger.Logger, lvmVG map[string]string, nodeName, LvmType string) (lvgName, vgName string, err error) {
	listLvgs := &snc.LvmVolumeGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       snc.LVMVolumeGroupKind,
			APIVersion: snc.TypeMediaAPIVersion,
		},
		ListMeta: metav1.ListMeta{},
		Items:    []snc.LvmVolumeGroup{},
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.List(ctx, listLvgs)
		if err == nil {
			break
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}
	if err != nil {
		return "", "", fmt.Errorf("after %d attempts of getting LvmVolumeGroups, last error: %w", KubernetesApiRequestLimit, err)
	}

	for _, lvg := range listLvgs.Items {

		log.Trace(fmt.Sprintf("[GetLVMVolumeGroupParams] process lvg: %+v", lvg))

		_, ok := lvmVG[lvg.Name]
		if ok {
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] found lvg from storage class: %s", lvg.Name))
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] lvg.Status.Nodes[0].Name: %s, prefferedNode: %s", lvg.Status.Nodes[0].Name, nodeName))
			if lvg.Status.Nodes[0].Name == nodeName {
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
		} else {
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] skip lvg: %s", lvg.Name))
		}

	}
	return "", "", errors.New("there are no matches")
}

func GetLVMVolumeGroup(ctx context.Context, kc client.Client, lvgName, namespace string) (*snc.LvmVolumeGroup, error) {
	var lvg snc.LvmVolumeGroup
	var err error

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Get(ctx, client.ObjectKey{
			Name:      lvgName,
			Namespace: namespace,
		}, &lvg)

		if err == nil {
			return &lvg, nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}
	return nil, fmt.Errorf("after %d attempts of getting LvmVolumeGroup %s in namespace %s, last error: %w", KubernetesApiRequestLimit, lvgName, namespace, err)
}

func GetLVMVolumeGroupFreeSpace(lvg snc.LvmVolumeGroup) (vgFreeSpace resource.Quantity) {
	vgFreeSpace = lvg.Status.VGSize
	vgFreeSpace.Sub(lvg.Status.AllocatedSize)
	return vgFreeSpace
}

func GetLVMThinPoolFreeSpace(lvg snc.LvmVolumeGroup, thinPoolName string) (thinPoolFreeSpace resource.Quantity, err error) {
	var storagePoolThinPool *snc.LvmVolumeGroupThinPoolStatus
	for _, thinPool := range lvg.Status.ThinPools {
		if thinPool.Name == thinPoolName {
			storagePoolThinPool = &thinPool
			break
		}
	}

	if storagePoolThinPool == nil {
		return thinPoolFreeSpace, fmt.Errorf("[GetLVMThinPoolFreeSpace] thin pool %s not found in lvg %+v", thinPoolName, lvg)
	}

	return storagePoolThinPool.AvailableSpace, nil
}

func UpdateLVMLogicalVolume(ctx context.Context, kc client.Client, llv *snc.LVMLogicalVolume) error {
	var err error
	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Update(ctx, llv)
		if err == nil {
			return nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}

	if err != nil {
		return fmt.Errorf("after %d attempts of updating LVMLogicalVolume %s, last error: %w", KubernetesApiRequestLimit, llv.Name, err)
	}
	return nil
}

func GetStorageClassLVGsAndParameters(ctx context.Context, kc client.Client, log *logger.Logger, storageClassLVGParametersString string) (storageClassLVGs []snc.LvmVolumeGroup, storageClassLVGParametersMap map[string]string, err error) {
	var storageClassLVGParametersList LVMVolumeGroups
	err = yaml.Unmarshal([]byte(storageClassLVGParametersString), &storageClassLVGParametersList)
	if err != nil {
		log.Error(err, "unmarshal yaml lvmVolumeGroup")
		return nil, nil, err
	}

	storageClassLVGParametersMap = make(map[string]string, len(storageClassLVGParametersList))
	for _, v := range storageClassLVGParametersList {
		storageClassLVGParametersMap[v.Name] = v.Thin.PoolName
	}
	log.Info(fmt.Sprintf("StorageClass LVM volume groups parameters map: %+v", storageClassLVGParametersMap))

	lvgs, err := GetLVGList(ctx, kc)
	if err != nil {
		return nil, nil, err
	}

	for _, lvg := range lvgs.Items {
		log.Trace(fmt.Sprintf("[GetStorageClassLVGs] process lvg: %+v", lvg))

		_, ok := storageClassLVGParametersMap[lvg.Name]
		if ok {
			log.Info(fmt.Sprintf("[GetStorageClassLVGs] found lvg from storage class: %s", lvg.Name))
			log.Info(fmt.Sprintf("[GetStorageClassLVGs] lvg.Status.Nodes[0].Name: %s", lvg.Status.Nodes[0].Name))
			storageClassLVGs = append(storageClassLVGs, lvg)
		} else {
			log.Trace(fmt.Sprintf("[GetStorageClassLVGs] skip lvg: %s", lvg.Name))
		}
	}

	return storageClassLVGs, storageClassLVGParametersMap, nil
}

func GetLVGList(ctx context.Context, kc client.Client) (*snc.LvmVolumeGroupList, error) {
	var err error
	listLvgs := &snc.LvmVolumeGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       snc.LVMVolumeGroupKind,
			APIVersion: snc.TypeMediaAPIVersion,
		},
		ListMeta: metav1.ListMeta{},
		Items:    []snc.LvmVolumeGroup{},
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.List(ctx, listLvgs)
		if err == nil {
			return listLvgs, nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}

	return nil, fmt.Errorf("after %d attempts of getting LvmVolumeGroupList, last error: %w", KubernetesApiRequestLimit, err)
}

func GetLLVSpec(log *logger.Logger, lvName string, selectedLVG snc.LvmVolumeGroup, storageClassLVGParametersMap map[string]string, lvmType string, llvSize resource.Quantity, contiguous bool) snc.LVMLogicalVolumeSpec {
	lvmLogicalVolumeSpec := snc.LVMLogicalVolumeSpec{
		ActualLVNameOnTheNode: lvName,
		Type:                  lvmType,
		Size:                  llvSize.String(),
		LvmVolumeGroupName:    selectedLVG.Name,
	}

	switch lvmType {
	case internal.LVMTypeThin:
		lvmLogicalVolumeSpec.Thin = &snc.LVMLogicalVolumeThinSpec{
			PoolName: storageClassLVGParametersMap[selectedLVG.Name],
		}
		log.Info(fmt.Sprintf("[GetLLVSpec] Thin pool name: %s", lvmLogicalVolumeSpec.Thin.PoolName))
	case internal.LVMTypeThick:
		if contiguous {
			lvmLogicalVolumeSpec.Thick = &snc.LVMLogicalVolumeThickSpec{
				Contiguous: &contiguous,
			}
		}

		log.Info(fmt.Sprintf("[GetLLVSpec] Thick contiguous: %t", contiguous))
	}

	return lvmLogicalVolumeSpec
}

func SelectLVG(storageClassLVGs []snc.LvmVolumeGroup, nodeName string) (snc.LvmVolumeGroup, error) {
	for _, lvg := range storageClassLVGs {
		if lvg.Status.Nodes[0].Name == nodeName {
			return lvg, nil
		}
	}
	return snc.LvmVolumeGroup{}, fmt.Errorf("[SelectLVG] no LVMVolumeGroup found for node %s", nodeName)
}

func removeLLVFinalizerIfExist(ctx context.Context, kc client.Client, llv *snc.LVMLogicalVolume, finalizer string) (bool, error) {
	removed := false

	for i, val := range llv.Finalizers {
		if val == finalizer {
			llv.Finalizers = slices.Delete(llv.Finalizers, i, i+1)
			removed = true
			break
		}
	}

	if removed {
		err := UpdateLVMLogicalVolume(ctx, kc, llv)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func IsContiguous(request *csi.CreateVolumeRequest, lvmType string) bool {
	if lvmType == internal.LVMTypeThin {
		return false
	}

	val, exist := request.Parameters[internal.LVMVThickContiguousParamKey]
	if exist {
		return val == "true"
	}

	return false
}
