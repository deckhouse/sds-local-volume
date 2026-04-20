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

package rawfile

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestValidateVolumeID(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		assert.NoError(t, ValidateVolumeID("pvc-123"))
		assert.NoError(t, ValidateVolumeID("test-volume-123"))
	})
	t.Run("empty", func(t *testing.T) {
		assert.ErrorIs(t, ValidateVolumeID(""), ErrInvalidVolumeID)
	})
	t.Run("path separator", func(t *testing.T) {
		assert.ErrorIs(t, ValidateVolumeID("vol/name"), ErrInvalidVolumeID)
		assert.ErrorIs(t, ValidateVolumeID("/absolute"), ErrInvalidVolumeID)
		assert.ErrorIs(t, ValidateVolumeID(`vol\name`), ErrInvalidVolumeID)
	})
	t.Run("path traversal", func(t *testing.T) {
		assert.ErrorIs(t, ValidateVolumeID(".."), ErrInvalidVolumeID)
		assert.ErrorIs(t, ValidateVolumeID("vol/../etc"), ErrInvalidVolumeID)
	})
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
	initialSize := int64(1024 * 1024)      // 1 MB
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

func TestManager_ReclaimPolicyMarker(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	const volumeID = "pvc-reclaim"

	t.Run("missing marker returns empty string", func(t *testing.T) {
		got, err := m.GetReclaimPolicy(volumeID)
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("set then get persists value", func(t *testing.T) {
		require.NoError(t, m.SetReclaimPolicy(volumeID, "Delete"))
		got, err := m.GetReclaimPolicy(volumeID)
		require.NoError(t, err)
		assert.Equal(t, "Delete", got)

		// File must live in the volume directory.
		_, statErr := os.Stat(filepath.Join(m.GetVolumeDir(volumeID), reclaimMarkerFile))
		require.NoError(t, statErr)
	})

	t.Run("overwrite atomically", func(t *testing.T) {
		require.NoError(t, m.SetReclaimPolicy(volumeID, "Retain"))
		got, err := m.GetReclaimPolicy(volumeID)
		require.NoError(t, err)
		assert.Equal(t, "Retain", got)
	})

	t.Run("empty value rejected", func(t *testing.T) {
		err := m.SetReclaimPolicy(volumeID, "")
		require.Error(t, err)
	})

	t.Run("invalid volumeID rejected", func(t *testing.T) {
		err := m.SetReclaimPolicy("", "Delete")
		require.ErrorIs(t, err, ErrInvalidVolumeID)
		_, err = m.GetReclaimPolicy("../etc")
		require.ErrorIs(t, err, ErrInvalidVolumeID)
	})

	t.Run("DeleteVolume removes marker", func(t *testing.T) {
		_, err := m.CreateVolume(volumeID, 1024, true)
		require.NoError(t, err)
		require.NoError(t, m.SetReclaimPolicy(volumeID, "Delete"))

		require.NoError(t, m.DeleteVolume(volumeID))

		got, err := m.GetReclaimPolicy(volumeID)
		require.NoError(t, err)
		assert.Equal(t, "", got, "marker must be gone after DeleteVolume")
	})
}

func TestManager_SparseMarker(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	const volumeID = "pvc-sparse"

	t.Run("missing marker returns ok=false", func(t *testing.T) {
		_, ok, err := m.GetSparse(volumeID)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("true round-trips", func(t *testing.T) {
		require.NoError(t, m.SetSparse(volumeID, true))
		val, ok, err := m.GetSparse(volumeID)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.True(t, val)
	})

	t.Run("false round-trips", func(t *testing.T) {
		require.NoError(t, m.SetSparse(volumeID, false))
		val, ok, err := m.GetSparse(volumeID)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.False(t, val)
	})

	t.Run("invalid disk content surfaces as error", func(t *testing.T) {
		dir := m.GetVolumeDir(volumeID)
		require.NoError(t, os.MkdirAll(dir, DefaultDirMode))
		require.NoError(t, os.WriteFile(filepath.Join(dir, sparseMarkerFile), []byte("not-a-bool"), 0o600))

		_, _, err := m.GetSparse(volumeID)
		require.Error(t, err)
	})

	t.Run("invalid volumeID rejected", func(t *testing.T) {
		err := m.SetSparse("", true)
		require.ErrorIs(t, err, ErrInvalidVolumeID)
	})
}

func TestParseLosetupOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \n\t\n", nil},
		{
			name: "single device",
			in:   "/dev/loop0: [64768]:12345678 (/var/lib/sds-local-volume/rawfile/pvc-1/disk.img)",
			want: []string{"/dev/loop0"},
		},
		{
			name: "multiple devices preserve order",
			in: `/dev/loop3: [64768]:12345678 (/path/disk.img)
/dev/loop1: [64768]:12345678 (/path/disk.img)
/dev/loop7: [64768]:12345678 (/path/disk.img)`,
			want: []string{"/dev/loop3", "/dev/loop1", "/dev/loop7"},
		},
		{
			name: "trailing newline tolerated",
			in:   "/dev/loop0: [64768]:1 (/x)\n",
			want: []string{"/dev/loop0"},
		},
		{
			name: "CRLF tolerated",
			in:   "/dev/loop0: [x]:1 (/x)\r\n/dev/loop2: [x]:2 (/x)\r\n",
			want: []string{"/dev/loop0", "/dev/loop2"},
		},
		{
			name: "blank lines skipped",
			in:   "\n/dev/loop0: [x]:1 (/x)\n\n/dev/loop1: [x]:2 (/x)\n\n",
			want: []string{"/dev/loop0", "/dev/loop1"},
		},
		{
			name: "missing colon skipped",
			in:   "garbage line without colon\n/dev/loop0: [x]:1 (/x)",
			want: []string{"/dev/loop0"},
		},
		{
			name: "leading whitespace before colon trimmed",
			in:   "  /dev/loop0  : [x]:1 (/x)",
			want: []string{"/dev/loop0"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseLosetupOutput(c.in)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestVolumeLock_MutualExclusion(t *testing.T) {
	vl := newVolumeLock()

	const volumeID = "pvc-lock"
	var counter int32
	var maxObserved int32

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			vl.Lock(volumeID)
			defer vl.Unlock(volumeID)

			cur := atomic.AddInt32(&counter, 1)
			for {
				prev := atomic.LoadInt32(&maxObserved)
				if cur <= prev || atomic.CompareAndSwapInt32(&maxObserved, prev, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&counter, -1)
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&maxObserved), "more than one goroutine held the lock at the same time")
}

func TestVolumeLock_DistinctVolumesNotBlocked(t *testing.T) {
	vl := newVolumeLock()

	vl.Lock("vol-a")
	defer vl.Unlock("vol-a")

	done := make(chan struct{})
	go func() {
		vl.Lock("vol-b")
		vl.Unlock("vol-b")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Lock on a different volumeID was unexpectedly blocked")
	}
}

func TestVolumeLock_ReleasesEntries(t *testing.T) {
	vl := newVolumeLock()

	for i := 0; i < 100; i++ {
		vl.Lock("pvc-temp")
		vl.Unlock("pvc-temp")
	}

	vl.mu.Lock()
	mapLen := len(vl.locks)
	vl.mu.Unlock()
	assert.Equal(t, 0, mapLen, "lock map must drop entries with zero waiters")
}

func TestManager_DeleteVolume_RemovesMarkers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rawfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log, _ := logger.NewLogger(logger.DebugLevel)
	m := NewManager(log, tmpDir)

	const volumeID = "pvc-cleanup"
	_, err = m.CreateVolume(volumeID, 1024, true)
	require.NoError(t, err)
	require.NoError(t, m.SetReclaimPolicy(volumeID, "Delete"))
	require.NoError(t, m.SetSparse(volumeID, true))

	dir := m.GetVolumeDir(volumeID)
	for _, name := range []string{"disk.img", reclaimMarkerFile, sparseMarkerFile} {
		_, statErr := os.Stat(filepath.Join(dir, name))
		require.NoErrorf(t, statErr, "expected %s to exist before deletion", name)
	}

	require.NoError(t, m.DeleteVolume(volumeID))

	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr), "volume directory must be gone after DeleteVolume")
}
