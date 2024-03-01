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
	"sds-local-volume-csi/internal"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *Driver) NodeStageVolume(ctx context.Context, request *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	d.log.Info("method NodeStageVolume")
	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *Driver) NodeUnstageVolume(ctx context.Context, request *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	d.log.Info("method NodeUnstageVolume")
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *Driver) NodePublishVolume(ctx context.Context, request *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	d.log.Info("method NodePublishVolume")
	d.log.Trace("------------- NodePublishVolume --------------")
	d.log.Trace(request.String())
	d.log.Trace("------------- NodePublishVolume --------------")

	dev := fmt.Sprintf("/dev/%s/%s", request.GetVolumeContext()[internal.VGNameKey], request.VolumeId)

	var mountOptions []string
	if request.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}
	var fsType string
	var IsBlock bool

	if request.GetVolumeCapability().GetBlock() != nil {
		mountOptions = []string{"bind"}
		IsBlock = true
	}

	if mnt := request.GetVolumeCapability().GetMount(); mnt != nil {
		fsType = request.VolumeCapability.GetMount().FsType
		mountOptions = append(mountOptions, mnt.GetMountFlags()...)
	}

	err := d.storeManager.Mount(dev, request.GetTargetPath(), IsBlock, fsType, false, mountOptions)
	if err != nil {
		d.log.Error(err, "d.mounter.Mount :")
		return nil, err
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, request *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	d.log.Info("method NodeUnpublishVolume")
	d.log.Trace("------------- NodeUnpublishVolume --------------")
	d.log.Trace(request.String())
	d.log.Trace("------------- NodeUnpublishVolume --------------")

	err := d.storeManager.Unmount(request.GetTargetPath())
	if err != nil {
		d.log.Error(err, "NodeUnpublishVolume err ")
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetVolumeStats(ctx context.Context, request *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	d.log.Info("method NodeGetVolumeStats")
	return &csi.NodeGetVolumeStatsResponse{}, nil
}

func (d *Driver) NodeExpandVolume(ctx context.Context, request *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
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

func (d *Driver) NodeGetCapabilities(ctx context.Context, request *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	d.log.Info("method NodeGetCapabilities")

	capabilities := []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
	}

	csiCaps := make([]*csi.NodeServiceCapability, len(capabilities))
	for i, capability := range capabilities {
		csiCaps[i] = &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: capability,
				},
			},
		}
	}

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: csiCaps,
	}, nil
}

func (d *Driver) NodeGetInfo(ctx context.Context, request *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	d.log.Info("method NodeGetInfo")
	d.log.Info("hostID = ", d.hostID)

	return &csi.NodeGetInfoResponse{
		NodeId:            d.hostID,
		MaxVolumesPerNode: 10,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				internal.TopologyKey: d.hostID,
			},
		},
	}, nil
}
