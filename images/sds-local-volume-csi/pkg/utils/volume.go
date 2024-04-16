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
	"fmt"
	"os"
	"sds-local-volume-csi/internal"
	"sds-local-volume-csi/pkg/logger"
	"slices"
	"strings"

	mountutils "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
)

type NodeStoreManager interface {
	FormatAndMount(source, target string, isBlock bool, fsType string, readonly bool, mountOpts []string, lvmType, lvmThinPoolName string) error
	Unstage(target string) error
	Unpublish(target string) error
	IsNotMountPoint(target string) (bool, error)
	ResizeFS(target string) error
	PathExists(path string) (bool, error)
	NeedResize(devicePath string, deviceMountPath string) (bool, error)
	BindMount(source, target, fsType string, mountOpts []string) error
}

type Store struct {
	Log         *logger.Logger
	NodeStorage mountutils.SafeFormatAndMount
}

func NewStore(logger *logger.Logger) *Store {
	return &Store{
		Log: logger,
		NodeStorage: mountutils.SafeFormatAndMount{
			Interface: mountutils.New("/bin/mount"),
			Exec:      utilexec.New(),
		},
	}
}

func (s *Store) FormatAndMount(devSourcePath, target string, isBlock bool, fsType string, readonly bool, mntOpts []string, lvmType, lvmThinPoolName string) error {
	s.Log.Info(" ----== Node Mount ==---- ")

	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ Mount options ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
	s.Log.Trace(fmt.Sprintf("[mount] params source=%s target=%s fs=%s blockMode=%t mountOptions=%v", devSourcePath, target, fsType, isBlock, mntOpts))
	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ Mount options ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

	info, err := os.Stat(devSourcePath)
	if err != nil {
		return fmt.Errorf("failed to stat source device: %w", err)
	}

	if (info.Mode() & os.ModeDevice) != os.ModeDevice {
		return fmt.Errorf("[NewMount] path %s is not a device", devSourcePath)
	}

	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ MODE SOURCE ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
	s.Log.Trace(info.Mode().String())
	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ MODE SOURCE  ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ isBlock ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
	s.Log.Trace(fmt.Sprintf("%t ", isBlock))
	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ isBlock  ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

	if !isBlock {
		s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ FS MOUNT ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		s.Log.Trace("-----------------== start MkdirAll ==-----------------")
		s.Log.Trace("mkdir create dir =" + target)
		exists, err := s.PathExists(target)
		if err != nil {
			return fmt.Errorf("[PathExists] could not check if target directory %s exists: %w", target, err)
		}
		if !exists {
			s.Log.Debug(fmt.Sprintf("Creating target directory %s", target))
			if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
				return fmt.Errorf("[MkdirAll] could not create target directory %s: %w", target, err)
			}
		}
		s.Log.Trace("-----------------== stop MkdirAll ==-----------------")

		isMountPoint, err := s.NodeStorage.IsMountPoint(target)
		if err != nil {
			return fmt.Errorf("[s.NodeStorage.IsMountPoint] unable to determine mount status of %s: %w", target, err)
		}

		s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ isMountPoint ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		s.Log.Trace(fmt.Sprintf("%t", isMountPoint))
		s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ isMountPoint  ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

		if isMountPoint {
			mapperSourcePath := toMapperPath(devSourcePath)
			s.Log.Trace(fmt.Sprintf("Target %s is a mount point. Checking if it is already mounted to source %s or %s", target, devSourcePath, mapperSourcePath))

			mountedDevicePath, _, err := mountutils.GetDeviceNameFromMount(s.NodeStorage.Interface, target)
			if err != nil {
				return fmt.Errorf("failed to find the device mounted at %s: %w", target, err)
			}
			s.Log.Trace(fmt.Sprintf("Found device mounted at %s: %s", target, mountedDevicePath))

			if mountedDevicePath != devSourcePath && mountedDevicePath != mapperSourcePath {
				return fmt.Errorf("target %s is a mount point and is not mounted to source %s or %s", target, devSourcePath, mapperSourcePath)
			}

			s.Log.Trace(fmt.Sprintf("Target %s is a mount point and already mounted to source %s. Skipping FormatAndMount without any checks", target, devSourcePath))
			return nil
		}

		s.Log.Trace("-----------------== start FormatAndMount ==---------------")

		if lvmType == internal.LVMTypeThin {
			s.Log.Trace(fmt.Sprintf("LVM type is Thin. Thin pool name: %s", lvmThinPoolName))
		}
		err = s.NodeStorage.FormatAndMount(devSourcePath, target, fsType, mntOpts)
		if err != nil {
			return fmt.Errorf("failed to FormatAndMount : %w", err)
		}
		s.Log.Trace("-----------------== stop FormatAndMount ==---------------")
		return nil
	}

	if isBlock {
		s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ BLOCK MOUNT ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		s.Log.Trace("-----------------== start Create File ==---------------")
		f, err := os.OpenFile(target, os.O_CREATE, os.FileMode(0666))
		if err != nil {
			if !os.IsExist(err) {
				return fmt.Errorf("could not create bind target for block volume %s, %w", target, err)
			}
		} else {
			_ = f.Close()
		}
		s.Log.Trace("-----------------== stop Create File ==---------------")
		s.Log.Trace("-----------------== start Mount ==---------------")
		err = s.NodeStorage.Mount(devSourcePath, target, fsType, mntOpts)
		if err != nil {
			s.Log.Error(err, "block mount error :")
			return err
		}
		s.Log.Trace("-----------------== stop Mount ==---------------")
		s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ BLOCK MOUNT ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		return nil
	}
	s.Log.Info("-----------------== Final ==---------------")
	return nil
}

func (s *Store) Unpublish(target string) error {
	return s.Unstage(target)
}

func (s *Store) Unstage(target string) error {
	s.Log.Info(fmt.Sprintf("[unmount volume] target=%s", target))
	err := mountutils.CleanupMountPoint(target, s.NodeStorage.Interface, false)
	// Ignore the error when it contains "not mounted", because that indicates the
	// world is already in the desired state
	//
	// mount-utils attempts to detect this on its own but fails when running on
	// a read-only root filesystem
	if err == nil || strings.Contains(fmt.Sprint(err), "not mounted") {
		return nil
	} else {
		return err
	}
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
	s.Log.Info(" ----== Resize FS ==---- ")
	devicePath, _, err := mountutils.GetDeviceNameFromMount(s.NodeStorage.Interface, mountTarget)
	if err != nil {
		s.Log.Error(err, "Failed to find the device mounted at mountTarget", "mountTarget", mountTarget)
		return fmt.Errorf("failed to find the device mounted at %s: %w", mountTarget, err)
	}

	s.Log.Info("Found device for resizing", "devicePath", devicePath, "mountTarget", mountTarget)

	_, err = mountutils.NewResizeFs(s.NodeStorage.Exec).Resize(devicePath, mountTarget)
	if err != nil {
		s.Log.Error(err, "Failed to resize filesystem", "devicePath", devicePath, "mountTarget", mountTarget)
		return fmt.Errorf("failed to resize filesystem %s on device %s: %w", mountTarget, devicePath, err)
	}

	s.Log.Info("Filesystem resized successfully", "devicePath", devicePath)
	return nil
}

func (s *Store) PathExists(path string) (bool, error) {
	return mountutils.PathExists(path)
}

func (s *Store) NeedResize(devicePath string, deviceMountPath string) (bool, error) {
	return mountutils.NewResizeFs(s.NodeStorage.Exec).NeedResize(devicePath, deviceMountPath)
}

func (s *Store) BindMount(source, target, fsType string, mountOpts []string) error {
	s.Log.Info(" ----== Bind Mount ==---- ")
	s.Log.Trace(fmt.Sprintf("[BindMount] params source=%q target=%q mountOptions=%v", source, target, mountOpts))
	isMountPoint := false
	exists, err := s.PathExists(target)
	if err != nil {
		return fmt.Errorf("[BindMount] could not check if target directory %s exists: %w", target, err)
	}

	if exists {
		s.Log.Trace(fmt.Sprintf("[BindMount] target directory %s already exists", target))
		isMountPoint, err = s.NodeStorage.IsMountPoint(target)
		if err != nil {
			return fmt.Errorf("[BindMount] could not check if target directory %s is a mount point: %w", target, err)
		}
	} else {
		s.Log.Trace(fmt.Sprintf("[BindMount] creating target directory %q", target))
		if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
			return fmt.Errorf("[BindMount] could not create target directory %q: %w", target, err)
		}
	}

	if isMountPoint {
		s.Log.Trace(fmt.Sprintf("[BindMount] target directory %q is a mount point. Check mount", target))
		err := checkMount(s, source, target, mountOpts)
		if err != nil {
			return fmt.Errorf("[BindMount] failed to check mount info for %q: %w", target, err)
		}
		s.Log.Trace(fmt.Sprintf("[BindMount] target directory %q is a mount point and already mounted to source %q", target, source))
		return nil
	}
	// if err != nil {

	err = s.NodeStorage.Interface.Mount(source, target, fsType, mountOpts)
	if err != nil {
		return fmt.Errorf("[BindMount] failed to bind mount %q to %q with mount options %v: %w", source, target, mountOpts, err)
	}

	return nil
}

func toMapperPath(devPath string) string {
	if !strings.HasPrefix(devPath, "/dev/") {
		return ""
	}

	shortPath := strings.TrimPrefix(devPath, "/dev/")
	mapperPath := strings.Replace(shortPath, "-", "--", -1)
	mapperPath = strings.Replace(mapperPath, "/", "-", -1)
	return "/dev/mapper/" + mapperPath
}

func checkMount(s *Store, source, target string, mountOpts []string) error {
	mntInfo, err := s.NodeStorage.Interface.List()
	if err != nil {
		return fmt.Errorf("[checkMount] failed to list mounts: %w", err)
	}

	for _, m := range mntInfo {
		if m.Path == target {
			if m.Device != source {
				return fmt.Errorf("[checkMount] device from mount point %q does not match expected source %q", m.Device, source)
			}

			if slices.Contains(mountOpts, "ro") {
				if !slices.Contains(m.Opts, "ro") {
					return fmt.Errorf("[checkMount] passed mount options contain 'ro' but mount options from mount point %q do not", target)
				}

				if slices.Equal(m.Opts, mountOpts) {
					return fmt.Errorf("mount options %v do not match expected mount options %v", m.Opts, mountOpts)
				}
			}

			return nil
		}
	}

	return fmt.Errorf("[checkMount] mount point %q not found in mount info", target)
}
