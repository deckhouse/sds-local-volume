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

package rawfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
)

func TestManager_GetVolumePath(t *testing.T) {
	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, "/var/lib/rawfile")

	path := m.GetVolumePath("test-volume-123")
	assert.Equal(t, "/var/lib/rawfile/test-volume-123/disk.img", path)
}

func TestManager_GetVolumeDir(t *testing.T) {
	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, "/var/lib/rawfile")

	dir := m.GetVolumeDir("test-volume-123")
	assert.Equal(t, "/var/lib/rawfile/test-volume-123", dir)
}

func TestManager_CreateAndDeleteVolume_Sparse(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	volumeID := "test-sparse-volume"
	sizeBytes := int64(1024 * 1024) // 1 MB

	// Create sparse volume
	info, err := m.CreateVolume(volumeID, sizeBytes, true)
	require.NoError(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, volumeID, info.VolumeID)
	assert.Equal(t, sizeBytes, info.Size)

	// Verify file exists
	volumePath := m.GetVolumePath(volumeID)
	assert.Equal(t, volumePath, info.Path)

	stat, err := os.Stat(volumePath)
	require.NoError(t, err)
	assert.Equal(t, sizeBytes, stat.Size())

	// Verify VolumeExists returns true
	assert.True(t, m.VolumeExists(volumeID))

	// Delete volume
	err = m.DeleteVolume(volumeID)
	require.NoError(t, err)

	// Verify file no longer exists
	_, err = os.Stat(volumePath)
	assert.True(t, os.IsNotExist(err))

	// Verify VolumeExists returns false
	assert.False(t, m.VolumeExists(volumeID))
}

func TestManager_CreateVolume_AlreadyExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	volumeID := "test-exists-volume"
	sizeBytes := int64(1024 * 1024)

	// Create volume first time
	info1, err := m.CreateVolume(volumeID, sizeBytes, true)
	require.NoError(t, err)
	assert.NotNil(t, info1)

	// Create volume second time - should return existing
	info2, err := m.CreateVolume(volumeID, sizeBytes, true)
	require.NoError(t, err)
	assert.NotNil(t, info2)
	assert.Equal(t, info1.Path, info2.Path)
}

func TestManager_DeleteVolume_NotExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	// Deleting non-existent volume should not error
	err = m.DeleteVolume("non-existent-volume")
	require.NoError(t, err)
}

func TestManager_GetVolumeInfo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	volumeID := "test-info-volume"
	sizeBytes := int64(1024 * 1024)

	// Create volume
	_, err = m.CreateVolume(volumeID, sizeBytes, true)
	require.NoError(t, err)

	// Get volume info
	info, err := m.GetVolumeInfo(volumeID)
	require.NoError(t, err)
	assert.Equal(t, volumeID, info.VolumeID)
	assert.Equal(t, sizeBytes, info.Size)
}

func TestManager_GetVolumeInfo_NotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	// Get info for non-existent volume
	_, err = m.GetVolumeInfo("non-existent")
	assert.ErrorIs(t, err, ErrVolumeNotFound)
}

func TestManager_ExpandVolume(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	volumeID := "test-expand-volume"
	initialSize := int64(1024 * 1024)  // 1 MB
	expandedSize := int64(2 * 1024 * 1024) // 2 MB

	// Create volume
	_, err = m.CreateVolume(volumeID, initialSize, true)
	require.NoError(t, err)

	// Expand volume
	err = m.ExpandVolume(volumeID, expandedSize, true)
	require.NoError(t, err)

	// Verify new size
	info, err := m.GetVolumeInfo(volumeID)
	require.NoError(t, err)
	assert.Equal(t, expandedSize, info.Size)
}

func TestManager_ExpandVolume_SmallerSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	volumeID := "test-expand-smaller-volume"
	initialSize := int64(2 * 1024 * 1024)
	smallerSize := int64(1024 * 1024)

	// Create volume
	_, err = m.CreateVolume(volumeID, initialSize, true)
	require.NoError(t, err)

	// Try to shrink - should be a no-op
	err = m.ExpandVolume(volumeID, smallerSize, true)
	require.NoError(t, err)

	// Verify size unchanged
	info, err := m.GetVolumeInfo(volumeID)
	require.NoError(t, err)
	assert.Equal(t, initialSize, info.Size)
}

func TestManager_ListVolumes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	// Initially empty
	volumes, err := m.ListVolumes()
	require.NoError(t, err)
	assert.Empty(t, volumes)

	// Create volumes
	_, err = m.CreateVolume("vol1", 1024*1024, true)
	require.NoError(t, err)
	_, err = m.CreateVolume("vol2", 1024*1024, true)
	require.NoError(t, err)

	// List volumes
	volumes, err = m.ListVolumes()
	require.NoError(t, err)
	assert.Len(t, volumes, 2)
	assert.Contains(t, volumes, "vol1")
	assert.Contains(t, volumes, "vol2")
}

func TestManager_EnsureDataDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dataDir := filepath.Join(tmpDir, "subdir", "rawfile")

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, dataDir)

	// Ensure data dir creates nested directories
	err = m.EnsureDataDir()
	require.NoError(t, err)

	stat, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.True(t, stat.IsDir())
}

