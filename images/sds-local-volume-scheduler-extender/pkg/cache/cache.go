package cache

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	"sds-local-volume-scheduler-extender/api/v1alpha1"
)

const pvcPerLVGCount = 150

type Cache struct {
	lvgs   map[string]*lvgCache
	pvcLVG map[string]string
}

type lvgCache struct {
	lvg  *v1alpha1.LvmVolumeGroup
	pvcs map[string]*pvcCache
}

type pvcCache struct {
	pvc      *v1.PersistentVolumeClaim
	nodeName string
}

func NewCache(size int) *Cache {
	return &Cache{
		lvgs:   make(map[string]*lvgCache, size),
		pvcLVG: make(map[string]string, size),
	}
}

func (c *Cache) AddLVG(lvg *v1alpha1.LvmVolumeGroup) error {
	if _, exist := c.lvgs[lvg.Name]; exist {
		return fmt.Errorf("the LVMVolumeGroup %s was found in the cache", lvg.Name)
	}

	c.lvgs[lvg.Name] = &lvgCache{
		lvg:  lvg,
		pvcs: make(map[string]*pvcCache, pvcPerLVGCount),
	}

	return nil
}

func (c *Cache) UpdateLVG(lvg *v1alpha1.LvmVolumeGroup) error {
	if cache, exist := c.lvgs[lvg.Name]; exist {
		cache.lvg = lvg
		return nil
	}

	return fmt.Errorf("the LVMVolumeGroup %s was not found in the cache", lvg.Name)
}

func (c *Cache) TryGetLVG(name string) *v1alpha1.LvmVolumeGroup {
	if c.lvgs[name] == nil {
		return nil
	}

	return c.lvgs[name].lvg
}

func (c *Cache) GetLVGNames() []string {
	names := make([]string, 0, len(c.lvgs))
	for lvgName := range c.lvgs {
		names = append(names, lvgName)
	}

	return names
}

func (c *Cache) GetLVGReservedSpace(lvgName string) int64 {
	lvg := c.lvgs[lvgName]

	var space int64
	for _, pvc := range lvg.pvcs {
		space += pvc.pvc.Spec.Resources.Requests.Storage().Value()
	}

	return space
}

func (c *Cache) DeleteLVG(lvgName string) {
	delete(c.lvgs, lvgName)
}

func (c *Cache) AddPVC(lvgName string, pvc *v1.PersistentVolumeClaim, pvcNodeName string) error {
	if c.lvgs[lvgName].pvcs[pvc.Name] != nil {
		return fmt.Errorf("PVC %s already exist in the cache", pvc.Name)
	}

	c.lvgs[lvgName].pvcs[pvc.Name] = &pvcCache{pvc: pvc, nodeName: pvcNodeName}
	c.pvcLVG[pvc.Name] = lvgName
	return nil
}

func (c *Cache) UpdatePVC(pvc *v1.PersistentVolumeClaim, pvcNodeName string) error {
	lvgName := c.pvcLVG[pvc.Name]
	if c.lvgs[lvgName].pvcs[pvc.Name] == nil {
		return fmt.Errorf("PVC %s not found", pvc.Name)
	}

	c.lvgs[lvgName].pvcs[pvc.Name].pvc = pvc
	c.lvgs[lvgName].pvcs[pvc.Name].nodeName = pvcNodeName
	return nil
}

func (c *Cache) TryGetPVC(name string) *v1.PersistentVolumeClaim {
	lvgName := c.pvcLVG[name]

	if c.lvgs[lvgName] == nil {
		return nil
	}

	return c.lvgs[lvgName].pvcs[name].pvc
}

//func (c *Cache) GetCorrespondingLVGNameByPVC(pvcName string) string {
//	return c.pvcLVG[pvcName]
//}

func (c *Cache) GetAllPVCByLVG(lvgName string) []*v1.PersistentVolumeClaim {
	pvcsCache := c.lvgs[lvgName]

	result := make([]*v1.PersistentVolumeClaim, 0, len(pvcsCache.pvcs))
	for _, cache := range pvcsCache.pvcs {
		result = append(result, cache.pvc)
	}

	return result
}

func (c *Cache) GetPVCNodeName(pvcName string) string {
	lvgName := c.pvcLVG[pvcName]
	return c.lvgs[lvgName].pvcs[pvcName].nodeName
}

func (c *Cache) GetAllPVCNames() []string {
	result := make([]string, 0, len(c.pvcLVG))

	for pvcName := range c.pvcLVG {
		result = append(result, pvcName)
	}

	return result
}

func (c *Cache) RemovePVCSpaceReservation(pvcName string) {
	lvgName := c.pvcLVG[pvcName]
	delete(c.lvgs[lvgName].pvcs, pvcName)
}
