package cache

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sds-local-volume-scheduler-extender/api/v1alpha1"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"testing"
)

func BenchmarkCache_DeleteLVG(b *testing.B) {
	cache := NewCache(logger.Logger{})
	lvg := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
		},
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.AddLVG(lvg)
			if _, found := cache.lvgs.Load(lvg.Name); found {
				//b.Log("lvg found, delete it")
				cache.DeleteLVG(lvg.Name)
			}
		}
	})
}

func BenchmarkCache_GetLVGReservedSpace(b *testing.B) {
	cache := NewCache(logger.Logger{})
	lvg := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
		},
	}

	cache.AddLVG(lvg)

	pvcs := []v1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pvc-1",
			},
			Spec: v1.PersistentVolumeClaimSpec{
				Resources: v1.VolumeResourceRequirements{
					Requests: v1.ResourceList{
						"pvc": *resource.NewQuantity(1000000, resource.BinarySI),
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pvc-2",
			},
			Spec: v1.PersistentVolumeClaimSpec{
				Resources: v1.VolumeResourceRequirements{
					Requests: v1.ResourceList{
						"pvc": *resource.NewQuantity(2000000, resource.BinarySI),
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pvc-3",
			},
			Spec: v1.PersistentVolumeClaimSpec{
				Resources: v1.VolumeResourceRequirements{
					Requests: v1.ResourceList{
						"pvc": *resource.NewQuantity(30000000, resource.BinarySI),
					},
				},
			},
		},
	}

	for _, pvc := range pvcs {
		cache.AddPVCToLVG(lvg.Name, &pvc)
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := cache.GetLVGReservedSpace(lvg.Name)
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func BenchmarkCache_GetAllLVG(b *testing.B) {
	cache := NewCache(logger.Logger{})
	lvgs := map[string]*lvgCache{
		"first": {
			lvg: &v1alpha1.LvmVolumeGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "first",
				},
			},
		},
		"second": {
			lvg: &v1alpha1.LvmVolumeGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "second",
				},
			},
		},
	}

	for _, lvg := range lvgs {
		cache.lvgs.Store(lvg.lvg.Name, lvg)
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			mp := cache.GetAllLVG()

			if len(mp) != 2 {
				b.Error("not enough LVG")
			}
		}
	})
}

func BenchmarkCache_GetLVGNamesByNodeName(b *testing.B) {
	cache := NewCache(logger.Logger{})
	lvgs := []string{
		"first",
		"second",
		"third",
	}
	nodeName := "test-node"

	cache.nodeLVGs.Store(nodeName, lvgs)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l := cache.GetLVGNamesByNodeName(nodeName)
			if len(l) != 3 {
				b.Error("not enough LVG")
			}
		}
	})
}

func BenchmarkCache_TryGetLVG(b *testing.B) {
	cache := NewCache(logger.Logger{})
	name := "test-name"

	lvg := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	cache.AddLVG(lvg)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l := cache.TryGetLVG(lvg.Name)
			if l == nil {
				b.Error("nil LVG from cache")
			}
		}
	})
}

func BenchmarkCache_AddLVG(b *testing.B) {
	cache := NewCache(logger.Logger{})
	i := 0

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i++
			lvg := &v1alpha1.LvmVolumeGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("test-lvg-%d", i),
				},
			}
			cache.AddLVG(lvg)
		}
	})
}

func BenchmarkCache_UpdateLVG(b *testing.B) {
	cache := NewCache(logger.Logger{})
	name := "test-name"
	i := 0

	lvg := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	cache.AddLVG(lvg)

	_, found := cache.lvgs.Load(name)
	if !found {
		b.Error("not found LVG")
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i++
			updated := &v1alpha1.LvmVolumeGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
				},
				Status: v1alpha1.LvmVolumeGroupStatus{
					AllocatedSize: fmt.Sprintf("2%dGi", i),
				},
			}
			b.Logf("updates the LVG with allocated size: %s", updated.Status.AllocatedSize)
			cache.UpdateLVG(updated)
		}
	})
}

func BenchmarkCache_UpdatePVC(b *testing.B) {
	cache := NewCache(logger.Logger{})
	i := 0
	lvg := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-lvg",
		},
	}
	cache.AddLVG(lvg)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i++
			pvc := &v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-pvc-%d", i),
					Namespace: "test-ns",
				},
			}
			err := cache.UpdatePVC(lvg.Name, pvc)
			if err != nil {
				b.Error(err)
			}
		}
	})
}
