package cache

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	"sds-local-volume-scheduler-extender/api/v1alpha1"
	"sds-local-volume-scheduler-extender/pkg/logger"
)

const (
	pvcPerLVGCount         = 150
	SelectedNodeAnnotation = "volume.kubernetes.io/selected-node"
)

type Cache struct {
	lvgs     map[string]*lvgCache
	pvcQueue map[string]*v1.PersistentVolumeClaim
	pvcLVGs  map[string][]string
	nodeLVGs map[string][]string
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
		lvgs:     make(map[string]*lvgCache, size),
		pvcQueue: make(map[string]*v1.PersistentVolumeClaim, size),
		pvcLVGs:  make(map[string][]string, size),
		nodeLVGs: make(map[string][]string, size),
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

	for _, node := range lvg.Status.Nodes {
		c.nodeLVGs[node.Name] = append(c.nodeLVGs[node.Name], lvg.Name)
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

func (c *Cache) GetLVGNamesByNodeName(nodeName string) []string {
	return c.nodeLVGs[nodeName]
}

func (c *Cache) GetAllLVG() map[string]*v1alpha1.LvmVolumeGroup {
	lvgs := make(map[string]*v1alpha1.LvmVolumeGroup, len(c.lvgs))
	for _, lvgCh := range c.lvgs {
		lvgs[lvgCh.lvg.Name] = lvgCh.lvg
	}

	return lvgs
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

func (c *Cache) AddPVCToFilterQueue(pvc *v1.PersistentVolumeClaim) {
	pvcKey := configurePVCKey(pvc)
	c.pvcQueue[pvcKey] = pvc
}

func (c *Cache) AddUnboundedPVCToLVG(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	if pvc.Annotations[SelectedNodeAnnotation] != "" {
		return fmt.Errorf("PVC %s is expected to not have a selected node, but got one: %s", pvc.Name, pvc.Annotations[SelectedNodeAnnotation])
	}

	pvcKey := configurePVCKey(pvc)
	if c.lvgs[lvgName].pvcs[pvcKey] != nil {
		return fmt.Errorf("PVC %s already exist in the cache", pvc.Name)
	}

	c.lvgs[lvgName].pvcs[pvcKey] = &pvcCache{pvc: pvc, nodeName: ""}
	c.pvcLVGs[pvcKey] = append(c.pvcLVGs[pvcKey], lvgName)

	return nil
}

func (c *Cache) UpdatePVC(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	pvcKey := configurePVCKey(pvc)
	if c.lvgs[lvgName].pvcs[pvcKey] == nil {
		return fmt.Errorf("PVC %s not found", pvc.Name)
	}

	c.lvgs[lvgName].pvcs[pvcKey].pvc = pvc
	c.lvgs[lvgName].pvcs[pvcKey].nodeName = pvc.Annotations[SelectedNodeAnnotation]

	return nil
}

func (c *Cache) TryGetPVC(lvgName string, pvc *v1.PersistentVolumeClaim) *v1.PersistentVolumeClaim {
	pvcKey := configurePVCKey(pvc)

	if c.lvgs[lvgName] == nil {
		return nil
	}

	return c.lvgs[lvgName].pvcs[pvcKey].pvc
}

func (c *Cache) CheckPVCInQueue(pvc *v1.PersistentVolumeClaim) bool {
	pvcKey := configurePVCKey(pvc)

	if _, exist := c.pvcQueue[pvcKey]; !exist {
		return false
	}

	return true
}

func (c *Cache) GetPVCsFromQueue(namespace string) map[string]*v1.PersistentVolumeClaim {
	result := make(map[string]*v1.PersistentVolumeClaim, len(c.pvcQueue))

	for _, pvc := range c.pvcQueue {
		if pvc.Namespace == namespace {
			result[pvc.Name] = pvc
		}
	}

	return result
}

func (c *Cache) GetAllPVCByLVG(lvgName string) []*v1.PersistentVolumeClaim {
	pvcsCache := c.lvgs[lvgName]

	result := make([]*v1.PersistentVolumeClaim, 0, len(pvcsCache.pvcs))
	for _, cache := range pvcsCache.pvcs {
		result = append(result, cache.pvc)
	}

	return result
}

func (c *Cache) GetPVCNodeName(lvgName string, pvc *v1.PersistentVolumeClaim) string {
	pvcKey := configurePVCKey(pvc)
	return c.lvgs[lvgName].pvcs[pvcKey].nodeName
}

func (c *Cache) GetLVGNamesForPVC(pvc *v1.PersistentVolumeClaim) []string {
	pvcKey := configurePVCKey(pvc)
	return c.pvcLVGs[pvcKey]
}

func (c *Cache) RemoveBoundedPVCSpaceReservation(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	pvcKey := configurePVCKey(pvc)
	pvcCh := c.lvgs[lvgName].pvcs[pvcKey]
	if pvcCh.nodeName == "" {
		return fmt.Errorf("no node selected for PVC %s", pvc.Name)
	}

	delete(c.lvgs[lvgName].pvcs, pvcKey)
	delete(c.pvcLVGs, pvcKey)
	delete(c.pvcQueue, pvcKey)

	return nil
}

func (c *Cache) RemoveUnboundedPVCSpaceReservation(log logger.Logger, pvc *v1.PersistentVolumeClaim) error {
	pvcKey := configurePVCKey(pvc)

	for _, lvgCh := range c.lvgs {
		pvcCh, exist := lvgCh.pvcs[pvcKey]
		if exist {
			if pvcCh.nodeName == "" {
				delete(lvgCh.pvcs, pvcKey)
				delete(c.pvcQueue, pvcKey)
				log.Debug(fmt.Sprintf("[RemoveUnboundedPVCSpaceReservation] removed unbound cache PVC %s from LVG %s", pvc.Name, lvgCh.lvg.Name))
				continue
			}

			log.Debug(fmt.Sprintf("[RemoveUnboundedPVCSpaceReservation] PVC %s has bounded to node %s. It should not be revomed from LVG %s", pvc.Name, pvcCh.nodeName, lvgCh.lvg.Name))
		}
	}

	return nil
}

func (c *Cache) RemovePVCSpaceReservationForced(pvc *v1.PersistentVolumeClaim) {
	pvcKey := configurePVCKey(pvc)

	for _, lvgName := range c.pvcLVGs[pvcKey] {
		delete(c.lvgs[lvgName].pvcs, pvcKey)
	}

	delete(c.pvcLVGs, pvcKey)
	delete(c.pvcQueue, pvcKey)
}

func configurePVCKey(pvc *v1.PersistentVolumeClaim) string {
	return fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)
}
