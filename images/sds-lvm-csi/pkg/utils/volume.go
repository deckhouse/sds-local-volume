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
	mu "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
	"os"
	"sds-lvm-csi/pkg/logger"
	"time"
)

type Store struct {
	Log     *logger.Logger
	Mounter mu.SafeFormatAndMount
}

type NewMounter interface {
	Mount(source, target, fsType string, readonly bool, mntOpts []string) error
	Unmount(target string) error
	IsNotMountPoint(target string) (bool, error)
}

func NewStore(logger *logger.Logger) *Store {
	return &Store{
		Log: logger,
		Mounter: mu.SafeFormatAndMount{
			Interface: mu.New("/bin/mount"),
			Exec:      utilexec.New(),
		},
	}
}

func (s *Store) Mount(source, target string, isBlock bool, fsType string, readonly bool, mntOpts []string) error {
	s.Log.Info(" ----== Node Mount ==---- ")

	s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ Mount options ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
	s.Log.Info(fmt.Sprintf("[mount] params source=%s target=%s fs=%s blockMode=%t mountOptions=%v", source, target, fsType, isBlock, mntOpts))
	s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ Mount options ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("failed to stat source device: %w", err)
	}

	if (info.Mode() & os.ModeDevice) != os.ModeDevice {
		return fmt.Errorf("[NewMount] path %s is not a device", source)
	}

	s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ MODE SOURCE ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
	s.Log.Info(info.Mode().String())
	s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ MODE SOURCE  ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

	s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ isBlock ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
	s.Log.Info(fmt.Sprintf("%t ", isBlock))
	s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ isBlock  ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

	if !isBlock {
		s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ FS MOUNT ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		s.Log.Info("-----------------== start MkdirAll ==-----------------")
		s.Log.Info("mkdir create dir =" + target)
		if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
			return fmt.Errorf("[MkdirAll] could not create target directory %s, %v", target, err)
		}
		s.Log.Info("-----------------== stop MkdirAll ==-----------------")

		needsMount, err := s.Mounter.IsMountPoint(target)
		if err != nil {
			return fmt.Errorf("[s.Mounter.IsMountPoint] unable to determine mount status of %s %v", target, err)
		}

		s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ needsMount ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		s.Log.Info(fmt.Sprintf("%t", needsMount))
		s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ needsMount  ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")

		//todo
		//if !needsMount {
		//	return nil
		//}

		s.Log.Info("-----------------== start FormatAndMount ==---------------")
		err = s.Mounter.FormatAndMount(source, target, fsType, mntOpts)
		if err != nil {
			return fmt.Errorf("failed to FormatAndMount : %w", err)
		}
		s.Log.Info("-----------------== stop FormatAndMount ==---------------")
		return nil
	}

	if isBlock {
		s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ BLOCK MOUNT ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		s.Log.Info("-----------------== start Create File ==---------------")
		f, err := os.OpenFile(target, os.O_CREATE, os.FileMode(0666))
		if err != nil {
			if !os.IsExist(err) {
				return fmt.Errorf("could not create bind target for block volume %s, %w", target, err)
			}
		} else {
			_ = f.Close()
		}
		s.Log.Info("-----------------== stop Create File ==---------------")
		s.Log.Info("-----------------== start Mount ==---------------")
		err = s.Mounter.Mount(source, target, fsType, mntOpts)
		if err != nil {
			s.Log.Error(err, "block mount error :")
			return err
		}
		s.Log.Info("-----------------== stop Mount ==---------------")
		s.Log.Info("≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈ BLOCK MOUNT ≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈≈")
		return nil
	}
	s.Log.Info("-----------------== Final ==---------------")
	return nil
}

func (s *Store) Unmount(target string) error {
	s.Log.Info(fmt.Sprintf("[unmount volume] target=%s", target))

	err := s.Mounter.Unmount(target)
	if err != nil {
		s.Log.Error(err, "[s.Mounter.Unmount]: ")
		return err
	}
	time.Sleep(time.Second * 1)
	return nil
}

func (s *Store) IsNotMountPoint(target string) (bool, error) {
	notMounted, err := s.Mounter.IsMountPoint(target)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return notMounted, nil
}
