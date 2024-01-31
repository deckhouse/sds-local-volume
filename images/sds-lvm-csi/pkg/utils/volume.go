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

func (s *Store) Mount(source, target, fsType string, readonly bool, mntOpts []string) error {
	s.Log.Info(" ----== Node Mount ==---- ")

	var block bool
	if fsType == "" {
		block = true
	}
	s.Log.Info(fmt.Sprintf("[mount volune] source=%s target=%s moutnOpt=%s filesystem=%s blockAccessMode=%t",
		source, target, mntOpts, fsType, block))

	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("failed to stat source device: %w", err)
	}

	if (info.Mode() & os.ModeDevice) != os.ModeDevice {
		return fmt.Errorf("[NewMount] path %s is not a device", source)
	}

	if readonly {
		mntOpts = append(mntOpts, "ro")
		//todo set RO
	} else {
		//todo set RW
	}

	//if !block {
	fmt.Println("======== start MkdirAll ========")
	fmt.Println("create dir =", target)
	if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
		return fmt.Errorf("[NewMount] could not create target directory %s, %v", target, err)
	}
	fmt.Println("======== stop  MkdirAll ========")

	fmt.Println("-----------------== IsNotMountPoint ==--------------- 3 ")

	needsMount, err := s.Mounter.IsMountPoint(target)
	if err != nil {
		return fmt.Errorf("[NewMount] unable to determine mount status of %s %v", target, err)
	}

	if !needsMount {
		return nil
	}

	fmt.Println("-----------------== FormatAndMount ==--------------- 4 ")

	err = s.Mounter.FormatAndMount(source, target, fsType, mntOpts)
	if err != nil {
		return fmt.Errorf("failed to FormatAndMount : %w", err)
	}

	fmt.Println("-----------------== Final ==--------------- 5 ")

	//todo sleep 60
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
