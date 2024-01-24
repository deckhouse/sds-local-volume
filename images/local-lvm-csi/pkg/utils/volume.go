package utils

import (
	"fmt"
	utilexec "k8s.io/utils/exec"
	"k8s.io/utils/mount"
	"local-lvm-csi/pkg/logger"
	"os"
)

type Store struct {
	Log     *logger.Logger
	Mounter mount.SafeFormatAndMount
}

type NewMounter interface {
	Mount(source, target, fsType string, readonly bool, mntOpts []string) error
	Unmount(target string) error
	IsNotMountPoint(target string) (bool, error)
}

func NewStore(logger *logger.Logger) *Store {
	return &Store{
		Log: logger,
		Mounter: mount.SafeFormatAndMount{
			Interface: mount.New("/bin/mount"),
			Exec:      utilexec.New(),
		},
	}
}

func (s *Store) Mount(source, target, fsType string, readonly bool, mntOpts []string) error {
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
		return fmt.Errorf("[NewMount] path %s is not a device", source) //nolint:goerr113
	}

	fmt.Println("----======== info ============----")
	fmt.Println(info)
	fmt.Println("----======== info ============----")

	if readonly {
		mntOpts = append(mntOpts, "ro")
		//todo set RO
	} else {
		//todo set RW
	}

	if !block {
		if err := os.MkdirAll(target, os.FileMode(0755)); err != nil {
			return fmt.Errorf("[NewMount] could not create target directory %s, %v", target, err)
		}
	} else {
		f, err := os.OpenFile(target, os.O_CREATE, os.FileMode(0644))
		if err != nil {
			if !os.IsExist(err) {
				return fmt.Errorf("[NewMount] could not create bind target for block volume %s, %w", target, err)
			}
		} else {
			_ = f.Close()
		}
	}

	needsMount, err := mount.IsNotMountPoint(s.Mounter, target)
	if err != nil {
		return fmt.Errorf("[NewMount] unable to determine mount status of %s %v", target, err)
	}

	if !needsMount {
		return nil
	}

	err = s.Mounter.Mount(source, target, fsType, mntOpts)
	if err != nil {
		return err
	}

	return nil
}

func (s *Store) Unmount(target string) error {
	s.Log.Info(fmt.Sprintf("[unmount volume] target=%s", target))
	err := mount.CleanupMountPoint(target, s.Mounter, true)
	if err != nil {
		return fmt.Errorf("[NewUnmount] unable to cleanup mount point: %w", err)
	}
	return nil
}

func (s *Store) IsNotMountPoint(target string) (bool, error) {
	notMounted, err := mount.IsNotMountPoint(s.Mounter, target)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return notMounted, nil
}
