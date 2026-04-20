/*
Copyright 2026 Flant JSC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package driver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/internal"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/sds-local-volume-csi/pkg/rawfile"
)

func newTestDriver(t *testing.T, dataDir string, cl client.Client) *Driver {
	t.Helper()
	log, err := logger.NewLogger(logger.DebugLevel)
	require.NoError(t, err)
	return &Driver{
		log:            log,
		cl:             cl,
		rawfileManager: rawfile.NewManager(log, dataDir),
		inFlight:       internal.NewInFlight(),
		hostID:         "node-test",
	}
}

func newTestClient(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// rawFilePV builds a minimal RawFile-style PV.
func rawFilePV(name string, withFinalizer bool) *corev1.PersistentVolume {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       DefaultDriverName,
					VolumeHandle: name,
					VolumeAttributes: map[string]string{
						internal.TypeKey: internal.RawFile,
					},
				},
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		},
	}
	if withFinalizer {
		pv.Finalizers = []string{internal.RawFilePVFinalizer}
	}
	return pv
}

func mustCreate(t *testing.T, m *rawfile.Manager, volumeID string) {
	t.Helper()
	_, err := m.CreateVolume(volumeID, 1024, true)
	require.NoError(t, err)
}

func TestListRawFilePVs_FiltersByDriverAndType(t *testing.T) {
	other := rawFilePV("other-driver", false)
	other.Spec.CSI.Driver = "some.other.csi"

	wrongType := rawFilePV("wrong-type", false)
	wrongType.Spec.CSI.VolumeAttributes[internal.TypeKey] = internal.Lvm

	good := rawFilePV("pvc-good", false)

	cl := newTestClient(other, wrongType, good)
	d := newTestDriver(t, t.TempDir(), cl)

	idx, err := d.listRawFilePVs(context.Background())
	require.NoError(t, err)
	assert.Len(t, idx, 1)
	assert.Contains(t, idx, "pvc-good")
}

func TestCleanupOrphanedVolume_RetainKeepsFile(t *testing.T) {
	dataDir := t.TempDir()
	d := newTestDriver(t, dataDir, newTestClient())

	const volumeID = "pvc-orphan-retain"
	mustCreate(t, d.rawfileManager, volumeID)
	require.NoError(t, d.rawfileManager.SetReclaimPolicy(volumeID, string(corev1.PersistentVolumeReclaimRetain)))

	d.cleanupOrphanedVolume(volumeID)

	_, err := os.Stat(d.rawfileManager.GetVolumePath(volumeID))
	assert.NoError(t, err, "Retain orphan must NOT be deleted")
}

func TestCleanupOrphanedVolume_DeleteRemovesFile(t *testing.T) {
	dataDir := t.TempDir()
	d := newTestDriver(t, dataDir, newTestClient())

	const volumeID = "pvc-orphan-delete"
	mustCreate(t, d.rawfileManager, volumeID)
	require.NoError(t, d.rawfileManager.SetReclaimPolicy(volumeID, string(corev1.PersistentVolumeReclaimDelete)))

	d.cleanupOrphanedVolume(volumeID)

	_, err := os.Stat(d.rawfileManager.GetVolumePath(volumeID))
	assert.True(t, os.IsNotExist(err), "Delete orphan must be removed")
}

func TestCleanupOrphanedVolume_MissingMarkerIsConservative(t *testing.T) {
	dataDir := t.TempDir()
	d := newTestDriver(t, dataDir, newTestClient())

	const volumeID = "pvc-orphan-no-marker"
	mustCreate(t, d.rawfileManager, volumeID)
	// no SetReclaimPolicy call — simulate older driver that didn't persist.

	d.cleanupOrphanedVolume(volumeID)

	_, err := os.Stat(d.rawfileManager.GetVolumePath(volumeID))
	assert.NoError(t, err, "Orphan with missing reclaim marker must NOT be removed (conservative default)")
}

func TestProcessRawFilePVs_GracePeriodSkipsFreshFiles(t *testing.T) {
	dataDir := t.TempDir()
	d := newTestDriver(t, dataDir, newTestClient())

	const volumeID = "pvc-fresh-orphan"
	mustCreate(t, d.rawfileManager, volumeID)
	require.NoError(t, d.rawfileManager.SetReclaimPolicy(volumeID, string(corev1.PersistentVolumeReclaimDelete)))

	// Volume is brand-new (mtime = now), so even with Delete policy
	// processRawFilePVs MUST NOT delete it on this cycle.
	d.processRawFilePVs(context.Background())

	_, err := os.Stat(d.rawfileManager.GetVolumePath(volumeID))
	assert.NoError(t, err, "Fresh orphan must be preserved by grace period")
}

func TestProcessRawFilePVs_DeletesAgedDeleteOrphan(t *testing.T) {
	dataDir := t.TempDir()
	d := newTestDriver(t, dataDir, newTestClient())

	const volumeID = "pvc-aged-orphan"
	mustCreate(t, d.rawfileManager, volumeID)
	require.NoError(t, d.rawfileManager.SetReclaimPolicy(volumeID, string(corev1.PersistentVolumeReclaimDelete)))

	// Backdate disk.img so it falls outside the grace period.
	old := time.Now().Add(-2 * orphanGracePeriod)
	require.NoError(t, os.Chtimes(d.rawfileManager.GetVolumePath(volumeID), old, old))

	d.processRawFilePVs(context.Background())

	_, err := os.Stat(d.rawfileManager.GetVolumePath(volumeID))
	assert.True(t, os.IsNotExist(err), "Aged Delete orphan must be cleaned up")
}

func TestProcessRawFilePVs_KeepsAgedRetainOrphan(t *testing.T) {
	dataDir := t.TempDir()
	d := newTestDriver(t, dataDir, newTestClient())

	const volumeID = "pvc-aged-retain"
	mustCreate(t, d.rawfileManager, volumeID)
	require.NoError(t, d.rawfileManager.SetReclaimPolicy(volumeID, string(corev1.PersistentVolumeReclaimRetain)))

	old := time.Now().Add(-2 * orphanGracePeriod)
	require.NoError(t, os.Chtimes(d.rawfileManager.GetVolumePath(volumeID), old, old))

	d.processRawFilePVs(context.Background())

	_, err := os.Stat(d.rawfileManager.GetVolumePath(volumeID))
	assert.NoError(t, err, "Retain orphan MUST survive even after grace period")
}

func TestProcessRawFilePVs_LiveRetainPVIsLeftAlone(t *testing.T) {
	dataDir := t.TempDir()

	const volumeID = "pvc-live-retain"
	pv := rawFilePV(volumeID, false)
	pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain

	d := newTestDriver(t, dataDir, newTestClient(pv))

	mustCreate(t, d.rawfileManager, volumeID)
	require.NoError(t, d.rawfileManager.SetReclaimPolicy(volumeID, string(corev1.PersistentVolumeReclaimRetain)))

	d.processRawFilePVs(context.Background())

	_, err := os.Stat(d.rawfileManager.GetVolumePath(volumeID))
	assert.NoError(t, err, "Live PV without DeletionTimestamp must not trigger deletion")
}

func TestHasFinalizer(t *testing.T) {
	d := newTestDriver(t, t.TempDir(), newTestClient())

	assert.False(t, d.hasFinalizer(rawFilePV("x", false)))
	assert.True(t, d.hasFinalizer(rawFilePV("x", true)))
}

func TestIsRetryableAPIError(t *testing.T) {
	assert.False(t, isRetryableAPIError(nil))
}
