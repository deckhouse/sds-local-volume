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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"sds-local-volume-csi/internal"
	"sds-local-volume-csi/pkg/utils"
)

const (
	// default file system type to be used when it is not provided
	defaultFsType = internal.FSTypeExt4

	// VolumeOperationAlreadyExists is message fmt returned to CO when there is another in-flight call on the given volumeID
	VolumeOperationAlreadyExists = "An operation with the given volume=%q is already in progress"
)

var (
	// nodeCaps represents the capability of node service.
	nodeCaps = []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
	}

	ValidFSTypes = map[string]struct{}{
		internal.FSTypeExt4: {},
		internal.FSTypeXfs:  {},
	}
)

func (d *Driver) NodeStageVolume(_ context.Context, request *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	d.log.Debug(fmt.Sprintf("[NodeStageVolume] method called with request: %v", request))

	volumeID := request.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodeStageVolume] Volume id cannot be empty")
	}

	target := request.GetStagingTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodeStageVolume] Staging target path cannot be empty")
	}

	volCap := request.GetVolumeCapability()
	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument, "[NodeStageVolume] Volume capability cannot be empty")
	}

	context := request.GetVolumeContext()
	vgName, ok := context[internal.VGNameKey]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "[NodeStageVolume] Volume group name cannot be empty")
	}

	if volCap.GetBlock() != nil {
		d.log.Info("[NodeStageVolume] Block volume detected. Skipping staging.")
		return &csi.NodeStageVolumeResponse{}, nil
	}

	mountVolume := volCap.GetMount()
	if mountVolume == nil {
		return nil, status.Error(codes.InvalidArgument, "[NodeStageVolume] Volume capability mount cannot be empty")
	}

	fsType := mountVolume.GetFsType()
	d.log.Info(fmt.Sprintf("[NodeStageVolume] fsType: %s", fsType))
	d.log.Info(fmt.Sprintf("[NodeStageVolume] fsType (from context): %s", context[internal.FSTypeKey]))
	if fsType == "" {
		fsType = defaultFsType
	}

	_, ok = ValidFSTypes[strings.ToLower(fsType)]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, fmt.Sprintf("[NodeStageVolume] Invalid fsType: %s. Supported values: %v", fsType, ValidFSTypes))
	}

	blockSize, err := recheckFormattingOptionParameter(context, internal.BlockSizeKey, internal.FileSystemConfigs, fsType)
	if err != nil {
		return nil, err
	}
	inodeSize, err := recheckFormattingOptionParameter(context, internal.InodeSizeKey, internal.FileSystemConfigs, fsType)
	if err != nil {
		return nil, err
	}
	bytesPerInode, err := recheckFormattingOptionParameter(context, internal.BytesPerInodeKey, internal.FileSystemConfigs, fsType)
	if err != nil {
		return nil, err
	}
	numInodes, err := recheckFormattingOptionParameter(context, internal.NumberOfInodesKey, internal.FileSystemConfigs, fsType)
	if err != nil {
		return nil, err
	}
	ext4BigAlloc, err := recheckFormattingOptionParameter(context, internal.Ext4BigAllocKey, internal.FileSystemConfigs, fsType)
	if err != nil {
		return nil, err
	}
	ext4ClusterSize, err := recheckFormattingOptionParameter(context, internal.Ext4ClusterSizeKey, internal.FileSystemConfigs, fsType)
	if err != nil {
		return nil, err
	}

	formatOptions := []string{}
	if len(blockSize) > 0 {
		if fsType == internal.FSTypeXfs {
			blockSize = "size=" + blockSize
		}
		formatOptions = append(formatOptions, "-b", blockSize)
	}
	if len(inodeSize) > 0 {
		option := "-I"
		if fsType == internal.FSTypeXfs {
			option, inodeSize = "-i", "size="+inodeSize
		}
		formatOptions = append(formatOptions, option, inodeSize)
	}
	if len(bytesPerInode) > 0 {
		formatOptions = append(formatOptions, "-i", bytesPerInode)
	}
	if len(numInodes) > 0 {
		formatOptions = append(formatOptions, "-N", numInodes)
	}
	if ext4BigAlloc == "true" {
		formatOptions = append(formatOptions, "-O", "bigalloc")
	}
	if len(ext4ClusterSize) > 0 {
		formatOptions = append(formatOptions, "-C", ext4ClusterSize)
	}

	// support mounting on old linux kernels
	needLegacySupport, err := needLegacyXFSSupport()
	if err != nil {
		return nil, err
	}
	if fsType == internal.FSTypeXfs && needLegacySupport {
		d.log.Info("[NodeStageVolume] legacy xfs support is on")
		formatOptions = append(formatOptions, "-m", "bigtime=0,inobtcount=0,reflink=0")
	}

	mountOptions := collectMountOptions(fsType, mountVolume.GetMountFlags(), []string{})

	d.log.Debug(fmt.Sprintf("[NodeStageVolume] Volume %s operation started", volumeID))
	ok = d.inFlight.Insert(volumeID)
	if !ok {
		return nil, status.Errorf(codes.Aborted, VolumeOperationAlreadyExists, volumeID)
	}
	defer func() {
		d.log.Debug(fmt.Sprintf("[NodeStageVolume] Volume %s operation completed", volumeID))
		d.inFlight.Delete(volumeID)
	}()

	devPath := fmt.Sprintf("/dev/%s/%s", vgName, request.VolumeId)
	d.log.Debug(fmt.Sprintf("[NodeStageVolume] Checking if device exists: %s", devPath))
	exists, err := d.storeManager.PathExists(devPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "[NodeStageVolume] Error checking if device exists: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "[NodeStageVolume] Device %s not found", devPath)
	}

	lvmType := context[internal.LvmTypeKey]
	lvmThinPoolName := context[internal.ThinPoolNameKey]

	d.log.Trace(fmt.Sprintf("mountOptions = %s", mountOptions))
	d.log.Trace(fmt.Sprintf("lvmType = %s", lvmType))
	d.log.Trace(fmt.Sprintf("lvmThinPoolName = %s", lvmThinPoolName))
	d.log.Trace(fmt.Sprintf("fsType = %s", fsType))

	err = d.storeManager.NodeStageVolumeFS(devPath, target, fsType, mountOptions, formatOptions, lvmType, lvmThinPoolName)
	if err != nil {
		d.log.Error(err, "[NodeStageVolume] Error mounting volume")
		return nil, status.Errorf(codes.Internal, "[NodeStageVolume] Error format device %q and mounting volume at %q: %v", devPath, target, err)
	}

	needResize, err := d.storeManager.NeedResize(devPath, target)
	if err != nil {
		d.log.Error(err, "[NodeStageVolume] Error checking if volume needs resize")
		return nil, status.Errorf(codes.Internal, "[NodeStageVolume] Error checking if the volume %q (%q) mounted at %q needs resizing: %v", volumeID, devPath, target, err)
	}

	if needResize {
		d.log.Info(fmt.Sprintf("[NodeStageVolume] Resizing volume %q (%q) mounted at %q", volumeID, devPath, target))
		err = d.storeManager.ResizeFS(target)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "[NodeStageVolume] Error resizing volume %q (%q) mounted at %q: %v", volumeID, devPath, target, err)
		}
	}

	d.log.Info(fmt.Sprintf("[NodeStageVolume] Volume %q (%q) successfully staged at %s. FsType: %s", volumeID, devPath, target, fsType))

	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *Driver) NodeUnstageVolume(_ context.Context, request *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	d.log.Debug(fmt.Sprintf("[NodeUnstageVolume] method called with request: %v", request))
	volumeID := request.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodeUnstageVolume] Volume id cannot be empty")
	}

	target := request.GetStagingTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodeUnstageVolume] Staging target path cannot be empty")
	}

	d.log.Debug(fmt.Sprintf("[NodeUnstageVolume] Volume %s operation started", volumeID))
	ok := d.inFlight.Insert(volumeID)
	if !ok {
		return nil, status.Errorf(codes.Aborted, VolumeOperationAlreadyExists, volumeID)
	}
	defer func() {
		d.log.Debug(fmt.Sprintf("[NodeUnstageVolume] Volume %s operation completed", volumeID))
		d.inFlight.Delete(volumeID)
	}()
	err := d.storeManager.Unstage(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "[NodeUnstageVolume] Error unmounting volume %q mounted at %q: %v", volumeID, target, err)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *Driver) NodePublishVolume(_ context.Context, request *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	d.log.Info("Start method NodePublishVolume")
	d.log.Trace("------------- NodePublishVolume --------------")
	d.log.Trace(request.String())
	d.log.Trace("------------- NodePublishVolume --------------")

	volumeID := request.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodePublishVolume] Volume id cannot be empty")
	}

	source := request.GetStagingTargetPath()
	if len(source) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodePublishVolume] Staging target path cannot be empty")
	}

	target := request.GetTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodePublishVolume] Target path cannot be empty")
	}

	volCap := request.GetVolumeCapability()
	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument, "[NodePublishVolume] Volume capability cannot be empty")
	}

	mountOptions := []string{"bind"}
	if request.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	vgName, ok := request.GetVolumeContext()[internal.VGNameKey]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "[NodePublishVolume] Volume group name cannot be empty")
	}

	devPath := fmt.Sprintf("/dev/%s/%s", vgName, request.VolumeId)
	d.log.Debug(fmt.Sprintf("[NodePublishVolume] Checking if device exists: %s", devPath))
	exists, err := d.storeManager.PathExists(devPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "[NodePublishVolume] Error checking if device exists: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "[NodePublishVolume] Device %q not found", devPath)
	}

	d.log.Debug(fmt.Sprintf("[NodePublishVolume] Volume %s operation started", volumeID))

	ok = d.inFlight.Insert(volumeID)
	if !ok {
		return nil, status.Errorf(codes.Aborted, VolumeOperationAlreadyExists, volumeID)
	}
	defer func() {
		d.log.Debug(fmt.Sprintf("[NodePublishVolume] Volume %s operation completed", volumeID))
		d.inFlight.Delete(volumeID)
	}()

	switch volCap.GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		d.log.Trace("[NodePublishVolume] Block volume detected.")

		err := d.storeManager.NodePublishVolumeBlock(devPath, target, mountOptions)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "[NodePublishVolume] Error mounting volume %q at %q: %v", devPath, target, err)
		}

	case *csi.VolumeCapability_Mount:
		d.log.Trace("[NodePublishVolume] FS type volume detected.")
		mountVolume := volCap.GetMount()
		if mountVolume == nil {
			return nil, status.Error(codes.InvalidArgument, "[NodePublishVolume] Volume capability mount cannot be empty")
		}
		fsType := mountVolume.GetFsType()
		if fsType == "" {
			fsType = defaultFsType
		}

		_, ok = ValidFSTypes[strings.ToLower(fsType)]
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, fmt.Sprintf("[NodeStageVolume] Invalid fsType: %s. Supported values: %v", fsType, ValidFSTypes))
		}

		mountOptions = collectMountOptions(fsType, mountVolume.GetMountFlags(), mountOptions)

		err := d.storeManager.NodePublishVolumeFS(source, devPath, target, fsType, mountOptions)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "[NodePublishVolume] Error bind mounting volume %q. Source: %q. Target: %q. Mount options:%v. Err: %v", volumeID, source, target, mountOptions, err)
		}
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(_ context.Context, request *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	d.log.Debug(fmt.Sprintf("[NodeUnpublishVolume] method called with request: %v", request))
	d.log.Trace("------------- NodeUnpublishVolume --------------")
	d.log.Trace(request.String())
	d.log.Trace("------------- NodeUnpublishVolume --------------")

	volumeID := request.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodeUnpublishVolume] Volume id cannot be empty")
	}

	target := request.GetTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "[NodeUnpublishVolume] Staging target path cannot be empty")
	}

	d.log.Debug(fmt.Sprintf("[NodeUnpublishVolume] Volume %s operation started", volumeID))
	ok := d.inFlight.Insert(volumeID)
	if !ok {
		return nil, status.Errorf(codes.Aborted, VolumeOperationAlreadyExists, volumeID)
	}
	defer func() {
		d.log.Debug(fmt.Sprintf("[NodeUnpublishVolume] Volume %s operation completed", volumeID))
		d.inFlight.Delete(volumeID)
	}()

	err := d.storeManager.Unpublish(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "[NodeUnpublishVolume] Error unmounting volume %q mounted at %q: %v", volumeID, target, err)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetVolumeStats(_ context.Context, _ *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	d.log.Info("method NodeGetVolumeStats")
	return &csi.NodeGetVolumeStatsResponse{}, nil
}

func (d *Driver) NodeExpandVolume(_ context.Context, request *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	d.log.Info("Call method NodeExpandVolume")

	d.log.Trace("========== NodeExpandVolume ============")
	d.log.Trace(request.String())
	d.log.Trace("========== NodeExpandVolume ============")

	volumeID := request.GetVolumeId()
	volumePath := request.GetVolumePath()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume id cannot be empty")
	}
	if len(volumePath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume Path cannot be empty")
	}

	err := d.storeManager.ResizeFS(volumePath)
	if err != nil {
		d.log.Error(err, "d.mounter.ResizeFS:")
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodeExpandVolumeResponse{}, nil
}

func (d *Driver) NodeGetCapabilities(_ context.Context, request *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	d.log.Debug(fmt.Sprintf("[NodeGetCapabilities] method called with request: %v", request))

	caps := make([]*csi.NodeServiceCapability, len(nodeCaps))
	for i, capability := range nodeCaps {
		caps[i] = &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: capability,
				},
			},
		}
	}

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: caps,
	}, nil
}

func (d *Driver) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	d.log.Info("method NodeGetInfo")
	d.log.Info(fmt.Sprintf("hostID = %s", d.hostID))

	return &csi.NodeGetInfoResponse{
		NodeId: d.hostID,
		//MaxVolumesPerNode: 10,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				internal.TopologyKey: d.hostID,
			},
		},
	}, nil
}

// collectMountOptions returns array of mount options from
// VolumeCapability_MountVolume and special mount options for
// given filesystem.
func collectMountOptions(fsType string, mountFlags, mountOptions []string) []string {
	for _, opt := range mountFlags {
		if !slices.Contains(mountOptions, opt) {
			mountOptions = append(mountOptions, opt)
		}
	}

	// By default, xfs does not allow mounting of two volumes with the same filesystem uuid.
	// Force ignore this uuid to be able to mount volume + its clone / restored snapshot on the same node.
	if fsType == internal.FSTypeXfs {
		if !slices.Contains(mountOptions, "nouuid") {
			mountOptions = append(mountOptions, "nouuid")
		}
	}

	return mountOptions
}

func recheckFormattingOptionParameter(context map[string]string, key string, fsConfigs map[string]internal.FileSystemConfig, fsType string) (value string, err error) {
	v, ok := context[key]
	if ok {
		// This check is already performed on the controller side
		// However, because it is potentially security-sensitive, we redo it here to be safe
		if isAlphanumeric := utils.StringIsAlphanumeric(v); !isAlphanumeric {
			return "", status.Errorf(codes.InvalidArgument, "Invalid %s (aborting!): %v", key, err)
		}

		// In the case that the default fstype does not support custom sizes we could
		// be using an invalid fstype, so recheck that here
		if supported := fsConfigs[strings.ToLower(fsType)].IsParameterSupported(key); !supported {
			return "", status.Errorf(codes.InvalidArgument, "Cannot use %s with fstype %s", key, fsType)
		}
	}
	return v, nil
}

func int8ToStr(arr []int8) string {
	b := make([]byte, 0, len(arr))
	for _, v := range arr {
		if v == 0x00 {
			break
		}
		b = append(b, byte(v))
	}
	return string(b)
}

func needLegacyXFSSupport() (bool, error) {
	// checking if Linux kernel version is <= 5.4
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return false, fmt.Errorf("unable to Uname kernel version: %w", err)
	}

	fullVersion := int8ToStr(uname.Release[:]) // similar to: "6.8.0-44-generic"

	parts := strings.SplitN(fullVersion, ".", 3)
	if len(parts) < 3 {
		return false, fmt.Errorf("unexpected kernel version: %s", fullVersion)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, fmt.Errorf("unexpected kernel version (major part): %s", fullVersion)
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false, fmt.Errorf("unexpected kernel version (minor part): %s", fullVersion)
	}

	return major < 5 || major == 5 && minor <= 4, nil
}
