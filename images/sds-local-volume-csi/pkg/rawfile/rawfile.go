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

// Package rawfile provides functionality for managing raw files
// mounted as loop devices for use as CSI volumes.
package rawfile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
)

const (
	// DefaultDirMode is the default permission mode for directories
	DefaultDirMode = 0750
	// DefaultFileMode is the default permission mode for raw files
	DefaultFileMode = 0600
)

var (
	ErrVolumeNotFound    = errors.New("volume not found")
	ErrVolumeExists      = errors.New("volume already exists")
	ErrLoopDeviceNotFound = errors.New("loop device not found")
)

// Manager handles raw file operations for loop device volumes
type Manager struct {
	log     *logger.Logger
	dataDir string
}

// NewManager creates a new rawfile Manager
func NewManager(log *logger.Logger, dataDir string) *Manager {
	return &Manager{
		log:     log,
		dataDir: dataDir,
	}
}

// VolumeInfo contains information about a rawfile volume
type VolumeInfo struct {
	VolumeID   string
	Path       string
	Size       int64
	DevicePath string
}

// GetVolumePath returns the path to the raw file for a given volume ID
func (m *Manager) GetVolumePath(volumeID string) string {
	return filepath.Join(m.dataDir, volumeID, "disk.img")
}

// GetVolumeDir returns the directory containing the volume data
func (m *Manager) GetVolumeDir(volumeID string) string {
	return filepath.Join(m.dataDir, volumeID)
}

// CreateVolume creates a new raw file volume with the specified size
func (m *Manager) CreateVolume(volumeID string, sizeBytes int64, sparse bool) (*VolumeInfo, error) {
	m.log.Info(fmt.Sprintf("[RawFile] Creating volume %s with size %d bytes (sparse=%t)", volumeID, sizeBytes, sparse))

	volumeDir := m.GetVolumeDir(volumeID)
	volumePath := m.GetVolumePath(volumeID)

	// Check if volume already exists
	if _, err := os.Stat(volumePath); err == nil {
		m.log.Warning(fmt.Sprintf("[RawFile] Volume %s already exists at %s", volumeID, volumePath))
		info, err := m.GetVolumeInfo(volumeID)
		if err != nil {
			return nil, fmt.Errorf("volume exists but failed to get info: %w", err)
		}
		return info, nil
	}

	// Create volume directory
	if err := os.MkdirAll(volumeDir, DefaultDirMode); err != nil {
		return nil, fmt.Errorf("failed to create volume directory %s: %w", volumeDir, err)
	}

	// Create the raw file
	if err := m.createRawFile(volumePath, sizeBytes, sparse); err != nil {
		// Cleanup on failure
		os.RemoveAll(volumeDir)
		return nil, fmt.Errorf("failed to create raw file: %w", err)
	}

	m.log.Info(fmt.Sprintf("[RawFile] Successfully created volume %s at %s", volumeID, volumePath))

	return &VolumeInfo{
		VolumeID: volumeID,
		Path:     volumePath,
		Size:     sizeBytes,
	}, nil
}

// createRawFile creates a raw file with the specified size
func (m *Manager) createRawFile(path string, sizeBytes int64, sparse bool) error {
	if sparse {
		// Create sparse file using truncate
		file, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("failed to create file: %w", err)
		}
		defer file.Close()

		if err := file.Truncate(sizeBytes); err != nil {
			return fmt.Errorf("failed to truncate file to size %d: %w", sizeBytes, err)
		}

		// Set file permissions
		if err := os.Chmod(path, DefaultFileMode); err != nil {
			return fmt.Errorf("failed to set file permissions: %w", err)
		}
	} else {
		// Create pre-allocated file using fallocate
		if err := m.fallocate(path, sizeBytes); err != nil {
			return fmt.Errorf("fallocate failed: %w", err)
		}
	}

	return nil
}

// fallocate creates a pre-allocated file using the fallocate command
func (m *Manager) fallocate(path string, sizeBytes int64) error {
	// First create the file
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	file.Close()

	// Set permissions before allocating
	if err := os.Chmod(path, DefaultFileMode); err != nil {
		return fmt.Errorf("failed to set file permissions: %w", err)
	}

	// Use fallocate command for pre-allocation
	cmd := exec.Command("fallocate", "-l", strconv.FormatInt(sizeBytes, 10), path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback to Go-based allocation if fallocate fails (e.g., on some filesystems like NFS)
		m.log.Warning(fmt.Sprintf("[RawFile] fallocate failed, falling back to Go-based allocation: %s", string(output)))
		return m.goAllocate(path, sizeBytes)
	}

	return nil
}

// goAllocate creates a pre-allocated file using pure Go as a fallback
// when fallocate is not supported by the filesystem
func (m *Manager) goAllocate(path string, sizeBytes int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, DefaultFileMode)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Write zeros in chunks to avoid memory issues
	const chunkSize = 4 * 1024 * 1024 // 4MB chunks
	zeroChunk := make([]byte, chunkSize)
	
	remaining := sizeBytes
	for remaining > 0 {
		writeSize := int64(chunkSize)
		if remaining < writeSize {
			writeSize = remaining
		}
		
		n, err := file.Write(zeroChunk[:writeSize])
		if err != nil {
			return fmt.Errorf("failed to write zeros: %w", err)
		}
		remaining -= int64(n)
	}

	// Sync to disk
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}

	return nil
}

// DeleteVolume removes a raw file volume and detaches any loop device
func (m *Manager) DeleteVolume(volumeID string) error {
	m.log.Info(fmt.Sprintf("[RawFile] Deleting volume %s", volumeID))

	volumePath := m.GetVolumePath(volumeID)
	volumeDir := m.GetVolumeDir(volumeID)

	// Check if the volume exists
	if _, err := os.Stat(volumePath); os.IsNotExist(err) {
		m.log.Warning(fmt.Sprintf("[RawFile] Volume %s does not exist, nothing to delete", volumeID))
		return nil
	}

	// Detach any associated loop device
	if err := m.DetachLoopDevice(volumePath); err != nil {
		m.log.Warning(fmt.Sprintf("[RawFile] Failed to detach loop device for %s: %v", volumeID, err))
	}

	// Remove the volume directory and all contents
	if err := os.RemoveAll(volumeDir); err != nil {
		return fmt.Errorf("failed to remove volume directory %s: %w", volumeDir, err)
	}

	m.log.Info(fmt.Sprintf("[RawFile] Successfully deleted volume %s", volumeID))
	return nil
}

// AttachLoopDevice attaches a raw file to a loop device and returns the device path
func (m *Manager) AttachLoopDevice(filePath string) (string, error) {
	m.log.Info(fmt.Sprintf("[RawFile] Attaching loop device for %s", filePath))

	// Check if already attached
	existingDevice, err := m.FindLoopDevice(filePath)
	if err == nil && existingDevice != "" {
		m.log.Info(fmt.Sprintf("[RawFile] File %s already attached to %s", filePath, existingDevice))
		return existingDevice, nil
	}

	// Use losetup to attach the file to a loop device
	cmd := exec.Command("losetup", "--find", "--show", filePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup failed: %s: %w", string(output), err)
	}

	devicePath := strings.TrimSpace(string(output))
	m.log.Info(fmt.Sprintf("[RawFile] Attached %s to %s", filePath, devicePath))

	return devicePath, nil
}

// DetachLoopDevice detaches a loop device associated with a file
func (m *Manager) DetachLoopDevice(filePath string) error {
	m.log.Info(fmt.Sprintf("[RawFile] Detaching loop device for %s", filePath))

	devicePath, err := m.FindLoopDevice(filePath)
	if err != nil {
		if errors.Is(err, ErrLoopDeviceNotFound) {
			m.log.Info(fmt.Sprintf("[RawFile] No loop device found for %s", filePath))
			return nil
		}
		return err
	}

	return m.DetachLoopDeviceByPath(devicePath)
}

// DetachLoopDeviceByPath detaches a specific loop device
func (m *Manager) DetachLoopDeviceByPath(devicePath string) error {
	m.log.Info(fmt.Sprintf("[RawFile] Detaching loop device %s", devicePath))

	cmd := exec.Command("losetup", "-d", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup -d failed: %s: %w", string(output), err)
	}

	m.log.Info(fmt.Sprintf("[RawFile] Successfully detached %s", devicePath))
	return nil
}

// FindLoopDevice finds the loop device associated with a backing file
func (m *Manager) FindLoopDevice(filePath string) (string, error) {
	// Get absolute path for comparison
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Read /proc/mounts or use losetup -j
	cmd := exec.Command("losetup", "-j", absPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// losetup returns error if no device found
		return "", ErrLoopDeviceNotFound
	}

	// Parse output: /dev/loop0: [64768]:12345678 (/path/to/file)
	outputStr := strings.TrimSpace(string(output))
	if outputStr == "" {
		return "", ErrLoopDeviceNotFound
	}

	parts := strings.Split(outputStr, ":")
	if len(parts) < 1 {
		return "", ErrLoopDeviceNotFound
	}

	return strings.TrimSpace(parts[0]), nil
}

// GetVolumeInfo retrieves information about an existing volume
func (m *Manager) GetVolumeInfo(volumeID string) (*VolumeInfo, error) {
	volumePath := m.GetVolumePath(volumeID)

	info, err := os.Stat(volumePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrVolumeNotFound
		}
		return nil, fmt.Errorf("failed to stat volume file: %w", err)
	}

	volumeInfo := &VolumeInfo{
		VolumeID: volumeID,
		Path:     volumePath,
		Size:     info.Size(),
	}

	// Try to find associated loop device
	devicePath, err := m.FindLoopDevice(volumePath)
	if err == nil {
		volumeInfo.DevicePath = devicePath
	}

	return volumeInfo, nil
}

// ExpandVolume expands an existing raw file volume to a new size
func (m *Manager) ExpandVolume(volumeID string, newSizeBytes int64, sparse bool) error {
	m.log.Info(fmt.Sprintf("[RawFile] Expanding volume %s to %d bytes", volumeID, newSizeBytes))

	volumePath := m.GetVolumePath(volumeID)

	// Get current size
	info, err := os.Stat(volumePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrVolumeNotFound
		}
		return fmt.Errorf("failed to stat volume: %w", err)
	}

	currentSize := info.Size()
	if newSizeBytes <= currentSize {
		m.log.Info(fmt.Sprintf("[RawFile] Volume %s is already %d bytes, requested %d bytes", volumeID, currentSize, newSizeBytes))
		return nil
	}

	// Expand the file
	if sparse {
		if err := os.Truncate(volumePath, newSizeBytes); err != nil {
			return fmt.Errorf("failed to truncate file: %w", err)
		}
	} else {
		// Use fallocate to extend the file
		cmd := exec.Command("fallocate", "-l", strconv.FormatInt(newSizeBytes, 10), volumePath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("fallocate failed: %s: %w", string(output), err)
		}
	}

	// If there's an attached loop device, refresh its size
	devicePath, err := m.FindLoopDevice(volumePath)
	if err == nil && devicePath != "" {
		if err := m.RefreshLoopDevice(devicePath); err != nil {
			m.log.Warning(fmt.Sprintf("[RawFile] Failed to refresh loop device %s: %v", devicePath, err))
		}
	}

	m.log.Info(fmt.Sprintf("[RawFile] Successfully expanded volume %s from %d to %d bytes", volumeID, currentSize, newSizeBytes))
	return nil
}

// RefreshLoopDevice refreshes a loop device to recognize size changes
func (m *Manager) RefreshLoopDevice(devicePath string) error {
	m.log.Info(fmt.Sprintf("[RawFile] Refreshing loop device %s", devicePath))

	cmd := exec.Command("losetup", "-c", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup -c failed: %s: %w", string(output), err)
	}

	return nil
}

// VolumeExists checks if a volume with the given ID exists
func (m *Manager) VolumeExists(volumeID string) bool {
	volumePath := m.GetVolumePath(volumeID)
	_, err := os.Stat(volumePath)
	return err == nil
}

// GetDeviceSize returns the size of a block device in bytes
func (m *Manager) GetDeviceSize(devicePath string) (int64, error) {
	file, err := os.Open(devicePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open device: %w", err)
	}
	defer file.Close()

	var size int64
	// BLKGETSIZE64 ioctl
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), 0x80081272, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, fmt.Errorf("ioctl BLKGETSIZE64 failed: %v", errno)
	}

	return size, nil
}

// EnsureDataDir ensures the data directory exists with proper permissions
func (m *Manager) EnsureDataDir() error {
	if err := os.MkdirAll(m.dataDir, DefaultDirMode); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", m.dataDir, err)
	}
	return nil
}

// ListVolumes returns a list of all volume IDs in the data directory
func (m *Manager) ListVolumes() ([]string, error) {
	entries, err := os.ReadDir(m.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read data directory: %w", err)
	}

	var volumes []string
	for _, entry := range entries {
		if entry.IsDir() {
			volumePath := m.GetVolumePath(entry.Name())
			if _, err := os.Stat(volumePath); err == nil {
				volumes = append(volumes, entry.Name())
			}
		}
	}

	return volumes, nil
}

// GetLoopDevices returns a map of backing files to their loop devices
func (m *Manager) GetLoopDevices() (map[string]string, error) {
	devices := make(map[string]string)

	cmd := exec.Command("losetup", "-l", "-n", "-O", "NAME,BACK-FILE")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// No loop devices is not an error
		return devices, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 2 {
			devicePath := fields[0]
			backingFile := fields[1]
			devices[backingFile] = devicePath
		}
	}

	return devices, nil
}

