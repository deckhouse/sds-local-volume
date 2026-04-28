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
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/rawfile"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/utils"
	"github.com/deckhouse/sds-local-volume/lib/go/common/pkg/feature"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	sourceVolumeKindSnapshot = "LVMLogicalVolumeSnapshot"
	sourceVolumeKindVolume   = "LVMLogicalVolume"
	LVMVolumeCleanupParamKey = "local.csi.storage.deckhouse.io/lvm-volume-cleanup"
)

func (d *Driver) CreateVolume(ctx context.Context, request *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	traceID := uuid.New().String()

	d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s] ========== CreateVolume ============", traceID))
	d.log.Trace(request.String())
	d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s] ========== CreateVolume ============", traceID))

	volumeType := request.Parameters[internal.TypeKey]

	switch volumeType {
	case internal.Lvm:
		return d.createLVMVolume(ctx, request, traceID)
	case internal.RawFile:
		return d.createRawFileVolume(ctx, request, traceID)
	default:
		if volumeType == "" {
			return nil, status.Error(codes.InvalidArgument, "storage class type must be set (e.g. lvm or rawfile), got empty")
		}
		return nil, status.Errorf(codes.InvalidArgument, "Unsupported Storage Class type: %q", volumeType)
	}
}

func (d *Driver) createRawFileVolume(_ context.Context, request *csi.CreateVolumeRequest, traceID string) (*csi.CreateVolumeResponse, error) {
	if len(request.Name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume Name cannot be empty")
	}
	volumeID := request.Name
	if err := rawfile.ValidateVolumeID(volumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "[CreateVolume] invalid volume ID: %v", err)
	}
	if request.VolumeContentSource != nil {
		return nil, status.Error(codes.InvalidArgument, "RawFile volumes do not support clone or snapshot (VolumeContentSource)")
	}

	if request.VolumeCapabilities == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capability cannot be empty")
	}

	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] Creating RawFile volume", traceID, volumeID))

	if request.CapacityRange == nil {
		return nil, status.Error(codes.InvalidArgument, "CapacityRange is required")
	}

	sizeBytes := request.CapacityRange.GetRequiredBytes()
	if sizeBytes == 0 {
		sizeBytes = request.CapacityRange.GetLimitBytes()
	}
	if sizeBytes == 0 {
		return nil, status.Error(codes.InvalidArgument, "CapacityRange must specify RequiredBytes or LimitBytes")
	}

	bindingMode := request.Parameters[internal.BindingModeKey]
	d.log.Debug(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] BindingMode from parameters: '%s'", traceID, volumeID, bindingMode))

	// Build volume context with only the parameters needed for node-side volume creation
	volumeCtx := map[string]string{
		internal.TypeKey:        internal.RawFile,
		internal.BindingModeKey: bindingMode,
		internal.RawFileSizeKey: strconv.FormatInt(sizeBytes, 10),
	}
	if v, ok := request.Parameters[internal.RawFileSparseKey]; ok {
		volumeCtx[internal.RawFileSparseKey] = v
	}
	if v, ok := request.Parameters[internal.FSTypeKey]; ok {
		volumeCtx[internal.FSTypeKey] = v
	}

	// Parse allowed nodes from StorageClass parameters
	var allowedNodes map[string]struct{}
	if nodesParam, ok := request.Parameters[internal.RawFileNodesKey]; ok && nodesParam != "" {
		nodeList := strings.Split(nodesParam, ",")
		allowedNodes = make(map[string]struct{}, len(nodeList))
		for _, n := range nodeList {
			if trimmed := strings.TrimSpace(n); trimmed != "" {
				allowedNodes[trimmed] = struct{}{}
			}
		}
		d.log.Debug(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] RawFile allowed nodes: %v", traceID, volumeID, allowedNodes))
	}

	// Build accessible topology based on binding mode
	var accessibleTopology []*csi.Topology

	// Log AccessibilityRequirements for debugging
	if request.AccessibilityRequirements != nil {
		d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] AccessibilityRequirements.Preferred: %+v", traceID, volumeID, request.AccessibilityRequirements.Preferred))
		d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] AccessibilityRequirements.Requisite: %+v", traceID, volumeID, request.AccessibilityRequirements.Requisite))
	} else {
		d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] AccessibilityRequirements is nil", traceID, volumeID))
	}

	// RawFile volumes are inherently node-local: the backing file lives on a single
	// node's filesystem and cannot be migrated. Therefore we MUST advertise exactly
	// one node in AccessibleTopology — otherwise external-provisioner would set
	// PV.nodeAffinity to multiple nodes, the scheduler could place a Pod on a
	// different node from the one that already holds the file, NodeStageVolume would
	// silently create an empty file on the new node, and the original file would be
	// orphaned with no clean way to detect it (the cleanup goroutine treats existing
	// PVs as live). See review comment #2.
	candidates := topologyCandidates(request.AccessibilityRequirements, bindingMode)
	if len(candidates) == 0 {
		// Both binding modes REQUIRE provisioner to pass topology. For
		// Immediate, external-provisioner derives Requisite from
		// StorageClass.AllowedTopologies (which we populate from
		// rawFile.nodes in the LSC controller). For WFFC the scheduler
		// fills in Preferred[0]. Falling back to the current controller
		// pod's hostID is unsafe in HA: the leader can change between
		// restarts, so two PVs created back-to-back could pin to two
		// random nodes regardless of allowedNodes / data locality.
		return nil, status.Errorf(
			codes.InvalidArgument,
			"[CreateVolume] no node topology provided (binding mode %q); set StorageClass.allowedTopologies or use WaitForFirstConsumer",
			bindingMode,
		)
	}

	// Defence-in-depth: re-apply the rawFile.nodes filter after the scheduler /
	// external-provisioner already considered StorageClass.allowedTopologies.
	// In normal operation this filter is a no-op because allowedTopologies is
	// the authoritative source for kube-scheduler and external-provisioner.
	// We keep it for cases where rawFile.nodes is changed but the StorageClass
	// has not yet been re-rendered, or where the request bypasses the standard
	// provisioner path.
	if len(allowedNodes) > 0 {
		var filtered []*csi.Topology
		for _, t := range candidates {
			if nodeName, ok := t.Segments[internal.TopologyKey]; ok {
				if _, allowed := allowedNodes[nodeName]; allowed {
					filtered = append(filtered, t)
				}
			}
		}
		if len(filtered) == 0 {
			return nil, status.Errorf(codes.ResourceExhausted, "[CreateVolume] no eligible nodes after filtering by rawFile.nodes constraint (allowed: %v)", allowedNodes)
		}
		if len(filtered) != len(candidates) {
			d.log.Debug(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] Filtered candidates from %d to %d nodes by rawFile.nodes", traceID, volumeID, len(candidates), len(filtered)))
		}
		candidates = filtered
	}

	// Pick exactly one node — the first candidate (Preferred[0] in WFFC, first
	// allowed node otherwise). Single-node accessibility makes PV.nodeAffinity
	// pin the volume to one node, eliminating the orphan-file scenario above.
	accessibleTopology = candidates[:1]
	if pinned, ok := accessibleTopology[0].Segments[internal.TopologyKey]; ok {
		d.log.Debug(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] Pinned RawFile to single node: %s", traceID, volumeID, pinned))
	}

	d.log.Debug(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] RawFile volume metadata prepared, size: %d bytes", traceID, volumeID, sizeBytes))

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes:      sizeBytes,
			VolumeId:           volumeID,
			VolumeContext:      volumeCtx,
			AccessibleTopology: accessibleTopology,
		},
	}, nil
}

func (d *Driver) createLVMVolume(ctx context.Context, request *csi.CreateVolumeRequest, traceID string) (*csi.CreateVolumeResponse, error) {
	if len(request.Name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume Name cannot be empty")
	}
	volumeID := request.Name
	if request.VolumeCapabilities == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capability cannot be empty")
	}

	BindingMode := request.Parameters[internal.BindingModeKey]
	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] storage class BindingMode: %s", traceID, volumeID, BindingMode))

	LvmType := request.Parameters[internal.LvmTypeKey]
	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] storage class LvmType: %s", traceID, volumeID, LvmType))

	if len(request.Parameters[internal.LVMVolumeGroupKey]) == 0 {
		err := errors.New("no LVMVolumeGroups specified in a storage class's parameters")
		d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] no LVMVolumeGroups were found for the request: %+v", traceID, volumeID, request))
		return nil, status.Errorf(codes.InvalidArgument, "no LVMVolumeGroups specified in a storage class's parameters")
	}

	storageClassLVGs, storageClassLVGParametersMap, err := utils.GetStorageClassLVGsAndParameters(ctx, d.cl, d.log, request.Parameters[internal.LVMVolumeGroupKey])
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error GetStorageClassLVGs", traceID, volumeID))
		return nil, status.Errorf(codes.Internal, "error during GetStorageClassLVGs")
	}

	contiguous := utils.IsContiguous(request, LvmType)
	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] contiguous: %t", traceID, volumeID, contiguous))

	// TODO: Consider refactoring the naming strategy for llvName and lvName.
	// Currently, we use the same name for llvName (the name of the LVMLogicalVolume resource in Kubernetes)
	// and lvName (the name of the LV in LVM on the node) because the PV name is unique within the cluster,
	// preventing name collisions. This approach simplifies matching between nodes and Kubernetes by maintaining
	// the same name in both contexts. Future consideration should be given to optimizing this logic to enhance
	// code readability and maintainability.
	llvName := volumeID
	lvName := volumeID
	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] llv name: %s", traceID, volumeID, llvName))

	llvSize := resource.NewQuantity(request.CapacityRange.GetRequiredBytes(), resource.BinarySI)
	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] llv size: %s", traceID, volumeID, llvSize.String()))

	var selectedLVG *snc.LVMVolumeGroup
	var preferredNode string
	var sourceVolume *snc.LVMLogicalVolumeSource

	if request.VolumeContentSource != nil {
		sourceVolume = &snc.LVMLogicalVolumeSource{}
		switch s := request.VolumeContentSource.Type.(type) {
		case *csi.VolumeContentSource_Snapshot:
			sourceVolume.Kind = sourceVolumeKindSnapshot
			sourceVolume.Name = s.Snapshot.SnapshotId

			sourceVol, err := utils.GetLVMLogicalVolumeSnapshot(ctx, d.cl, sourceVolume.Name, "")
			if err != nil {
				d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error getting source LVMLogicalVolumeSnapshot", traceID, sourceVolume.Name))
				return nil, status.Errorf(codes.NotFound, "error getting LVMLogicalVolumeSnapshot %s: %s", sourceVolume.Name, err.Error())
			}

			if sourceVol.Status == nil || sourceVol.Status.Phase != internal.LLVSStatusCreated {
				d.log.Error(nil, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] source LVMLogicalVolumeSnapshot is not in Created phase", traceID, sourceVolume.Name))
				return nil, status.Errorf(codes.FailedPrecondition, "LVMLogicalVolumeSnapshot %s is not in Created phase", sourceVolume.Name)
			}

			selectedLVG, err = utils.SelectLVGByActualNameOnTheNode(storageClassLVGs, sourceVol.Status.NodeName, sourceVol.Status.ActualVGNameOnTheNode)
			if err != nil {
				d.log.Error(
					err,
					fmt.Sprintf(
						"[CreateVolume][traceID:%s] source LVMVolumeGroup %s from node %s is not found in storage class LVGs",
						traceID,
						sourceVol.Status.ActualVGNameOnTheNode,
						sourceVol.Status.NodeName,
					),
				)
				return nil, status.Errorf(codes.FailedPrecondition, "error getting LVMVolumeGroup %s: %s", sourceVol.Status.ActualVGNameOnTheNode, err.Error())
			}

			if _, ok := storageClassLVGParametersMap[selectedLVG.Name]; !ok {
				d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] should use the same storage class as source", traceID, volumeID))
				return nil, status.Errorf(codes.InvalidArgument, "should use the same storage class as source")
			}

			if llvSize.Value() == 0 {
				*llvSize = sourceVol.Status.Size
			} else {
				alignedLlvSize, alignErr := utils.AlignSizeToExtent(*llvSize, utils.SafeExtentSize(selectedLVG.Status.ExtentSize))
				if alignErr != nil {
					d.log.Error(alignErr, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error aligning size to extent", traceID, volumeID))
					return nil, status.Errorf(codes.Internal, "error aligning size to extent: %s", alignErr.Error())
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
				d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error getting source LVMLogicalVolume", traceID, sourceVolume.Name))
				return nil, status.Errorf(codes.NotFound, "error getting LVMLogicalVolume %s: %s", sourceVolume.Name, err.Error())
			}

			if sourceVol.Spec.Type != internal.LVMTypeThin {
				return nil, status.Errorf(codes.InvalidArgument, "Source LVMLogicalVolume '%s' is not of 'Thin' type", sourceVol.Name)
			}

			sourceSizeQty, err := resource.ParseQuantity(sourceVol.Spec.Size)
			if err != nil {
				d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s] error parsing quantity %s", traceID, sourceVol.Spec.Size))
				return nil, status.Errorf(codes.Internal, "error parsing quantity: %v", err)
			}

			selectedLVG, err = utils.SelectLVGByName(storageClassLVGs, sourceVol.Spec.LVMVolumeGroupName)
			if err != nil {
				d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s] error getting LVMVolumeGroup %s", traceID, sourceVol.Spec.LVMVolumeGroupName))
				return nil, status.Errorf(codes.Internal, "error getting LVMVolumeGroup %s: %s", sourceVol.Spec.LVMVolumeGroupName, err.Error())
			}

			if _, ok := storageClassLVGParametersMap[selectedLVG.Name]; !ok {
				d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] should use the same storage class as source", traceID, volumeID))
				return nil, status.Errorf(codes.InvalidArgument, "should use the same storage class as source")
			}

			if llvSize.Value() == 0 {
				*llvSize = sourceSizeQty
			} else {
				alignedLlvSize, alignErr := utils.AlignSizeToExtent(*llvSize, utils.SafeExtentSize(selectedLVG.Status.ExtentSize))
				if alignErr != nil {
					d.log.Error(alignErr, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error aligning size to extent", traceID, volumeID))
					return nil, status.Errorf(codes.Internal, "error aligning size to extent: %s", alignErr.Error())
				}
				*llvSize = alignedLlvSize
				if llvSize.Value() < sourceSizeQty.Value() {
					return nil, status.Error(codes.OutOfRange, "requested size is smaller than the size of the source")
				}
			}

			preferredNode = selectedLVG.Spec.Local.NodeName
		}
	} else {
		switch BindingMode {
		case internal.BindingModeI:
			d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] BindingMode is %s. Start selecting node", traceID, volumeID, internal.BindingModeI))
			selectedNodeName, freeSpace, err := utils.GetNodeWithMaxFreeSpace(storageClassLVGs, storageClassLVGParametersMap, LvmType)
			if err != nil {
				d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error GetNodeMaxVGSize", traceID, volumeID))
			}

			preferredNode = selectedNodeName
			d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] Selected node: %s, free space %s", traceID, volumeID, selectedNodeName, freeSpace.String()))
			if LvmType == internal.LVMTypeThick {
				if llvSize.Value() > freeSpace.Value() {
					return nil, status.Errorf(codes.Internal, "requested size: %s is greater than free space: %s", llvSize.String(), freeSpace.String())
				}
			}
		case internal.BindingModeWFFC:
			d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] BindingMode is %s. Get preferredNode", traceID, volumeID, internal.BindingModeWFFC))
			if request.AccessibilityRequirements != nil && len(request.AccessibilityRequirements.Preferred) != 0 {
				t := request.AccessibilityRequirements.Preferred[0].Segments
				preferredNode = t[internal.TopologyKey]
			}
		}

		d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] preferredNode: %s. Select LVG", traceID, volumeID, preferredNode))
		selectedLVG, err = utils.SelectLVG(storageClassLVGs, preferredNode)
		d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] selectedLVG: %+v", traceID, volumeID, selectedLVG))
		if err != nil {
			d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error SelectLVG", traceID, volumeID))
			return nil, status.Errorf(codes.Internal, "error during SelectLVG")
		}
	}

	volumeCleanup := request.Parameters[LVMVolumeCleanupParamKey]
	if !feature.VolumeCleanupEnabled() && volumeCleanup != "" {
		return nil, errors.New("volume cleanup is not supported in your edition")
	}

	llvSpec := utils.GetLLVSpec(
		d.log,
		lvName,
		*selectedLVG,
		storageClassLVGParametersMap,
		LvmType,
		*llvSize,
		contiguous,
		sourceVolume,
		volumeCleanup,
	)

	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] LVMLogicalVolumeSpec: %+v", traceID, volumeID, llvSpec))

	d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] ------------ CreateLVMLogicalVolume start ------------", traceID, volumeID))
	_, err = utils.CreateLVMLogicalVolume(ctx, d.cl, d.log, traceID, llvName, llvSpec)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] LVMLogicalVolume %s already exists. Skip creating", traceID, volumeID, llvName))
		} else {
			d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error CreateLVMLogicalVolume", traceID, volumeID))
			return nil, err
		}
	}
	d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] ------------ CreateLVMLogicalVolume end ------------", traceID, volumeID))

	d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] start wait CreateLVMLogicalVolume", traceID, volumeID))

	attemptCounter, err := utils.WaitForStatusUpdate(ctx, d.cl, d.log, traceID, request.Name, "", *llvSize, utils.SafeExtentSize(selectedLVG.Status.ExtentSize))
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error WaitForStatusUpdate. Delete LVMLogicalVolume %s", traceID, volumeID, request.Name))

		deleteErr := utils.DeleteLVMLogicalVolume(ctx, d.cl, d.log, traceID, request.Name, volumeCleanup)
		if deleteErr != nil {
			d.log.Error(deleteErr, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error DeleteLVMLogicalVolume", traceID, volumeID))
		}

		d.log.Error(err, fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] error creating LVMLogicalVolume", traceID, volumeID))
		return nil, err
	}
	d.log.Trace(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] finish wait CreateLVMLogicalVolume, attempt counter = %d", traceID, volumeID, attemptCounter))

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

	d.log.Info(fmt.Sprintf("[CreateVolume][traceID:%s][volumeID:%s] Volume created successfully. volumeCtx: %+v", traceID, volumeID, volumeCtx))

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
	traceID := uuid.New().String()
	d.log.Info(fmt.Sprintf("[DeleteVolume][traceID:%s] ========== Start DeleteVolume ============", traceID))
	if len(request.VolumeId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID cannot be empty")
	}

	volumeID := request.VolumeId

	// Determine volume type from PV attributes (source of truth)
	pv, pvErr := utils.GetPersistentVolume(ctx, d.cl, volumeID)
	if pvErr == nil && pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil &&
		pv.Spec.CSI.VolumeAttributes[internal.TypeKey] == internal.RawFile {
		return d.deleteRawFileVolume(ctx, nil, traceID, volumeID)
	}

	// Verify LLV exists before attempting LVM deletion to avoid errors
	// when PV is gone and volume is neither local RawFile nor LVM
	_, llvErr := utils.GetLVMLogicalVolume(ctx, d.cl, volumeID, "")
	if llvErr != nil {
		d.log.Info(fmt.Sprintf("[DeleteVolume][traceID:%s][volumeID:%s] No RawFile volume and no LVMLogicalVolume found, considering already deleted", traceID, volumeID))
		return &csi.DeleteVolumeResponse{}, nil
	}

	localStorageClass, err := utils.GetLSCBeforeLLVDelete(ctx, d.cl, *d.log, volumeID, traceID)
	if err != nil {
		d.log.Warning(fmt.Sprintf("[DeleteVolume][traceID:%s][volumeID:%s] Could not get LocalStorageClass: %v", traceID, volumeID, err))
	}

	return d.deleteLVMVolume(ctx, request, traceID, localStorageClass)
}

func (d *Driver) deleteRawFileVolume(_ context.Context, _ *csi.DeleteVolumeRequest, traceID string, volumeID string) (*csi.DeleteVolumeResponse, error) {
	// RawFile volume files live on specific nodes. The controller cannot
	// reliably delete them because it may run on a different node.
	// Actual file cleanup is handled by the cleanup goroutine running on
	// each CSI node pod, which watches for PVs with DeletionTimestamp and
	// the rawfile-pv-protection finalizer.
	d.log.Info(fmt.Sprintf("[DeleteVolume][traceID:%s][volumeID:%s] RawFile volume deletion acknowledged; file cleanup will be handled by the node-side cleanup goroutine", traceID, volumeID))
	d.log.Debug(fmt.Sprintf("[DeleteVolume][traceID:%s] ========== END DeleteVolume ============", traceID))
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) deleteLVMVolume(ctx context.Context, request *csi.DeleteVolumeRequest, traceID string, lsc *slv.LocalStorageClass) (*csi.DeleteVolumeResponse, error) {
	volumeID := request.VolumeId

	volumeCleanup := ""
	if lsc != nil && lsc.Spec.LVM != nil {
		volumeCleanup = lsc.Spec.LVM.VolumeCleanup
	}

	if volumeCleanup != "" && !feature.VolumeCleanupEnabled() {
		return nil, errors.New("volumeCleanup is not supported in your edition")
	}

	err := utils.DeleteLVMLogicalVolume(ctx, d.cl, d.log, traceID, volumeID, volumeCleanup)
	if err != nil {
		d.log.Error(err, "error DeleteLVMLogicalVolume")
		return nil, err
	}
	d.log.Info(fmt.Sprintf("[DeleteVolume][traceID:%s][volumeID:%s] LVM volume deleted successfully", traceID, volumeID))
	d.log.Info(fmt.Sprintf("[DeleteVolume][traceID:%s] ========== END DeleteVolume ============", traceID))
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) ControllerPublishVolume(_ context.Context, request *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	d.log.Info("method ControllerPublishVolume")
	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			d.publishInfoVolumeName: request.VolumeId,
		},
	}, nil
}

func (d *Driver) ControllerUnpublishVolume(_ context.Context, _ *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	d.log.Info("method ControllerUnpublishVolume")
	// todo called Immediate
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (d *Driver) ValidateVolumeCapabilities(_ context.Context, _ *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	d.log.Info("call method ValidateVolumeCapabilities")
	return nil, nil
}

func (d *Driver) ListVolumes(_ context.Context, _ *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	d.log.Info("call method ListVolumes")
	return nil, nil
}

func (d *Driver) GetCapacity(_ context.Context, _ *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	d.log.Info("method GetCapacity")

	// todo MaxSize one PV
	// todo call volumeBindingMode: WaitForFirstConsumer

	return &csi.GetCapacityResponse{
		AvailableCapacity: 1000000,
		MaximumVolumeSize: nil,
		MinimumVolumeSize: nil,
	}, nil
}

func (d *Driver) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	d.log.Info("method ControllerGetCapabilities")

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

	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s] method ControllerExpandVolume", traceID))
	d.log.Trace(fmt.Sprintf("[ControllerExpandVolume][traceID:%s] ========== ControllerExpandVolume ============", traceID))
	d.log.Trace(request.String())
	d.log.Trace(fmt.Sprintf("[ControllerExpandVolume][traceID:%s] ========== ControllerExpandVolume ============", traceID))

	volumeID := request.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume id cannot be empty")
	}

	// Get PersistentVolume to determine volume type
	pv, err := utils.GetPersistentVolume(ctx, d.cl, volumeID)
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] error getting PersistentVolume", traceID, volumeID))
		return nil, status.Errorf(codes.Internal, "error getting PersistentVolume: %s", err.Error())
	}

	// Check volume type from PV's VolumeAttributes
	volumeType := ""
	if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
		volumeType = pv.Spec.CSI.VolumeAttributes[internal.TypeKey]
	}
	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] Volume type: %s", traceID, volumeID, volumeType))

	// Route to appropriate expand function based on volume type
	if volumeType == internal.RawFile {
		return d.expandRawFileVolume(request, traceID)
	}

	// Default to LVM volume expansion
	llv, err := utils.GetLVMLogicalVolume(ctx, d.cl, volumeID, "")
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] error getting LVMLogicalVolume", traceID, volumeID))
		return nil, status.Errorf(codes.Internal, "error getting LVMLogicalVolume: %s", err.Error())
	}

	lvg, err := utils.GetLVMVolumeGroup(ctx, d.cl, llv.Spec.LVMVolumeGroupName)
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] error getting LVMVolumeGroup", traceID, volumeID))
		return nil, status.Errorf(codes.Internal, "error getting LVMVolumeGroup: %v", err)
	}

	requestCapacity := resource.NewQuantity(request.CapacityRange.GetRequiredBytes(), resource.BinarySI)
	d.log.Trace(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] requestCapacity: %s", traceID, volumeID, requestCapacity.String()))

	alignedRequestCapacity, err := utils.AlignSizeToExtent(*requestCapacity, utils.SafeExtentSize(lvg.Status.ExtentSize))
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] error aligning size to extent", traceID, volumeID))
		return nil, status.Errorf(codes.Internal, "error aligning size to extent: %s", err.Error())
	}

	nodeExpansionRequired := request.GetVolumeCapability().GetBlock() == nil
	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] NodeExpansionRequired: %t", traceID, volumeID, nodeExpansionRequired))

	if llv.Status.ActualSize.Value() >= alignedRequestCapacity.Value() {
		d.log.Warning(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] actual size %s is already >= aligned requested size %s, no need to resize LVMLogicalVolume %s, return NodeExpansionRequired: %t and CapacityBytes: %d", traceID, volumeID, llv.Status.ActualSize.String(), alignedRequestCapacity.String(), volumeID, nodeExpansionRequired, llv.Status.ActualSize.Value()))
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         llv.Status.ActualSize.Value(),
			NodeExpansionRequired: nodeExpansionRequired,
		}, nil
	}

	if llv.Spec.Type == internal.LVMTypeThick {
		lvgFreeSpace := utils.GetLVMVolumeGroupFreeSpace(*lvg)

		if lvgFreeSpace.Value() < (alignedRequestCapacity.Value() - llv.Status.ActualSize.Value()) {
			d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] requested size: %s is greater than the capacity of the LVMVolumeGroup: %s", traceID, volumeID, alignedRequestCapacity.String(), lvgFreeSpace.String()))
			return nil, status.Errorf(codes.Internal, "requested size: %s is greater than the capacity of the LVMVolumeGroup: %s", alignedRequestCapacity.String(), lvgFreeSpace.String())
		}
	}

	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] start resize LVMLogicalVolume", traceID, volumeID))
	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] requested size: %s, actual size: %s", traceID, volumeID, requestCapacity.String(), llv.Status.ActualSize.String()))
	err = utils.ExpandLVMLogicalVolume(ctx, d.cl, llv, requestCapacity.String())
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] error updating LVMLogicalVolume", traceID, volumeID))
		return nil, status.Errorf(codes.Internal, "error updating LVMLogicalVolume: %v", err)
	}

	attemptCounter, err := utils.WaitForStatusUpdate(ctx, d.cl, d.log, traceID, llv.Name, llv.Namespace, *requestCapacity, utils.SafeExtentSize(lvg.Status.ExtentSize))
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] error WaitForStatusUpdate", traceID, volumeID))
		return nil, err
	}
	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] finish resize LVMLogicalVolume, attempt counter = %d ", traceID, volumeID, attemptCounter))

	updatedLLV, err := utils.GetLVMLogicalVolume(ctx, d.cl, llv.Name, llv.Namespace)
	if err != nil {
		d.log.Error(err, fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] error re-fetching LVMLogicalVolume after resize", traceID, volumeID))
		return nil, status.Errorf(codes.Internal, "error re-fetching LVMLogicalVolume after resize: %v", err)
	}

	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] Volume expanded successfully", traceID, volumeID))

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         updatedLLV.Status.ActualSize.Value(),
		NodeExpansionRequired: nodeExpansionRequired,
	}, nil
}

func (d *Driver) expandRawFileVolume(request *csi.ControllerExpandVolumeRequest, traceID string) (*csi.ControllerExpandVolumeResponse, error) {
	volumeID := request.GetVolumeId()

	if request.CapacityRange == nil {
		return nil, status.Error(codes.InvalidArgument, "CapacityRange is required")
	}
	requestedBytes := request.CapacityRange.GetRequiredBytes()

	d.log.Info(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] RawFile volume expansion requested to %d bytes", traceID, volumeID, requestedBytes))

	nodeExpansionRequired := true
	if vc := request.GetVolumeCapability(); vc != nil {
		nodeExpansionRequired = vc.GetBlock() == nil
	}

	d.log.Debug(fmt.Sprintf("[ControllerExpandVolume][traceID:%s][volumeID:%s] RawFile volume expansion will be handled by NodeExpandVolume, NodeExpansionRequired: %t", traceID, volumeID, nodeExpansionRequired))

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requestedBytes,
		NodeExpansionRequired: nodeExpansionRequired,
	}, nil
}

func (d *Driver) ControllerGetVolume(_ context.Context, _ *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	d.log.Info(" call method ControllerGetVolume")
	return &csi.ControllerGetVolumeResponse{}, nil
}

func (d *Driver) ControllerModifyVolume(_ context.Context, _ *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	d.log.Info(" call method ControllerModifyVolume")
	return &csi.ControllerModifyVolumeResponse{}, nil
}

// topologyCandidates returns the ordered list of candidate topology segments for
// a CreateVolume request, picking Preferred first in WFFC mode (the scheduler
// already chose a node) and Requisite first in Immediate mode. Returns nil when
// the request did not carry any topology hints.
func topologyCandidates(req *csi.TopologyRequirement, bindingMode string) []*csi.Topology {
	if req == nil {
		return nil
	}
	switch bindingMode {
	case internal.BindingModeWFFC:
		if len(req.Preferred) != 0 {
			return req.Preferred
		}
		return req.Requisite
	default:
		if len(req.Requisite) != 0 {
			return req.Requisite
		}
		return req.Preferred
	}
}
