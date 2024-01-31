/*
Copyright 2023 Flant JSC

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
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"sds-lvm-csi/pkg/utils"
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

	d.log.Info("------------- NodePublishVolume --------------")
	d.log.Info(request.String())
	d.log.Info("------------- NodePublishVolume --------------")

	d.log.Info("------------- Extend params --------------")
	fmt.Println("request.GetVolumeCapability().GetBlock():", request.GetVolumeCapability().GetBlock())
	fmt.Println("request.GetVolumeCapability().GetMount():", request.GetVolumeCapability().GetMount())
	d.log.Info("------------- Extend params  --------------")

	vgName := make(map[string]string)
	err := yaml.Unmarshal([]byte(request.GetVolumeContext()[lvmSelector]), &vgName)
	if err != nil {
		d.log.Error(err, "unmarshal labels")
		return nil, status.Error(codes.Internal, "Unmarshal volume context")
	}

	dev := fmt.Sprintf("/dev/%s/%s", request.GetVolumeContext()[VGNameKey], request.VolumeId)
	fsType := request.VolumeCapability.GetMount().FsType

	if fsType != "ext4" {
		fmt.Println("request.VolumeCapability.GetMount().FsType =", request.VolumeCapability.GetMount().FsType)
	}

	d.log.Info("vgName[VGNameKey] = ", request.GetVolumeContext()[VGNameKey])
	d.log.Info(fmt.Sprintf("[mount] params dev=%s target=%s fs=%s", dev, request.GetTargetPath(), fsType))

	///------------- External code ----------------

	d.log.Info("///------------- External code ----------------")

	command, _, err := utils.LVExist(request.GetVolumeContext()[VGNameKey], request.VolumeId)
	d.log.Info(command)
	if err != nil {
		d.log.Error(err, " error utils.LVExist")

		d.log.Info("LV Create START")
		deviceSize, err := resource.ParseQuantity("1000000000")
		if err != nil {
			fmt.Println(err)
		}

		lv, err := utils.CreateLV(deviceSize.String(), request.VolumeId, request.GetVolumeContext()[VGNameKey])
		if err != nil {
			d.log.Error(err, "")
		}
		d.log.Info(fmt.Sprintf("[lv create] size=%s pvc=%s vg=%s", deviceSize.String(), request.VolumeId, request.GetVolumeContext()[VGNameKey]))
		fmt.Println("lv create command = ", lv)
		if err != nil {
			fmt.Println(err)
		}

		d.log.Info("LV Create STOP")
	}

	d.log.Info("///------------- External code ----------------")

	///------------- External code ----------------

	var mountOptions []string
	if request.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	err = d.mounter.Mount(dev, request.GetTargetPath(), fsType, false, mountOptions)
	if err != nil {
		d.log.Error(err, " d.mounter.Mount ")
		return nil, err
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, request *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	d.log.Info("method NodeUnpublishVolume")
	fmt.Println("------------- NodeUnpublishVolume --------------")
	fmt.Println(request)
	fmt.Println("------------- NodeUnpublishVolume --------------")

	err := d.mounter.Unmount(request.GetTargetPath())
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
	d.log.Info("method NodeExpandVolume")
	return &csi.NodeExpandVolumeResponse{}, nil
}

func (d *Driver) NodeGetCapabilities(ctx context.Context, request *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	d.log.Info("method NodeGetCapabilities")
	return &csi.NodeGetCapabilitiesResponse{}, nil
}

func (d *Driver) NodeGetInfo(ctx context.Context, request *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	d.log.Info("method NodeGetInfo 0 2")
	d.log.Info("hostID = ", d.hostID)

	return &csi.NodeGetInfoResponse{
		NodeId:            d.hostID,
		MaxVolumesPerNode: 10,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				topologyKey: d.hostID,
			},
		},
	}, nil
}
