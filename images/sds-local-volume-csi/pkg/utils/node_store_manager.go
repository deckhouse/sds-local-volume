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

package utils

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	mountutils "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
)

type NodeStoreManager interface {
	NodeStageVolumeFS(source, target string, fsType string, mountOpts []string, formatOpts []string, lvmType, lvmThinPoolName string) error
	NodePublishVolumeBlock(source, target string, mountOpts []string) error
	NodePublishVolumeFS(source, devPath, target, fsType string, mountOpts []string) error
	Unstage(target string) error
	Unpublish(target string) error
	IsNotMountPoint(target string) (bool, error)
	ResizeFS(target string) error
	PathExists(path string) (bool, error)
	NeedResize(devicePath string, deviceMountPath string) (bool, error)
}

type Store struct {
	Log         logger.Logger
	NodeStorage mountutils.SafeFormatAndMount
}

func NewStore(logger logger.Logger) *Store {
	return &Store{
		Log: logger,
		NodeStorage: mountutils.SafeFormatAndMount{
			Interface: mountutils.New("/bin/mount"),
			Exec:      utilexec.New(),
		},
	}
}

func (s *Store) NodeStageVolumeFS(source, target string, fsType string, mountOpts []string, formatOpts []string, lvmType, lvmThinPoolName string) error {
	log := s.Log.Named("NodeStageVolumeFS").With("source", source, "target", target)

	log.Trace("format parameters", "fsType", fsType, "formatOptions", formatOpts)

	log.Trace("mount parameters", "fsType", fsType, "mountOptions", mountOpts)

	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("failed to stat source device: %w", err)
	}

	if (info.Mode() & os.ModeDevice) != os.ModeDevice {
		return fmt.Errorf("[NewMount] path %s is not a device", source)
	}

	log.Trace("source mode", "mode", info.Mode().String())

	exists, err := s.PathExists(target)
	if err != nil {
		return fmt.Errorf("[PathExists] could not check if target directory %s exists: %w", target, err)
	}
	if !exists {
		log.Debug("creating the target directory")
		if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
			return fmt.Errorf("[MkdirAll] could not create target directory %s: %w", target, err)
		}
	}

	isMountPoint, err := s.NodeStorage.IsMountPoint(target)
	if err != nil {
		return fmt.Errorf("[s.NodeStorage.IsMountPoint] unable to determine mount status of %s: %w", target, err)
	}

	log.Trace("checked the target", "isMountPoint", isMountPoint)

	if isMountPoint {
		mapperSourcePath := toMapperPath(source)
		log.Trace("the target is a mount point, checking whether it already points at the source", "mapperSourcePath", mapperSourcePath)

		mountedDevicePath, _, err := mountutils.GetDeviceNameFromMount(s.NodeStorage.Interface, target)
		if err != nil {
			return fmt.Errorf("failed to find the device mounted at %s: %w", target, err)
		}
		log.Trace("found the device mounted at the target", "mountedDevicePath", mountedDevicePath)

		if mountedDevicePath != source && mountedDevicePath != mapperSourcePath {
			return fmt.Errorf("target %s is a mount point and is not mounted to source %s or %s", target, source, mapperSourcePath)
		}

		log.Trace("the target is already mounted to the source, skipping format and mount")
		return nil
	}

	if lvmType == internal.LVMTypeThin {
		log.Trace("thin volume", "thinPoolName", lvmThinPoolName)
	}
	err = s.NodeStorage.FormatAndMountSensitiveWithFormatOptions(source, target, fsType, mountOpts, nil, formatOpts)
	if err != nil {
		return fmt.Errorf("failed to FormatAndMount : %w", err)
	}

	return nil
}

func (s *Store) NodePublishVolumeBlock(source, target string, mountOpts []string) error {
	log := s.Log.Named("NodePublishVolumeBlock").With("source", source, "target", target)

	log.Trace("mount parameters", "mountOptions", mountOpts)

	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("failed to stat source device: %w", err)
	}

	if (info.Mode() & os.ModeDevice) != os.ModeDevice {
		return fmt.Errorf("[NodePublishVolumeBlock] path %s is not a device", source)
	}

	log.Trace("source mode", "mode", info.Mode().String())

	f, err := os.OpenFile(target, os.O_CREATE, os.FileMode(0644))
	if err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("[NodePublishVolumeBlock] could not create bind target for block volume %s, %w", target, err)
		}
	} else {
		_ = f.Close()
	}
	err = s.NodeStorage.Mount(source, target, "", mountOpts)
	if err != nil {
		log.Error("unable to mount the block device", logger.Err(err))
		return err
	}
	return nil
}

func (s *Store) NodePublishVolumeFS(source, devPath, target, fsType string, mountOpts []string) error {
	log := s.Log.Named("NodePublishVolumeFS").With("source", source, "target", target)
	log.Trace("mount parameters", "mountOptions", mountOpts)
	isMountPoint := false
	exists, err := s.PathExists(target)
	if err != nil {
		return fmt.Errorf("[NodePublishVolumeFS] could not check if target file %s exists: %w", target, err)
	}

	if exists {
		log.Trace("the target file already exists")
		isMountPoint, err = s.NodeStorage.IsMountPoint(target)
		if err != nil {
			return fmt.Errorf("[NodePublishVolumeFS] could not check if target file %s is a mount point: %w", target, err)
		}
	} else {
		log.Trace("creating the target file")
		if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
			return fmt.Errorf("[NodePublishVolumeFS] could not create target file %q: %w", target, err)
		}
	}

	if isMountPoint {
		log.Trace("the target directory is a mount point, checking the mount")
		err := checkMount(s, devPath, target, mountOpts)
		if err != nil {
			return fmt.Errorf("[NodePublishVolumeFS] failed to check mount info for %q: %w", target, err)
		}
		log.Trace("the target directory is already mounted to the source, skipping mount")
		return nil
	}

	err = s.NodeStorage.Mount(source, target, fsType, mountOpts)
	if err != nil {
		return fmt.Errorf("[NodePublishVolumeFS] failed to bind mount %q to %q with mount options %v: %w", source, target, mountOpts, err)
	}

	return nil
}

func (s *Store) Unpublish(target string) error {
	return s.Unstage(target)
}

func (s *Store) Unstage(target string) error {
	s.Log.Named("Unstage").Info("unmounting", "target", target)
	err := mountutils.CleanupMountPoint(target, s.NodeStorage.Interface, false)
	// Ignore the error when it contains "not mounted", because that indicates the
	// world is already in the desired state
	//
	// mount-utils attempts to detect this on its own but fails when running on
	// a read-only root filesystem
	if err == nil || strings.Contains(fmt.Sprint(err), "not mounted") {
		return nil
	}

	return err
}

func (s *Store) IsNotMountPoint(target string) (bool, error) {
	notMounted, err := s.NodeStorage.IsMountPoint(target)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return notMounted, nil
}

func (s *Store) ResizeFS(mountTarget string) error {
	log := s.Log.Named("ResizeFS").With("mountTarget", mountTarget)
	devicePath, _, err := mountutils.GetDeviceNameFromMount(s.NodeStorage.Interface, mountTarget)
	if err != nil {
		log.Error("unable to find the device mounted at the target", logger.Err(err))
		return fmt.Errorf("failed to find the device mounted at %s: %w", mountTarget, err)
	}

	log.Info("found the device to resize", "devicePath", devicePath)

	_, err = mountutils.NewResizeFs(s.NodeStorage.Exec).Resize(devicePath, mountTarget)
	if err != nil {
		log.Error("unable to resize the filesystem", logger.Err(err), slog.String("devicePath", devicePath))
		return fmt.Errorf("failed to resize filesystem %s on device %s: %w", mountTarget, devicePath, err)
	}

	log.Info("filesystem resized successfully", "devicePath", devicePath)
	return nil
}

func (s *Store) PathExists(path string) (bool, error) {
	return mountutils.PathExists(path)
}

func (s *Store) NeedResize(devicePath string, deviceMountPath string) (bool, error) {
	return mountutils.NewResizeFs(s.NodeStorage.Exec).NeedResize(devicePath, deviceMountPath)
}

func toMapperPath(devPath string) string {
	if !strings.HasPrefix(devPath, "/dev/") {
		return ""
	}

	shortPath := strings.TrimPrefix(devPath, "/dev/")
	mapperPath := strings.ReplaceAll(shortPath, "-", "--")
	mapperPath = strings.ReplaceAll(mapperPath, "/", "-")
	return "/dev/mapper/" + mapperPath
}

func checkMount(s *Store, devPath, target string, mountOpts []string) error {
	mntInfo, err := s.NodeStorage.List()
	if err != nil {
		return fmt.Errorf("[checkMount] failed to list mounts: %w", err)
	}

	for _, m := range mntInfo {
		if m.Path == target {
			mapperDevicePath := toMapperPath(devPath)
			if m.Device != devPath && m.Device != mapperDevicePath {
				return fmt.Errorf("[checkMount] device from mount point %q does not match expected source device path %s or mapper device path %s", m.Device, devPath, mapperDevicePath)
			}
			s.Log.Trace("the mount point is mounted to a device", "target", target, "device", m.Device)

			if slices.Contains(mountOpts, "ro") {
				if !slices.Contains(m.Opts, "ro") {
					return fmt.Errorf("[checkMount] passed mount options contain 'ro' but mount options from mount point %q do not", target)
				}
				s.Log.Trace("the mount point is mounted read-only", "target", target)
			}
			s.Log.Trace("the mount point is mounted to a device", "target", target, "device", m.Device, "mountOptions", m.Opts)

			return nil
		}
	}

	return fmt.Errorf("[checkMount] mount point %q not found in mount info", target)
}
