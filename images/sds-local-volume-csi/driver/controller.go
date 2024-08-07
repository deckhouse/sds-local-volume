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

package driver

import (
	"context"
	"errors"
	"fmt"
	"sds-local-volume-csi/internal"
	"sds-local-volume-csi/pkg/utils"

	kerrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/resource"
)

func (d *Driver) CreateVolume(ctx context.Context, request *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	d.log.Info("method CreateVolume")

	d.log.Trace("========== CreateVolume ============")
	d.log.Trace(request.String())
	d.log.Trace("========== CreateVolume ============")

	if request.GetParameters()[internal.TypeKey] != internal.Lvm {
		return nil, status.Error(codes.InvalidArgument, "Unsupported Storage Class type")
	}

	if len(request.Name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume Name cannot be empty")
	}
	if request.VolumeCapabilities == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capability cannot de empty")
	}

	BindingMode := request.GetParameters()[internal.BindingModeKey]
	d.log.Info(fmt.Sprintf("storage class BindingMode: %s", BindingMode))

	LvmType := request.GetParameters()[internal.LvmTypeKey]
	d.log.Info(fmt.Sprintf("storage class LvmType: %s", LvmType))

	if len(request.GetParameters()[internal.LvmVolumeGroupKey]) == 0 {
		err := errors.New("no LVMVolumeGroups specified in a storage class's parameters")
		d.log.Error(err, fmt.Sprintf("no LVMVolumeGroups were found for the request: %+v", request))
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	storageClassLVGs, storageClassLVGParametersMap, err := utils.GetStorageClassLVGsAndParameters(ctx, d.cl, d.log, request.GetParameters()[internal.LvmVolumeGroupKey])
	if err != nil {
		d.log.Error(err, "error GetStorageClassLVGs")
		return nil, status.Errorf(codes.Internal, err.Error())
	}

	contiguous := utils.IsContiguous(request, LvmType)
	d.log.Info(fmt.Sprintf("contiguous: %t", contiguous))

	// TODO: Consider refactoring the naming strategy for llvName and lvName.
	// Currently, we use the same name for llvName (the name of the LVMLogicalVolume resource in Kubernetes)
	// and lvName (the name of the LV in LVM on the node) because the PV name is unique within the cluster,
	// preventing name collisions. This approach simplifies matching between nodes and Kubernetes by maintaining
	// the same name in both contexts. Future consideration should be given to optimizing this logic to enhance
	// code readability and maintainability.
	llvName := request.Name
	lvName := request.Name
	d.log.Info(fmt.Sprintf("llv name: %s ", llvName))

	llvSize := resource.NewQuantity(request.CapacityRange.GetRequiredBytes(), resource.BinarySI)
	d.log.Info(fmt.Sprintf("llv size: %s ", llvSize.String()))

	var preferredNode string
	switch BindingMode {
	case internal.BindingModeI:
		d.log.Info(fmt.Sprintf("BindingMode is %s. Start selecting node", internal.BindingModeI))
		selectedNodeName, freeSpace, err := utils.GetNodeWithMaxFreeSpace(storageClassLVGs, storageClassLVGParametersMap, LvmType)
		if err != nil {
			d.log.Error(err, "error GetNodeMaxVGSize")
		}

		preferredNode = selectedNodeName
		d.log.Info(fmt.Sprintf("Selected node: %s, free space %s ", selectedNodeName, freeSpace.String()))
		if LvmType == internal.LVMTypeThick {
			if llvSize.Value() > freeSpace.Value() {
				return nil, status.Errorf(codes.Internal, "requested size: %s is greater than free space: %s", llvSize.String(), freeSpace.String())
			}
		}
	case internal.BindingModeWFFC:
		d.log.Info(fmt.Sprintf("BindingMode is %s. Get preferredNode", internal.BindingModeWFFC))
		if len(request.AccessibilityRequirements.Preferred) != 0 {
			t := request.AccessibilityRequirements.Preferred[0].Segments
			preferredNode = t[internal.TopologyKey]
		}
	}

	selectedLVG, err := utils.SelectLVG(storageClassLVGs, preferredNode)
	if err != nil {
		d.log.Error(err, "error SelectLVG")
		return nil, status.Errorf(codes.Internal, err.Error())
	}

	llvSpec := utils.GetLLVSpec(d.log, lvName, selectedLVG, storageClassLVGParametersMap, LvmType, *llvSize, contiguous)
	d.log.Info(fmt.Sprintf("LVMLogicalVolumeSpec : %+v", llvSpec))
	resizeDelta, err := resource.ParseQuantity(internal.ResizeDelta)
	if err != nil {
		d.log.Error(err, "error ParseQuantity for ResizeDelta")
		return nil, err
	}

	d.log.Trace("------------ CreateLVMLogicalVolume start ------------")
	_, err = utils.CreateLVMLogicalVolume(ctx, d.cl, llvName, llvSpec)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			d.log.Info(fmt.Sprintf("LVMLogicalVolume %s already exists", llvName))
		} else {
			d.log.Error(err, "error CreateLVMLogicalVolume")
			return nil, err
		}
	}
	d.log.Trace("------------ CreateLVMLogicalVolume end ------------")

	d.log.Trace("start wait CreateLVMLogicalVolume ")

	attemptCounter, err := utils.WaitForStatusUpdate(ctx, d.cl, d.log, request.Name, "", *llvSize, resizeDelta)
	if err != nil {
		deleteErr := utils.DeleteLVMLogicalVolume(ctx, d.cl, d.log, request.Name)

		d.log.Error(err, fmt.Sprintf("error WaitForStatusUpdate. Delete LVMLogicalVolume %s", request.Name))
		if deleteErr != nil {
			d.log.Error(deleteErr, "error DeleteLVMLogicalVolume")
		}
		return nil, err
	}
	d.log.Trace(fmt.Sprintf("stop waiting CreateLVMLogicalVolume, attempt counter = %d ", attemptCounter))

	volumeCtx := make(map[string]string, len(request.Parameters))
	for k, v := range request.Parameters {
		volumeCtx[k] = v
	}

	volumeCtx[internal.SubPath] = request.Name
	volumeCtx[internal.VGNameKey] = selectedLVG.Spec.ActualVGNameOnTheNode
	if llvSpec.Type == internal.LVMTypeThin {
		volumeCtx[internal.ThinPoolNameKey] = llvSpec.Thin.PoolName
	} else {
		volumeCtx[internal.ThinPoolNameKey] = ""
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes: request.CapacityRange.GetRequiredBytes(),
			VolumeId:      request.Name,
			VolumeContext: volumeCtx,
			ContentSource: request.VolumeContentSource,
			AccessibleTopology: []*csi.Topology{
				{Segments: map[string]string{
					internal.TopologyKey: preferredNode,
				}},
			},
		},
	}, nil
}

func (d *Driver) DeleteVolume(ctx context.Context, request *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	d.log.Info("method DeleteVolume")
	err := utils.DeleteLVMLogicalVolume(ctx, d.cl, d.log, request.VolumeId)
	if err != nil {
		d.log.Error(err, "error DeleteLVMLogicalVolume")
	}
	d.log.Info(fmt.Sprintf("delete volume %s", request.VolumeId))
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) ControllerPublishVolume(ctx context.Context, request *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	d.log.Info("method ControllerPublishVolume")
	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			d.publishInfoVolumeName: request.VolumeId,
		},
	}, nil
}

func (d *Driver) ControllerUnpublishVolume(ctx context.Context, request *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	d.log.Info("method ControllerUnpublishVolume")
	// todo called Immediate
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, request *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	d.log.Info("call method ValidateVolumeCapabilities")
	return nil, nil
}

func (d *Driver) ListVolumes(ctx context.Context, request *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	d.log.Info("call method ListVolumes")
	return nil, nil
}

func (d *Driver) GetCapacity(ctx context.Context, request *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	d.log.Info("method GetCapacity")

	//todo MaxSize one PV
	//todo call volumeBindingMode: WaitForFirstConsumer

	return &csi.GetCapacityResponse{
		AvailableCapacity: 1000000,
		MaximumVolumeSize: nil,
		MinimumVolumeSize: nil,
	}, nil
}

func (d *Driver) ControllerGetCapabilities(ctx context.Context, request *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	d.log.Info("method ControllerGetCapabilities")
	capabilities := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
		csi.ControllerServiceCapability_RPC_GET_CAPACITY,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	}

	csiCaps := make([]*csi.ControllerServiceCapability, len(capabilities))
	for i, capability := range capabilities {
		csiCaps[i] = &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: capability,
				},
			},
		}
	}

	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: csiCaps,
	}, nil
}

func (d *Driver) CreateSnapshot(ctx context.Context, request *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	d.log.Info(" call method CreateSnapshot")
	return nil, nil
}

func (d *Driver) DeleteSnapshot(ctx context.Context, request *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	d.log.Info(" call method DeleteSnapshot")
	return nil, nil
}

func (d *Driver) ListSnapshots(ctx context.Context, request *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	d.log.Info(" call method ListSnapshots")
	return nil, nil
}

func (d *Driver) ControllerExpandVolume(ctx context.Context, request *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	d.log.Info("method ControllerExpandVolume")

	d.log.Trace("========== ControllerExpandVolume ============")
	d.log.Trace(request.String())
	d.log.Trace("========== ControllerExpandVolume ============")

	volumeID := request.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume id cannot be empty")
	}

	llv, err := utils.GetLVMLogicalVolume(ctx, d.cl, volumeID, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error getting LVMLogicalVolume: %s", err.Error())
	}

	resizeDelta, err := resource.ParseQuantity(internal.ResizeDelta)
	if err != nil {
		d.log.Error(err, "error ParseQuantity for ResizeDelta")
		return nil, err
	}
	d.log.Trace(fmt.Sprintf("resizeDelta: %s", resizeDelta.String()))
	requestCapacity := resource.NewQuantity(request.CapacityRange.GetRequiredBytes(), resource.BinarySI)
	d.log.Trace(fmt.Sprintf("requestCapacity: %s", requestCapacity.String()))

	nodeExpansionRequired := true
	if request.GetVolumeCapability().GetBlock() != nil {
		nodeExpansionRequired = false
	}
	d.log.Info(fmt.Sprintf("NodeExpansionRequired: %t", nodeExpansionRequired))

	if llv.Status.ActualSize.Value() > requestCapacity.Value()+resizeDelta.Value() || utils.AreSizesEqualWithinDelta(*requestCapacity, llv.Status.ActualSize, resizeDelta) {
		d.log.Warning(fmt.Sprintf("requested size is less than or equal to the actual size of the volume include delta %s , no need to resize LVMLogicalVolume %s, requested size: %s, actual size: %s, return NodeExpansionRequired: %t and CapacityBytes: %d", resizeDelta.String(), volumeID, requestCapacity.String(), llv.Status.ActualSize.String(), nodeExpansionRequired, llv.Status.ActualSize.Value()))
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         llv.Status.ActualSize.Value(),
			NodeExpansionRequired: nodeExpansionRequired,
		}, nil
	}

	lvg, err := utils.GetLVMVolumeGroup(ctx, d.cl, llv.Spec.LvmVolumeGroupName, llv.Namespace)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error getting LVMVolumeGroup: %v", err)
	}

	if llv.Spec.Type == internal.LVMTypeThick {
		lvgFreeSpace := utils.GetLVMVolumeGroupFreeSpace(*lvg)

		if lvgFreeSpace.Value() < (requestCapacity.Value() - llv.Status.ActualSize.Value()) {
			return nil, status.Errorf(codes.Internal, "requested size: %s is greater than the capacity of the LVMVolumeGroup: %s", requestCapacity.String(), lvgFreeSpace.String())
		}
	}

	d.log.Info("start resize LVMLogicalVolume")
	d.log.Info(fmt.Sprintf("requested size: %s, actual size: %s", requestCapacity.String(), llv.Status.ActualSize.String()))
	llv.Spec.Size = requestCapacity.String()
	err = utils.UpdateLVMLogicalVolume(ctx, d.cl, llv)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error updating LVMLogicalVolume: %v", err)
	}

	attemptCounter, err := utils.WaitForStatusUpdate(ctx, d.cl, d.log, llv.Name, llv.Namespace, *requestCapacity, resizeDelta)
	if err != nil {
		d.log.Error(err, "error WaitForStatusUpdate")
		return nil, err
	}
	d.log.Info(fmt.Sprintf("finish resize LVMLogicalVolume, attempt counter = %d ", attemptCounter))

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         request.CapacityRange.RequiredBytes,
		NodeExpansionRequired: nodeExpansionRequired,
	}, nil
}

func (d *Driver) ControllerGetVolume(ctx context.Context, request *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	d.log.Info(" call method ControllerGetVolume")
	return &csi.ControllerGetVolumeResponse{}, nil
}

func (d *Driver) ControllerModifyVolume(ctx context.Context, request *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	d.log.Info(" call method ControllerModifyVolume")
	return &csi.ControllerModifyVolumeResponse{}, nil
}
