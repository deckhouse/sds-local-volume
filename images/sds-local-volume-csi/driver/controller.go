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

package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/utils"
	"github.com/deckhouse/sds-local-volume/lib/go/common/pkg/feature"
	"github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	sourceVolumeKindSnapshot = "LVMLogicalVolumeSnapshot"
	sourceVolumeKindVolume   = "LVMLogicalVolume"
	LVMVolumeCleanupParamKey = "local.csi.storage.deckhouse.io/lvm-volume-cleanup"
)

// alignToLVGExtentOrStatusErr rounds size up to the extent boundary of the given
// LVG. The node always provisions whole extents, so every size that ends up in
// LVMLogicalVolume.Spec.Size or in a response capacity has to go through this
// first, otherwise it drifts from the LV that actually exists.
//
// The returned error is already a gRPC status and is meant to be handed straight
// back to the caller of the RPC.
func alignToLVGExtentOrStatusErr(log logger.Logger, size resource.Quantity, lvg *v1alpha1.LVMVolumeGroup) (resource.Quantity, error) {
	aligned, err := utils.AlignSizeToExtent(size, utils.SafeExtentSize(lvg.Status.ExtentSize))
	if err != nil {
		log.Error("unable to align the size to the LVM extent", logger.Err(err), slog.String("size", size.String()))
		return resource.Quantity{}, status.Errorf(codes.Internal, "error aligning size to extent: %s", err.Error())
	}

	return aligned, nil
}

// fitsFreeSpace reports whether an extent-aligned request fits into the free
// space of the node picked by Immediate binding.
//
// Only thick volumes are checked: a thin volume is over-provisioned by design,
// and with WaitForFirstConsumer the node is chosen by the scheduler, which has
// already accounted for the space.
//
// The check is deliberately performed on the aligned size: a request that fits
// only before rounding would pass here and then fail on the node with a far less
// obvious "not enough space", after CreateVolume had already blocked waiting for
// the LV.
func fitsFreeSpace(bindingMode, lvmType string, alignedSize, freeSpace resource.Quantity) bool {
	if bindingMode != internal.BindingModeI || lvmType != internal.LVMTypeThick {
		return true
	}

	return alignedSize.Value() <= freeSpace.Value()
}

// resolveCapacityBytes returns the capacity to report for a created or resized
// volume: the size that actually exists on the node rather than the size that
// was asked for.
//
// The node provisions whole extents, and on the content-source paths the size
// may have been derived from a source whose Spec.Size predates extent alignment,
// so the requested value is not what the caller ends up with.
//
// llv is expected to carry a status — WaitForStatusUpdate only returns once it
// does — but the fallback keeps a nil status from turning into a panic inside a
// gRPC handler. The second return value reports whether the status was present,
// so callers can log the anomaly.
func resolveCapacityBytes(llv *v1alpha1.LVMLogicalVolume, fallback resource.Quantity) (int64, bool) {
	if llv != nil && llv.Status != nil {
		return llv.Status.ActualSize.Value(), true
	}

	return fallback.Value(), false
}

func (d *Driver) CreateVolume(ctx context.Context, request *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	traceID := uuid.New().String()

	// Named + With replace the "[CreateVolume][traceID:...][volumeID:...]" prefix
	// that used to be interpolated into every message of this RPC.
	log := d.log.Named("CreateVolume").With("traceID", traceID)

	log.Trace("start", "request", request.String())

	if request.Parameters[internal.TypeKey] != internal.Lvm {
		return nil, status.Error(codes.InvalidArgument, "Unsupported Storage Class type")
	}

	if len(request.Name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume Name cannot be empty")
	}
	volumeID := request.Name
	if request.VolumeCapabilities == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capability cannot de empty")
	}

	// volumeID is only known once the request has been validated, so it joins the
	// logger here rather than above.
	log = log.With("volumeID", volumeID)

	BindingMode := request.Parameters[internal.BindingModeKey]
	log.Info("storage class binding mode", "bindingMode", BindingMode)

	LvmType := request.Parameters[internal.LvmTypeKey]
	log.Info("storage class LVM type", "lvmType", LvmType)

	if len(request.Parameters[internal.LVMVolumeGroupKey]) == 0 {
		err := errors.New("no LVMVolumeGroups specified in a storage class's parameters")
		log.Error("no LVMVolumeGroups were found for the request", logger.Err(err), slog.String("request", request.String()))
		return nil, status.Errorf(codes.InvalidArgument, "no LVMVolumeGroups specified in a storage class's parameters")
	}

	storageClassLVGs, storageClassLVGParametersMap, err := utils.GetStorageClassLVGsAndParameters(ctx, d.cl, log, request.Parameters[internal.LVMVolumeGroupKey])
	if err != nil {
		log.Error("unable to get the storage class LVMVolumeGroups", logger.Err(err))
		return nil, status.Errorf(codes.Internal, "error during GetStorageClassLVGs")
	}

	contiguous := utils.IsContiguous(request, LvmType)
	log.Info("resolved contiguous", "contiguous", contiguous)

	// TODO: Consider refactoring the naming strategy for llvName and lvName.
	// Currently, we use the same name for llvName (the name of the LVMLogicalVolume resource in Kubernetes)
	// and lvName (the name of the LV in LVM on the node) because the PV name is unique within the cluster,
	// preventing name collisions. This approach simplifies matching between nodes and Kubernetes by maintaining
	// the same name in both contexts. Future consideration should be given to optimizing this logic to enhance
	// code readability and maintainability.
	llvName := volumeID
	lvName := volumeID
	log.Info("resolved LVMLogicalVolume name", "llvName", llvName)

	llvSize := resource.NewQuantity(request.CapacityRange.GetRequiredBytes(), resource.BinarySI)
	log.Info("resolved LVMLogicalVolume size", "llvSize", llvSize.String())

	var selectedLVG *v1alpha1.LVMVolumeGroup
	var preferredNode string
	var sourceVolume *v1alpha1.LVMLogicalVolumeSource

	if request.VolumeContentSource != nil {
		// The node clones and restores with `lvcreate -s`, which exists for thin
		// volumes only. A Thick LVMLogicalVolume carrying a Source is created by the
		// agent as an empty LV and the source is silently ignored, so the user would
		// end up with a Bound PVC holding no data. Reject the combination here to
		// turn that into a synchronous error.
		if LvmType != internal.LVMTypeThin {
			err := fmt.Errorf("volume content source is supported for %s volumes only, got %s", internal.LVMTypeThin, LvmType)
			log.Error("unsupported LVM type for a volume with a content source", logger.Err(err), slog.String("lvmType", LvmType))
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		sourceVolume = &v1alpha1.LVMLogicalVolumeSource{}
		switch s := request.VolumeContentSource.Type.(type) {
		case *csi.VolumeContentSource_Snapshot:
			sourceVolume.Kind = sourceVolumeKindSnapshot
			sourceVolume.Name = s.Snapshot.SnapshotId

			sourceVol, err := utils.GetLVMLogicalVolumeSnapshot(ctx, d.cl, sourceVolume.Name, "")
			if err != nil {
				log.Error("unable to get the source LVMLogicalVolumeSnapshot", logger.Err(err), slog.String("sourceName", sourceVolume.Name))
				return nil, status.Errorf(codes.NotFound, "error getting LVMLogicalVolumeSnapshot %s: %s", sourceVolume.Name, err.Error())
			}

			if sourceVol.Status == nil || sourceVol.Status.Phase != internal.LLVSStatusCreated {
				log.Error("the source LVMLogicalVolumeSnapshot is not in the Created phase", slog.String("sourceName", sourceVolume.Name))
				return nil, status.Errorf(codes.FailedPrecondition, "LVMLogicalVolumeSnapshot %s is not in Created phase", sourceVolume.Name)
			}

			selectedLVG, err = utils.SelectLVGByActualNameOnTheNode(storageClassLVGs, sourceVol.Status.NodeName, sourceVol.Status.ActualVGNameOnTheNode)
			if err != nil {
				log.Error("the source LVMVolumeGroup is not among the storage class LVMVolumeGroups", logger.Err(err), slog.String("vgName", sourceVol.Status.ActualVGNameOnTheNode), slog.String("nodeName", sourceVol.Status.NodeName))
				return nil, status.Errorf(codes.FailedPrecondition, "error getting LVMVolumeGroup %s: %s", sourceVol.Status.ActualVGNameOnTheNode, err.Error())
			}

			if _, ok := storageClassLVGParametersMap[selectedLVG.Name]; !ok {
				log.Error("the volume must use the same storage class as its source", slog.String("lvgName", selectedLVG.Name))
				return nil, status.Errorf(codes.InvalidArgument, "should use the same storage class as source")
			}

			if llvSize.Value() == 0 {
				*llvSize = sourceVol.Status.Size
			} else {
				alignedLlvSize, alignErr := alignToLVGExtentOrStatusErr(log, *llvSize, selectedLVG)
				if alignErr != nil {
					return nil, alignErr
				}
				*llvSize = alignedLlvSize
				if llvSize.Value() < sourceVol.Status.Size.Value() {
					return nil, status.Error(codes.OutOfRange, "requested size is smaller than the size of the source")
				}
			}

			preferredNode = sourceVol.Status.NodeName
		case *csi.VolumeContentSource_Volume:
			sourceVolume.Kind = sourceVolumeKindVolume
			sourceVolume.Name = s.Volume.VolumeId

			sourceVol, err := utils.GetLVMLogicalVolume(ctx, d.cl, sourceVolume.Name, "")
			if err != nil {
				log.Error("unable to get the source LVMLogicalVolume", logger.Err(err), slog.String("sourceName", sourceVolume.Name))
				return nil, status.Errorf(codes.NotFound, "error getting LVMLogicalVolume %s: %s", sourceVolume.Name, err.Error())
			}

			if sourceVol.Spec.Type != internal.LVMTypeThin {
				return nil, status.Errorf(codes.InvalidArgument, "Source LVMLogicalVolume '%s' is not of 'Thin' type", sourceVol.Name)
			}

			sourceSizeQty, err := resource.ParseQuantity(sourceVol.Spec.Size)
			if err != nil {
				log.Error("unable to parse the source volume size", logger.Err(err), slog.String("size", sourceVol.Spec.Size))
				return nil, status.Errorf(codes.Internal, "error parsing quantity: %v", err)
			}

			selectedLVG, err = utils.SelectLVGByName(storageClassLVGs, sourceVol.Spec.LVMVolumeGroupName)
			if err != nil {
				log.Error("unable to select the LVMVolumeGroup by name", logger.Err(err), slog.String("lvgName", sourceVol.Spec.LVMVolumeGroupName))
				return nil, status.Errorf(codes.Internal, "error getting LVMVolumeGroup %s: %s", sourceVol.Spec.LVMVolumeGroupName, err.Error())
			}

			if _, ok := storageClassLVGParametersMap[selectedLVG.Name]; !ok {
				log.Error("the volume must use the same storage class as its source", slog.String("lvgName", selectedLVG.Name))
				return nil, status.Errorf(codes.InvalidArgument, "should use the same storage class as source")
			}

			if llvSize.Value() == 0 {
				*llvSize = sourceSizeQty
			} else {
				alignedLlvSize, alignErr := alignToLVGExtentOrStatusErr(log, *llvSize, selectedLVG)
				if alignErr != nil {
					return nil, alignErr
				}
				*llvSize = alignedLlvSize
				if llvSize.Value() < sourceSizeQty.Value() {
					return nil, status.Error(codes.OutOfRange, "requested size is smaller than the size of the source")
				}
			}

			preferredNode = selectedLVG.Spec.Local.NodeName
		}
	} else {
		// Free space of the node picked by Immediate binding. The thick check against
		// it is deferred until after the size has been aligned below, because the node
		// provisions whole extents and it is the aligned size that has to fit.
		var maxFreeSpace resource.Quantity

		switch BindingMode {
		case internal.BindingModeI:
			log.Info("immediate binding, selecting a node", "bindingMode", internal.BindingModeI)
			selectedNodeName, freeSpace, err := utils.GetNodeWithMaxFreeSpace(storageClassLVGs, storageClassLVGParametersMap, LvmType)
			if err != nil {
				log.Error("unable to select the node with the most free space", logger.Err(err))
				return nil, status.Errorf(codes.Internal, "error selecting the node with max free space: %s", err.Error())
			}

			preferredNode = selectedNodeName
			maxFreeSpace = freeSpace
			log.Info("selected a node", "nodeName", selectedNodeName, "freeSpace", freeSpace.String())
		case internal.BindingModeWFFC:
			log.Info("late binding, taking the preferred node from the request", "bindingMode", internal.BindingModeWFFC)
			if len(request.AccessibilityRequirements.Preferred) != 0 {
				t := request.AccessibilityRequirements.Preferred[0].Segments
				preferredNode = t[internal.TopologyKey]
			}
		}

		log.Trace("selecting an LVMVolumeGroup", "preferredNode", preferredNode)
		selectedLVG, err = utils.SelectLVG(storageClassLVGs, preferredNode)
		log.Info("selected an LVMVolumeGroup", "lvg", fmt.Sprintf("%+v", selectedLVG))
		if err != nil {
			log.Error("unable to select an LVMVolumeGroup", logger.Err(err), slog.String("preferredNode", preferredNode))
			return nil, status.Errorf(codes.Internal, "error during SelectLVG")
		}

		// Align the requested size to the LVM extent boundary so that Spec.Size and
		// the reported capacity match the size the node actually provisions. The
		// snapshot/volume content-source branches above already align; without this
		// the plain path stores an unaligned size (e.g. 35Mi) that drifts from the
		// extent-rounded LV on the node.
		alignedLlvSize, alignErr := alignToLVGExtentOrStatusErr(log, *llvSize, selectedLVG)
		if alignErr != nil {
			return nil, alignErr
		}
		*llvSize = alignedLlvSize

		if !fitsFreeSpace(BindingMode, LvmType, *llvSize, maxFreeSpace) {
			return nil, status.Errorf(codes.Internal, "requested size: %s is greater than free space: %s", llvSize.String(), maxFreeSpace.String())
		}
	}

	volumeCleanup := request.Parameters[LVMVolumeCleanupParamKey]
	if !feature.VolumeCleanupEnabled() && volumeCleanup != "" {
		return nil, errors.New("volume cleanup is not supported in your edition")
	}

	llvSpec := utils.GetLLVSpec(
		log,
		lvName,
		*selectedLVG,
		storageClassLVGParametersMap,
		LvmType,
		*llvSize,
		contiguous,
		sourceVolume,
		volumeCleanup,
	)

	log.Info("built the LVMLogicalVolume spec", "spec", fmt.Sprintf("%+v", llvSpec))

	log.Trace("creating the LVMLogicalVolume")
	_, err = utils.CreateLVMLogicalVolume(ctx, d.cl, log, llvName, llvSpec)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			log.Info("the LVMLogicalVolume already exists, skipping creation", "llvName", llvName)
		} else {
			log.Error("unable to create the LVMLogicalVolume", logger.Err(err), slog.String("llvName", llvName))
			return nil, err
		}
	}
	log.Trace("created the LVMLogicalVolume")

	log.Trace("waiting for the LVMLogicalVolume status")

	createdLLV, attemptCounter, err := utils.WaitForStatusUpdate(ctx, d.cl, log, request.Name, "", *llvSize, utils.SafeExtentSize(selectedLVG.Status.ExtentSize))
	if err != nil {
		log.Error("the LVMLogicalVolume did not become ready, deleting it", logger.Err(err), slog.String("llvName", request.Name))

		deleteErr := utils.DeleteLVMLogicalVolume(ctx, d.cl, log, request.Name, volumeCleanup)
		if deleteErr != nil {
			log.Error("unable to delete the LVMLogicalVolume after a failed wait", logger.Err(deleteErr), slog.String("llvName", request.Name))
		}

		log.Error("unable to create the volume", logger.Err(err))
		return nil, err
	}
	log.Trace("the LVMLogicalVolume became ready", "attempts", attemptCounter)

	capacityBytes, fromStatus := resolveCapacityBytes(createdLLV, *llvSize)
	if !fromStatus {
		log.Warn("the LVMLogicalVolume has no status after a successful wait, reporting the aligned requested size", "llvName", request.Name, "alignedSize", llvSize.String())
	}

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

	log.Info("volume created successfully", "volumeContext", fmt.Sprintf("%+v", volumeCtx))

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes: capacityBytes,
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
	traceID := uuid.New().String()
	log := d.log.Named("DeleteVolume").With("traceID", traceID, "volumeID", request.VolumeId)

	log.Info("start")
	if len(request.VolumeId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID cannot be empty")
	}

	volumeCleanup := func() string {
		localStorageClass, err := utils.GetLSCBeforeLLVDelete(ctx, d.cl, log, request.VolumeId)
		if err == nil && localStorageClass != nil && localStorageClass.Spec.LVM != nil {
			return localStorageClass.Spec.LVM.VolumeCleanup
		}
		return ""
	}()

	if volumeCleanup != "" && !feature.VolumeCleanupEnabled() {
		return nil, errors.New("volumeCleanup is not supported in your edition")
	}

	err := utils.DeleteLVMLogicalVolume(ctx, d.cl, log, request.VolumeId, volumeCleanup)
	if err != nil {
		log.Error("unable to delete the LVMLogicalVolume", logger.Err(err))
		return nil, err
	}
	log.Info("volume deleted successfully")
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) ControllerPublishVolume(_ context.Context, request *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	d.log.Named("ControllerPublishVolume").Info("called")
	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			d.publishInfoVolumeName: request.VolumeId,
		},
	}, nil
}

func (d *Driver) ControllerUnpublishVolume(_ context.Context, _ *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	d.log.Named("ControllerUnpublishVolume").Info("called")
	// todo called Immediate
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (d *Driver) ValidateVolumeCapabilities(_ context.Context, _ *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	d.log.Named("ValidateVolumeCapabilities").Info("called")
	return nil, nil
}

func (d *Driver) ListVolumes(_ context.Context, _ *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	d.log.Named("ListVolumes").Info("called")
	return nil, nil
}

func (d *Driver) GetCapacity(_ context.Context, _ *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	d.log.Named("GetCapacity").Info("called")

	// todo MaxSize one PV
	// todo call volumeBindingMode: WaitForFirstConsumer

	return &csi.GetCapacityResponse{
		AvailableCapacity: 1000000,
		MaximumVolumeSize: nil,
		MinimumVolumeSize: nil,
	}, nil
}

func (d *Driver) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	d.log.Named("ControllerGetCapabilities").Info("called")

	var capabilities = []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
		csi.ControllerServiceCapability_RPC_GET_CAPACITY,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	}

	if feature.SnapshotsEnabled() {
		capabilities = append(capabilities, csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT)
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

func (d *Driver) ControllerExpandVolume(ctx context.Context, request *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	traceID := uuid.New().String()

	log := d.log.Named("ControllerExpandVolume").With("traceID", traceID, "volumeID", request.GetVolumeId())

	log.Trace("start", "request", request.String())

	volumeID := request.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume id cannot be empty")
	}

	llv, err := utils.GetLVMLogicalVolume(ctx, d.cl, volumeID, "")
	if err != nil {
		log.Error("unable to get the LVMLogicalVolume", logger.Err(err))
		return nil, status.Errorf(codes.Internal, "error getting LVMLogicalVolume: %s", err.Error())
	}

	lvg, err := utils.GetLVMVolumeGroup(ctx, d.cl, llv.Spec.LVMVolumeGroupName)
	if err != nil {
		log.Error("unable to get the LVMVolumeGroup", logger.Err(err), slog.String("lvgName", llv.Spec.LVMVolumeGroupName))
		return nil, status.Errorf(codes.Internal, "error getting LVMVolumeGroup: %v", err)
	}

	requestCapacity := resource.NewQuantity(request.CapacityRange.GetRequiredBytes(), resource.BinarySI)
	log.Trace("requested capacity", "requestCapacity", requestCapacity.String())

	alignedRequestCapacity, err := alignToLVGExtentOrStatusErr(log, *requestCapacity, lvg)
	if err != nil {
		return nil, err
	}

	nodeExpansionRequired := request.GetVolumeCapability().GetBlock() == nil
	log.Info("resolved node expansion requirement", "nodeExpansionRequired", nodeExpansionRequired)

	if llv.Status.ActualSize.Value() >= alignedRequestCapacity.Value() {
		log.Warn("the actual size already covers the aligned request, skipping the resize", "actualSize", llv.Status.ActualSize.String(), "alignedRequestSize", alignedRequestCapacity.String(), "nodeExpansionRequired", nodeExpansionRequired, "capacityBytes", llv.Status.ActualSize.Value())
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         llv.Status.ActualSize.Value(),
			NodeExpansionRequired: nodeExpansionRequired,
		}, nil
	}

	if llv.Spec.Type == internal.LVMTypeThick {
		lvgFreeSpace := utils.GetLVMVolumeGroupFreeSpace(*lvg)

		if lvgFreeSpace.Value() < (alignedRequestCapacity.Value() - llv.Status.ActualSize.Value()) {
			log.Error("the requested size exceeds the free space of the LVMVolumeGroup", slog.String("alignedRequestSize", alignedRequestCapacity.String()), slog.String("lvgFreeSpace", lvgFreeSpace.String()))
			return nil, status.Errorf(codes.Internal, "requested size: %s is greater than the capacity of the LVMVolumeGroup: %s", alignedRequestCapacity.String(), lvgFreeSpace.String())
		}
	}

	log.Info("resizing the LVMLogicalVolume", "requestedSize", requestCapacity.String(), "actualSize", llv.Status.ActualSize.String())
	err = utils.ExpandLVMLogicalVolume(ctx, d.cl, llv, requestCapacity.String())
	if err != nil {
		log.Error("unable to update the LVMLogicalVolume", logger.Err(err))
		return nil, status.Errorf(codes.Internal, "error updating LVMLogicalVolume: %v", err)
	}

	updatedLLV, attemptCounter, err := utils.WaitForStatusUpdate(ctx, d.cl, log, llv.Name, llv.Namespace, *requestCapacity, utils.SafeExtentSize(lvg.Status.ExtentSize))
	if err != nil {
		log.Error("the resized LVMLogicalVolume did not become ready", logger.Err(err))
		return nil, err
	}
	log.Info("the LVMLogicalVolume was resized", "attempts", attemptCounter)

	capacityBytes, fromStatus := resolveCapacityBytes(updatedLLV, alignedRequestCapacity)
	if !fromStatus {
		log.Warn("the LVMLogicalVolume has no status after a successful wait, reporting the aligned requested size", "llvName", llv.Name, "alignedSize", alignedRequestCapacity.String())
	}

	log.Info("volume expanded successfully")

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         capacityBytes,
		NodeExpansionRequired: nodeExpansionRequired,
	}, nil
}

func (d *Driver) ControllerGetVolume(_ context.Context, _ *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	d.log.Named("ControllerGetVolume").Info("called")
	return &csi.ControllerGetVolumeResponse{}, nil
}

func (d *Driver) ControllerModifyVolume(_ context.Context, _ *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	d.log.Named("ControllerModifyVolume").Info("called")
	return &csi.ControllerModifyVolumeResponse{}, nil
}
