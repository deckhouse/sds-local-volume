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
	"math"
	"slices"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
	"gopkg.in/yaml.v2"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sds-local-volume-csi/internal"
	"sds-local-volume-csi/pkg/logger"
)

const (
	LLVStatusCreated            = "Created"
	LLVStatusFailed             = "Failed"
	LLVTypeThin                 = "Thin"
	KubernetesAPIRequestLimit   = 3
	KubernetesAPIRequestTimeout = 1
	SDSLocalVolumeCSIFinalizer  = "storage.deckhouse.io/sds-local-volume-csi"
)

func CreateLVMLogicalVolume(ctx context.Context, kc client.Client, log *logger.Logger, traceID, name string, lvmLogicalVolumeSpec snc.LVMLogicalVolumeSpec) (*snc.LVMLogicalVolume, error) {
	var err error
	llv := &snc.LVMLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			OwnerReferences: []metav1.OwnerReference{},
			Finalizers:      []string{SDSLocalVolumeCSIFinalizer},
		},
		Spec: lvmLogicalVolumeSpec,
	}

	log.Trace(fmt.Sprintf("[CreateLVMLogicalVolume][traceID:%s][volumeID:%s] LVMLogicalVolume: %+v", traceID, name, llv))

	err = kc.Create(ctx, llv)
	return llv, err
}

func DeleteLVMLogicalVolume(ctx context.Context, kc client.Client, log *logger.Logger, traceID, lvmLogicalVolumeName string) error {
	var err error

	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] Trying to find LVMLogicalVolume", traceID, lvmLogicalVolumeName))
	llv, err := GetLVMLogicalVolume(ctx, kc, lvmLogicalVolumeName, "")
	if err != nil {
		return fmt.Errorf("get LVMLogicalVolume %s: %w", lvmLogicalVolumeName, err)
	}

	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] LVMLogicalVolume found: %+v (status: %+v)", traceID, lvmLogicalVolumeName, llv, llv.Status))
	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] Removing finalizer %s if exists", traceID, lvmLogicalVolumeName, SDSLocalVolumeCSIFinalizer))

	removed, err := removeLLVFinalizerIfExist(ctx, kc, llv, SDSLocalVolumeCSIFinalizer)
	if err != nil {
		return fmt.Errorf("remove finalizers from LVMLogicalVolume %s: %w", lvmLogicalVolumeName, err)
	}
	if removed {
		log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] finalizer %s removed from LVMLogicalVolume %s", traceID, lvmLogicalVolumeName, SDSLocalVolumeCSIFinalizer, lvmLogicalVolumeName))
	} else {
		log.Warning(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] finalizer %s not found in LVMLogicalVolume %s", traceID, lvmLogicalVolumeName, SDSLocalVolumeCSIFinalizer, lvmLogicalVolumeName))
	}

	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] Trying to delete LVMLogicalVolume", traceID, lvmLogicalVolumeName))
	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] Attempt: %d", traceID, lvmLogicalVolumeName, attempt))
		err = kc.Delete(ctx, llv)
		if err == nil {
			log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] LVMLogicalVolume deleted", traceID, lvmLogicalVolumeName))
			return nil
		}
		log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] error: %s. Retry after %d seconds", traceID, lvmLogicalVolumeName, err.Error(), KubernetesAPIRequestTimeout))
		time.Sleep(KubernetesAPIRequestTimeout * time.Second)
	}

	if err != nil {
		return fmt.Errorf("after %d attempts of deleting LVMLogicalVolume %s, last error: %w", KubernetesAPIRequestLimit, lvmLogicalVolumeName, err)
	}
	return nil
}

func WaitForStatusUpdate(ctx context.Context, kc client.Client, log *logger.Logger, traceID, lvmLogicalVolumeName, namespace string, llvSize, delta resource.Quantity) (int, error) {
	var attemptCounter int
	sizeEquals := false
	log.Info(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Waiting for LVM Logical Volume status update", traceID, lvmLogicalVolumeName))
	for {
		attemptCounter++
		select {
		case <-ctx.Done():
			log.Warning(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] context done. Failed to wait for LVM Logical Volume status update", traceID, lvmLogicalVolumeName))
			return attemptCounter, ctx.Err()
		default:
			time.Sleep(500 * time.Millisecond)
		}

		llv, err := GetLVMLogicalVolume(ctx, kc, lvmLogicalVolumeName, namespace)
		if err != nil {
			return attemptCounter, err
		}

		if attemptCounter%10 == 0 {
			log.Info(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Attempt: %d,LVM Logical Volume: %+v; delta=%s; sizeEquals=%t", traceID, lvmLogicalVolumeName, attemptCounter, llv, delta.String(), sizeEquals))
		}

		if llv.Status != nil {
			log.Trace(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Attempt %d, LVM Logical Volume status: %+v, full LVMLogicalVolume resource: %+v", traceID, lvmLogicalVolumeName, attemptCounter, llv.Status, llv))
			sizeEquals = AreSizesEqualWithinDelta(llvSize, llv.Status.ActualSize, delta)

			if llv.Status.Phase == LLVStatusFailed {
				return attemptCounter, fmt.Errorf("failed to create LVM logical volume on node for LVMLogicalVolume %s, reason: %s", lvmLogicalVolumeName, llv.Status.Reason)
			}

			if llv.DeletionTimestamp != nil {
				return attemptCounter, fmt.Errorf("failed to create LVM logical volume on node for LVMLogicalVolume %s, reason: LVMLogicalVolume is being deleted", lvmLogicalVolumeName)
			}

			if llv.Status.Phase == LLVStatusCreated && sizeEquals {
				return attemptCounter, nil
			}
			log.Trace(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Attempt %d, LVM Logical Volume status does not have the required fields yet. Waiting...", traceID, lvmLogicalVolumeName, attemptCounter))
		}
	}
}

func GetLVMLogicalVolume(ctx context.Context, kc client.Client, lvmLogicalVolumeName, namespace string) (*snc.LVMLogicalVolume, error) {
	var llv snc.LVMLogicalVolume
	var err error

	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		err = kc.Get(ctx, client.ObjectKey{
			Name:      lvmLogicalVolumeName,
			Namespace: namespace,
		}, &llv)

		if err == nil {
			return &llv, nil
		}
		time.Sleep(KubernetesAPIRequestTimeout * time.Second)
	}
	return nil, fmt.Errorf("after %d attempts of getting LVMLogicalVolume %s in namespace %s, last error: %w", KubernetesAPIRequestLimit, lvmLogicalVolumeName, namespace, err)
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
func GetLVMVolumeGroupParams(ctx context.Context, kc client.Client, log logger.Logger, lvmVG map[string]string, nodeName, lvmType string) (lvgName, vgName string, err error) {
	listLvgs := &snc.LvmVolumeGroupList{
		ListMeta: metav1.ListMeta{},
		Items:    []snc.LvmVolumeGroup{},
	}

	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		err = kc.List(ctx, listLvgs)
		if err == nil {
			break
		}
		time.Sleep(KubernetesAPIRequestTimeout * time.Second)
	}
	if err != nil {
		return "", "", fmt.Errorf("after %d attempts of getting LvmVolumeGroups, last error: %w", KubernetesAPIRequestLimit, err)
	}

	for _, lvg := range listLvgs.Items {
		log.Trace(fmt.Sprintf("[GetLVMVolumeGroupParams] process lvg: %+v", lvg))

		_, ok := lvmVG[lvg.Name]
		if ok {
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] found lvg from storage class: %s", lvg.Name))
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] lvg.Status.Nodes[0].Name: %s, prefferedNode: %s", lvg.Status.Nodes[0].Name, nodeName))
			if lvg.Status.Nodes[0].Name == nodeName {
				if lvmType == LLVTypeThin {
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

	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		err = kc.Get(ctx, client.ObjectKey{
			Name:      lvgName,
			Namespace: namespace,
		}, &lvg)

		if err == nil {
			return &lvg, nil
		}
		time.Sleep(KubernetesAPIRequestTimeout * time.Second)
	}
	return nil, fmt.Errorf("after %d attempts of getting LvmVolumeGroup %s in namespace %s, last error: %w", KubernetesAPIRequestLimit, lvgName, namespace, err)
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

func ExpandLVMLogicalVolume(ctx context.Context, kc client.Client, llv *snc.LVMLogicalVolume, newSize string) error {
	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		llv.Spec.Size = newSize
		err := kc.Update(ctx, llv)
		if err == nil {
			return nil
		}
		if !kerrors.IsConflict(err) {
			return fmt.Errorf("[ExpandLVMLogicalVolume] error updating LVMLogicalVolume %s: %w", llv.Name, err)
		}

		freshLLV, getErr := GetLVMLogicalVolume(ctx, kc, llv.Name, "")
		if getErr != nil {
			return fmt.Errorf("[ExpandLVMLogicalVolume] error getting LVMLogicalVolume %s after update conflict: %w", llv.Name, getErr)
		}
		llv = freshLLV

		if attempt < KubernetesAPIRequestLimit-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				time.Sleep(KubernetesAPIRequestTimeout * time.Second)
			}
		}
	}

	return fmt.Errorf("after %d attempts of expanding LVMLogicalVolume %s, last error: %w", KubernetesAPIRequestLimit, llv.Name, nil)
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
	log.Info(fmt.Sprintf("[GetStorageClassLVGs] StorageClass LVM volume groups parameters map: %+v", storageClassLVGParametersMap))

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
	listLvgs := &snc.LvmVolumeGroupList{}

	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		err = kc.List(ctx, listLvgs)
		if err == nil {
			return listLvgs, nil
		}
		time.Sleep(KubernetesAPIRequestTimeout * time.Second)
	}

	return nil, fmt.Errorf("after %d attempts of getting LvmVolumeGroupList, last error: %w", KubernetesAPIRequestLimit, err)
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
	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		removed := false
		for i, val := range llv.Finalizers {
			if val == finalizer {
				llv.Finalizers = slices.Delete(llv.Finalizers, i, i+1)
				removed = true
				break
			}
		}

		if !removed {
			return false, nil
		}

		err := kc.Update(ctx, llv)
		if err == nil {
			return true, nil
		}

		if !kerrors.IsConflict(err) {
			return false, fmt.Errorf("[removeLLVFinalizerIfExist] error updating LVMLogicalVolume %s: %w", llv.Name, err)
		}

		if attempt < KubernetesAPIRequestLimit-1 {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			default:
				time.Sleep(KubernetesAPIRequestTimeout * time.Second)
			}
		}
	}

	return false, fmt.Errorf("after %d attempts of removing finalizer %s from LVMLogicalVolume %s, last error: %w", KubernetesAPIRequestLimit, finalizer, llv.Name, nil)
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
