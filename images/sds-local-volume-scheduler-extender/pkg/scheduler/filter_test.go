package scheduler

import (
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"testing"
)

func TestFilter(t *testing.T) {
	log := logger.Logger{}
	t.Run("filterNotManagedPVC", func(t *testing.T) {
		sc1 := "sc1"
		sc2 := "sc2"
		sc3 := "sc3"
		scs := map[string]*v12.StorageClass{
			sc1: {
				ObjectMeta: metav1.ObjectMeta{
					Name: sc1,
				},
				Provisioner: SdsLocalVolumeProvisioner,
			},
			sc2: {
				ObjectMeta: metav1.ObjectMeta{
					Name: sc2,
				},
				Provisioner: SdsLocalVolumeProvisioner,
			},
			sc3: {
				ObjectMeta: metav1.ObjectMeta{
					Name: sc3,
				},
			},
		}
		pvcs := map[string]*v1.PersistentVolumeClaim{
			"first": {
				ObjectMeta: metav1.ObjectMeta{
					Name: "first",
				},
				Spec: v1.PersistentVolumeClaimSpec{
					StorageClassName: &sc1,
				},
			},
			"second": {
				ObjectMeta: metav1.ObjectMeta{
					Name: "second",
				},
				Spec: v1.PersistentVolumeClaimSpec{
					StorageClassName: &sc2,
				},
			},
			"third": {
				ObjectMeta: metav1.ObjectMeta{
					Name: "third",
				},
				Spec: v1.PersistentVolumeClaimSpec{
					StorageClassName: &sc3,
				},
			},
		}

		filtered := filterNotManagedPVC(log, pvcs, scs)

		if assert.Equal(t, 2, len(filtered)) {
			_, ok := filtered["first"]
			assert.True(t, ok)
			_, ok = filtered["second"]
			assert.True(t, ok)
			_, ok = filtered["third"]
			assert.False(t, ok)
		}
	})
}
