package cache

import (
	"fmt"
	"github.com/stretchr/testify/assert"
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
		err := cache.AddPVC(lvg.Name, &pvc)
		if err != nil {
			b.Error(err)
		}
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

func BenchmarkCache_AddPVC(b *testing.B) {
	cache := NewCache(logger.Logger{})

	lvg1 := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
		},
		Status: v1alpha1.LvmVolumeGroupStatus{
			Nodes: []v1alpha1.LvmVolumeGroupNode{
				{Name: "test-node1"},
			},
		},
	}
	lvg2 := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "second",
		},
		Status: v1alpha1.LvmVolumeGroupStatus{
			Nodes: []v1alpha1.LvmVolumeGroupNode{
				{Name: "test-node2"},
			},
		},
	}
	lvg3 := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "third",
		},
		Status: v1alpha1.LvmVolumeGroupStatus{
			Nodes: []v1alpha1.LvmVolumeGroupNode{
				{Name: "test-node3"},
			},
		},
	}
	cache.AddLVG(lvg1)
	cache.AddLVG(lvg2)
	cache.AddLVG(lvg3)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			pvc := &v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "test-ns",
				},
				Status: v1.PersistentVolumeClaimStatus{
					Phase: v1.ClaimPending,
				},
			}

			err := cache.AddPVC(lvg1.Name, pvc)
			if err != nil {
				b.Error(err)
			}
			err = cache.AddPVC(lvg2.Name, pvc)
			if err != nil {
				b.Error(err)
			}
			err = cache.AddPVC(lvg3.Name, pvc)
			if err != nil {
				b.Error(err)
			}

			lvgs := cache.GetLVGNamesForPVC(pvc)
			b.Log(lvgs)
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
			lvg1 := &v1alpha1.LvmVolumeGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("test-lvg-%d", i),
				},
				Status: v1alpha1.LvmVolumeGroupStatus{
					Nodes: []v1alpha1.LvmVolumeGroupNode{
						{
							Name: "test-1",
						},
					},
				},
			}

			lvg2 := &v1alpha1.LvmVolumeGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("test-lvg-%d", i+1),
				},
				Status: v1alpha1.LvmVolumeGroupStatus{
					Nodes: []v1alpha1.LvmVolumeGroupNode{
						{
							Name: "test-1",
						},
					},
				},
			}

			lvg3 := &v1alpha1.LvmVolumeGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("test-lvg-%d", i+2),
				},
				Status: v1alpha1.LvmVolumeGroupStatus{
					Nodes: []v1alpha1.LvmVolumeGroupNode{
						{
							Name: "test-1",
						},
					},
				},
			}

			cache.AddLVG(lvg1)
			cache.AddLVG(lvg2)
			cache.AddLVG(lvg3)

			lvgs, _ := cache.nodeLVGs.Load("test-1")
			b.Log(lvgs.([]string))
		}
	})
}

func TestCache_UpdateLVG(t *testing.T) {
	cache := NewCache(logger.Logger{})
	name := "test-lvg"
	lvg := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: v1alpha1.LvmVolumeGroupStatus{
			AllocatedSize: resource.MustParse("1Gi"),
		},
	}
	cache.AddLVG(lvg)

	newLVG := &v1alpha1.LvmVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: v1alpha1.LvmVolumeGroupStatus{
			AllocatedSize: resource.MustParse("2Gi"),
		},
	}

	err := cache.UpdateLVG(newLVG)
	if err != nil {
		t.Error(err)
	}

	updatedLvg := cache.TryGetLVG(name)
	assert.Equal(t, newLVG.Status.AllocatedSize, updatedLvg.Status.AllocatedSize)
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
					AllocatedSize: resource.MustParse(fmt.Sprintf("2%dGi", i)),
				},
			}
			b.Logf("updates the LVG with allocated size: %s", updated.Status.AllocatedSize.String())
			err := cache.UpdateLVG(updated)
			if err != nil {
				b.Error(err)
			}
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
		Status: v1alpha1.LvmVolumeGroupStatus{
			Nodes: []v1alpha1.LvmVolumeGroupNode{
				{
					Name: "test-node",
				},
			},
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

			updatedPVC := &v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-pvc-%d", i),
					Namespace: "test-ns",
					Annotations: map[string]string{
						SelectedNodeAnnotation: "test-node",
					},
				},
			}
			err := cache.UpdatePVC(lvg.Name, pvc)
			if err != nil {
				b.Error(err)
			}
			err = cache.UpdatePVC(lvg.Name, updatedPVC)
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func BenchmarkCache_FullLoad(b *testing.B) {
	cache := NewCache(logger.Logger{})

	const (
		nodeName = "test-node"
	)

	i := 0
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i++

			lvgs := []*v1alpha1.LvmVolumeGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("test-lvg-%d", i),
					},
					Status: v1alpha1.LvmVolumeGroupStatus{
						Nodes: []v1alpha1.LvmVolumeGroupNode{
							{
								Name: nodeName,
							},
						},
						AllocatedSize: resource.MustParse(fmt.Sprintf("1%dGi", i)),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("test-lvg-%d", i+1),
					},
					Status: v1alpha1.LvmVolumeGroupStatus{
						Nodes: []v1alpha1.LvmVolumeGroupNode{
							{
								Name: nodeName,
							},
						},
						AllocatedSize: resource.MustParse(fmt.Sprintf("1%dGi", i)),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("test-lvg-%d", i+2),
					},
					Status: v1alpha1.LvmVolumeGroupStatus{
						Nodes: []v1alpha1.LvmVolumeGroupNode{
							{
								Name: nodeName,
							},
						},
						AllocatedSize: resource.MustParse(fmt.Sprintf("1%dGi", i)),
					},
				},
			}

			for _, lvg := range lvgs {
				cache.AddLVG(lvg)
				pvcs := []*v1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pvc-%d", i),
							Namespace: "test-ns",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pvc-%d", i+1),
							Namespace: "test-ns",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pvc-%d", i+2),
							Namespace: "test-ns",
						},
					},
				}

				for _, pvc := range pvcs {
					err := cache.AddPVC(lvg.Name, pvc)
					if err != nil {
						b.Error(err)
					}

					cache.GetLVGNamesForPVC(pvc)
				}
			}

			updatedLvgs := []*v1alpha1.LvmVolumeGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("test-lvg-%d", i),
					},
					Status: v1alpha1.LvmVolumeGroupStatus{
						AllocatedSize: resource.MustParse(fmt.Sprintf("1%dGi", i+1)),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("test-lvg-%d", i+1),
					},
					Status: v1alpha1.LvmVolumeGroupStatus{
						AllocatedSize: resource.MustParse(fmt.Sprintf("1%dGi", i+1)),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("test-lvg-%d", i+2),
					},
					Status: v1alpha1.LvmVolumeGroupStatus{
						AllocatedSize: resource.MustParse(fmt.Sprintf("1%dGi", i+1)),
					},
				},
			}

			for _, lvg := range updatedLvgs {
				var err error
				for err != nil {
					err = cache.UpdateLVG(lvg)
				}

				pvcs := []*v1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pvc-%d", i),
							Namespace: "test-ns",
							Annotations: map[string]string{
								SelectedNodeAnnotation: nodeName,
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pvc-%d", i+1),
							Namespace: "test-ns",
							Annotations: map[string]string{
								SelectedNodeAnnotation: nodeName,
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pvc-%d", i+2),
							Namespace: "test-ns",
							Annotations: map[string]string{
								SelectedNodeAnnotation: nodeName,
							},
						},
					},
				}

				for _, pvc := range pvcs {
					for err != nil {
						err = cache.UpdatePVC(lvg.Name, pvc)
					}

					cache.GetLVGNamesForPVC(pvc)
				}
			}

			lvgMp := cache.GetAllLVG()
			for lvgName := range lvgMp {
				_, err := cache.GetAllPVCForLVG(lvgName)
				if err != nil {
					b.Error(err)
				}
				_, err = cache.GetLVGReservedSpace(lvgName)
				if err != nil {
					b.Error(err)
				}
			}

			cache.GetLVGNamesByNodeName(nodeName)
		}
	})
}
