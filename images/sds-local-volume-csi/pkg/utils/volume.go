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
	"sds-local-volume-csi/pkg/logger"
	"time"

	mu "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
)

type NodeStoreManager interface {
	Mount(source, target string, isBlock bool, fsType string, readonly bool, mntOpts []string) error
	Unmount(target string) error
	IsNotMountPoint(target string) (bool, error)
	ResizeFS(target string) error
}

type Store struct {
	Log         *logger.Logger
	NodeStorage mu.SafeFormatAndMount
}

func NewStore(logger *logger.Logger) *Store {
	return &Store{
		Log: logger,
		NodeStorage: mu.SafeFormatAndMount{
			Interface: mu.New("/bin/mount"),
			Exec:      utilexec.New(),
		},
	}
}

func (s *Store) Mount(source, target string, isBlock bool, fsType string, readonly bool, mntOpts []string) error {
	s.Log.Info(" ----== Node Mount ==---- ")

	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ Mount options ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
	s.Log.Trace(fmt.Sprintf("[mount] params source=%s target=%s fs=%s blockMode=%t mountOptions=%v", source, target, fsType, isBlock, mntOpts))
	s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ Mount options ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("failed to stat source device: %w", err)
	}

	if (info.Mode() & os.ModeDevice) != os.ModeDevice {
		return fmt.Errorf("[NewMount] path %s is not a device", source)
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
		if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
			return fmt.Errorf("[MkdirAll] could not create target directory %s, %v", target, err)
		}
		s.Log.Trace("-----------------== stop MkdirAll ==-----------------")

		needsMount, err := s.NodeStorage.IsMountPoint(target)
		if err != nil {
			return fmt.Errorf("[s.NodeStorage.IsMountPoint] unable to determine mount status of %s %v", target, err)
		}

		s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ needsMount ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		s.Log.Trace(fmt.Sprintf("%t", needsMount))
		s.Log.Trace("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ needsMount  ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

		//todo
		//if !needsMount {
		//	return nil
		//}

		s.Log.Trace("-----------------== start FormatAndMount ==---------------")
		err = s.NodeStorage.FormatAndMount(source, target, fsType, mntOpts)
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
		err = s.NodeStorage.Mount(source, target, fsType, mntOpts)
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

func (s *Store) Unmount(target string) error {
	s.Log.Info(fmt.Sprintf("[unmount volume] target=%s", target))

	err := s.NodeStorage.Unmount(target)
	if err != nil {
		s.Log.Error(err, "[s.NodeStorage.Unmount]: ")
		return err
	}
	time.Sleep(time.Second * 1)
	return nil
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
	devicePath, _, err := mu.GetDeviceNameFromMount(s.NodeStorage.Interface, mountTarget)
	if err != nil {
		s.Log.Error(err, "Failed to find the device mounted at mountTarget", "mountTarget", mountTarget)
		return fmt.Errorf("failed to find the device mounted at %s: %w", mountTarget, err)
	}

	s.Log.Info("Found device for resizing", "devicePath", devicePath, "mountTarget", mountTarget)

	_, err = mu.NewResizeFs(s.NodeStorage.Exec).Resize(devicePath, mountTarget)
	if err != nil {
		s.Log.Error(err, "Failed to resize filesystem", "devicePath", devicePath, "mountTarget", mountTarget)
		return fmt.Errorf("failed to resize filesystem %s on device %s: %w", mountTarget, devicePath, err)
	}

	s.Log.Info("Filesystem resized successfully", "devicePath", devicePath)
	return nil
}
