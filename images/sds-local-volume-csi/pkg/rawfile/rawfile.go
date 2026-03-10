/*
Copyright 2026 Flant JSC

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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
)

const (
	// DefaultDirMode is the default permission mode for directories
	DefaultDirMode = 0750
	// DefaultFileMode is the default permission mode for raw files
	DefaultFileMode = 0600
)

var (
	ErrVolumeNotFound     = errors.New("volume not found")
	ErrLoopDeviceNotFound = errors.New("loop device not found")
	ErrInvalidVolumeID    = errors.New("volume ID must not be empty and must not contain path separators, backslashes, or '..'")
)

// ValidateVolumeID checks that volumeID is safe to use in paths (no path traversal).
func ValidateVolumeID(volumeID string) error {
	if volumeID == "" ||
		volumeID == "." ||
		strings.Contains(volumeID, "/") ||
		strings.Contains(volumeID, `\`) ||
		strings.Contains(volumeID, "..") ||
		strings.ContainsRune(volumeID, 0) {
		return ErrInvalidVolumeID
	}
	return nil
}

// volumeLock provides per-volume locking to avoid blocking unrelated volumes
// during long-running operations like file allocation.
type volumeLock struct {
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
}

func newVolumeLock() *volumeLock {
	return &volumeLock{locks: make(map[string]*sync.Mutex)}
}

func (vl *volumeLock) Lock(volumeID string) {
	vl.mu.Lock()
	l, ok := vl.locks[volumeID]
	if !ok {
		l = &sync.Mutex{}
		vl.locks[volumeID] = l
	}
	vl.mu.Unlock()
	l.Lock()
}

func (vl *volumeLock) Unlock(volumeID string) {
	vl.mu.Lock()
	l, ok := vl.locks[volumeID]
	vl.mu.Unlock()
	if ok {
		l.Unlock()
	}
}

// Manager handles raw file operations for loop device volumes
type Manager struct {
	log     *logger.Logger
	dataDir string
	vl      *volumeLock
}

// NewManager creates a new rawfile Manager
func NewManager(log *logger.Logger, dataDir string) *Manager {
	return &Manager{
		log:     log,
		dataDir: dataDir,
		vl:      newVolumeLock(),
	}
}

// VolumeInfo contains information about a rawfile volume
type VolumeInfo struct {
	VolumeID   string
	Path       string
	Size       int64
	ModTime    time.Time
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
	if err := ValidateVolumeID(volumeID); err != nil {
		return nil, err
	}

	m.vl.Lock(volumeID)
	defer m.vl.Unlock(volumeID)

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

	if err := m.createRawFile(volumePath, sizeBytes, sparse); err != nil {
		if cleanupErr := os.RemoveAll(volumeDir); cleanupErr != nil {
			m.log.Warning(fmt.Sprintf("[RawFile] Failed to clean up volume directory %s after creation failure: %v", volumeDir, cleanupErr))
		}
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

// allocateFile pre-allocates a file to the given size using the fastest
// available method: fallocate(2) syscall -> fallocate binary -> goAllocate.
// When truncate is true, existing data is discarded (for new files).
// When truncate is false, existing data is preserved (for expansion).
func (m *Manager) allocateFile(path string, sizeBytes int64, truncate bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if truncate {
		flags |= os.O_TRUNC
	}

	file, err := os.OpenFile(path, flags, DefaultFileMode)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}

	// Try the fallocate(2) syscall directly — no fork+exec overhead.
	err = unix.Fallocate(int(file.Fd()), 0, 0, sizeBytes)
	file.Close()
	if err == nil {
		m.log.Info(fmt.Sprintf("[RawFile] fallocate syscall succeeded for %s (%d bytes)", path, sizeBytes))
		return nil
	}
	m.log.Info(fmt.Sprintf("[RawFile] fallocate syscall returned %v, trying fallocate binary", err))

	// Fallback: fallocate binary
	cmd := exec.Command("fallocate", "-l", strconv.FormatInt(sizeBytes, 10), path)
	output, cmdErr := cmd.CombinedOutput()
	if cmdErr == nil {
		m.log.Info(fmt.Sprintf("[RawFile] fallocate binary succeeded for %s", path))
		return nil
	}
	m.log.Warning(fmt.Sprintf("[RawFile] fallocate binary failed (%s), falling back to goAllocate", strings.TrimSpace(string(output))))

	return m.goAllocate(path, sizeBytes, truncate)
}

// fallocate is a convenience wrapper for new file creation (truncates).
func (m *Manager) fallocate(path string, sizeBytes int64) error {
	return m.allocateFile(path, sizeBytes, true)
}

// goAllocate creates or extends a pre-allocated file by writing zeros.
// Used as a last resort when fallocate is not supported (e.g., NFS).
// Uses 32MB chunks and fadvise hints to maximize sequential I/O throughput.
func (m *Manager) goAllocate(path string, sizeBytes int64, truncate bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if truncate {
		flags |= os.O_TRUNC
	}

	file, err := os.OpenFile(path, flags, DefaultFileMode)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	_ = unix.Fadvise(int(file.Fd()), 0, sizeBytes, unix.FADV_SEQUENTIAL)

	// When not truncating (expand), seek to current end to append
	var startOffset int64
	if !truncate {
		startOffset, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			return fmt.Errorf("failed to seek to end: %w", err)
		}
		if startOffset >= sizeBytes {
			return nil
		}
	}

	const chunkSize = 32 * 1024 * 1024 // 32MB — 8x fewer write syscalls than 4MB
	const logEveryBytes int64 = 512 * 1024 * 1024
	zeroChunk := make([]byte, chunkSize)

	remaining := sizeBytes - startOffset
	var written int64
	var lastLogAt int64
	for remaining > 0 {
		writeSize := int64(chunkSize)
		if remaining < writeSize {
			writeSize = remaining
		}

		n, err := file.Write(zeroChunk[:writeSize])
		if err != nil {
			return fmt.Errorf("failed to write zeros at offset %d: %w", startOffset+written, err)
		}
		remaining -= int64(n)
		written += int64(n)

		if written-lastLogAt >= logEveryBytes {
			m.log.Info(fmt.Sprintf("[RawFile] goAllocate progress: %d / %d bytes (%.1f%%)",
				startOffset+written, sizeBytes, float64(startOffset+written)/float64(sizeBytes)*100))
			lastLogAt = written
		}
	}

	_ = unix.Fadvise(int(file.Fd()), 0, sizeBytes, unix.FADV_DONTNEED)

	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}

	m.log.Info(fmt.Sprintf("[RawFile] goAllocate completed: %d bytes written to %s", written, path))
	return nil
}

// DeleteVolume removes a raw file volume and detaches any loop device
func (m *Manager) DeleteVolume(volumeID string) error {
	if err := ValidateVolumeID(volumeID); err != nil {
		return err
	}

	m.vl.Lock(volumeID)
	defer m.vl.Unlock(volumeID)

	m.log.Info(fmt.Sprintf("[RawFile] Deleting volume %s", volumeID))

	volumePath := m.GetVolumePath(volumeID)
	volumeDir := m.GetVolumeDir(volumeID)

	if _, err := os.Stat(volumePath); err != nil {
		if os.IsNotExist(err) {
			m.log.Warning(fmt.Sprintf("[RawFile] Volume %s does not exist, nothing to delete", volumeID))
			return nil
		}
		return fmt.Errorf("failed to stat volume %s: %w", volumeID, err)
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
	m.log.Debug(fmt.Sprintf("[RawFile] Attaching loop device for %s", filePath))

	// Check if already attached
	existingDevice, err := m.FindLoopDevice(filePath)
	if err == nil && existingDevice != "" {
		m.log.Debug(fmt.Sprintf("[RawFile] File %s already attached to %s", filePath, existingDevice))
		return existingDevice, nil
	}

	// Use losetup to attach the file to a loop device
	cmd := exec.Command("losetup", "--find", "--show", filePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup failed: %s: %w", string(output), err)
	}

	devicePath := strings.TrimSpace(string(output))
	m.log.Debug(fmt.Sprintf("[RawFile] Attached %s to %s", filePath, devicePath))

	return devicePath, nil
}

// DetachLoopDevice detaches a loop device associated with a file
func (m *Manager) DetachLoopDevice(filePath string) error {
	m.log.Debug(fmt.Sprintf("[RawFile] Detaching loop device for %s", filePath))

	devicePath, err := m.FindLoopDevice(filePath)
	if err != nil {
		if errors.Is(err, ErrLoopDeviceNotFound) {
			m.log.Debug(fmt.Sprintf("[RawFile] No loop device found for %s", filePath))
			return nil
		}
		return err
	}

	return m.DetachLoopDeviceByPath(devicePath)
}

// DetachLoopDeviceByPath detaches a specific loop device
func (m *Manager) DetachLoopDeviceByPath(devicePath string) error {
	m.log.Debug(fmt.Sprintf("[RawFile] Detaching loop device %s", devicePath))

	cmd := exec.Command("losetup", "-d", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup -d failed: %s: %w", string(output), err)
	}

	m.log.Debug(fmt.Sprintf("[RawFile] Successfully detached %s", devicePath))
	return nil
}

// FindLoopDevice finds the loop device associated with a backing file.
// If multiple loop devices are attached, the first one is returned and a warning is logged.
func (m *Manager) FindLoopDevice(filePath string) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	cmd := exec.Command("losetup", "-j", absPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		if outputStr == "" {
			return "", ErrLoopDeviceNotFound
		}
		return "", fmt.Errorf("losetup -j failed: %s: %w", outputStr, err)
	}

	// Parse output lines: /dev/loop0: [64768]:12345678 (/path/to/file)
	outputStr := strings.TrimSpace(string(output))
	if outputStr == "" {
		return "", ErrLoopDeviceNotFound
	}

	lines := strings.Split(outputStr, "\n")
	if len(lines) > 1 {
		m.log.Warning(fmt.Sprintf("[RawFile] Multiple loop devices found for %s (%d devices), using first one", filePath, len(lines)))
	}

	parts := strings.SplitN(lines[0], ":", 2)
	if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
		return "", ErrLoopDeviceNotFound
	}

	return strings.TrimSpace(parts[0]), nil
}

// GetVolumeInfo retrieves information about an existing volume
func (m *Manager) GetVolumeInfo(volumeID string) (*VolumeInfo, error) {
	if err := ValidateVolumeID(volumeID); err != nil {
		return nil, err
	}
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
		ModTime:  info.ModTime(),
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
	if err := ValidateVolumeID(volumeID); err != nil {
		return err
	}

	m.vl.Lock(volumeID)
	defer m.vl.Unlock(volumeID)

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

	if sparse {
		if err := os.Truncate(volumePath, newSizeBytes); err != nil {
			return fmt.Errorf("failed to truncate file: %w", err)
		}
	} else {
		// truncate=false to preserve existing data
		if err := m.allocateFile(volumePath, newSizeBytes, false); err != nil {
			return fmt.Errorf("expand failed: %w", err)
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
	m.log.Debug(fmt.Sprintf("[RawFile] Refreshing loop device %s", devicePath))

	cmd := exec.Command("losetup", "-c", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup -c failed: %s: %w", string(output), err)
	}

	return nil
}

// RescanLoopDevice finds and refreshes the loop device associated with a backing file
func (m *Manager) RescanLoopDevice(filePath string) error {
	m.log.Debug(fmt.Sprintf("[RawFile] Rescanning loop device for %s", filePath))

	devicePath, err := m.FindLoopDevice(filePath)
	if err != nil {
		if errors.Is(err, ErrLoopDeviceNotFound) {
			m.log.Debug(fmt.Sprintf("[RawFile] No loop device found for %s, nothing to rescan", filePath))
			return nil
		}
		return fmt.Errorf("failed to find loop device: %w", err)
	}

	return m.RefreshLoopDevice(devicePath)
}

// VolumeExists checks if a volume with the given ID exists
func (m *Manager) VolumeExists(volumeID string) bool {
	if ValidateVolumeID(volumeID) != nil {
		return false
	}
	volumePath := m.GetVolumePath(volumeID)
	_, err := os.Stat(volumePath)
	return err == nil
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
		if entry.IsDir() && ValidateVolumeID(entry.Name()) == nil {
			volumePath := m.GetVolumePath(entry.Name())
			if _, err := os.Stat(volumePath); err == nil {
				volumes = append(volumes, entry.Name())
			}
		}
	}

	return volumes, nil
}

