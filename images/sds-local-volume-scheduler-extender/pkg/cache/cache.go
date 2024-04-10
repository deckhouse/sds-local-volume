package cache

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	slices2 "k8s.io/utils/strings/slices"
	"sds-local-volume-scheduler-extender/api/v1alpha1"
	"sds-local-volume-scheduler-extender/pkg/logger"
	"sync"
)

const (
	pvcPerLVGCount         = 150
	lvgsPerPVCCount        = 5
	SelectedNodeAnnotation = "volume.kubernetes.io/selected-node"
)

type Cache struct {
	lvgs     sync.Map
	pvcLVGs  sync.Map
	nodeLVGs sync.Map
	log      logger.Logger
}

type lvgCache struct {
	lvg  *v1alpha1.LvmVolumeGroup
	pvcs sync.Map
}

type pvcCache struct {
	pvc          *v1.PersistentVolumeClaim
	selectedNode string
}

func NewCache(logger logger.Logger) *Cache {
	return &Cache{
		log: logger,
	}
}

func (c *Cache) AddLVG(lvg *v1alpha1.LvmVolumeGroup) {
	_, loaded := c.lvgs.LoadOrStore(lvg.Name, &lvgCache{
		lvg:  lvg,
		pvcs: sync.Map{},
	})
	if loaded {
		c.log.Debug(fmt.Sprintf("[AddLVG] the LVMVolumeGroup %s has been already added to the cache", lvg.Name))
		return
	}

	for _, node := range lvg.Status.Nodes {
		lvgsOnTheNode, _ := c.nodeLVGs.Load(node.Name)
		if lvgsOnTheNode == nil {
			lvgsOnTheNode = make([]string, 0, 5)
		}

		lvgsOnTheNode = append(lvgsOnTheNode.([]string), lvg.Name)
		c.nodeLVGs.Store(node.Name, lvgsOnTheNode)
	}
}

func (c *Cache) UpdateLVG(lvg *v1alpha1.LvmVolumeGroup) error {
	if cache, found := c.lvgs.Load(lvg.Name); found {
		cache.(*lvgCache).lvg = lvg
		return nil
	}

	return fmt.Errorf("the LVMVolumeGroup %s was not found in the cache", lvg.Name)
}

func (c *Cache) TryGetLVG(name string) *v1alpha1.LvmVolumeGroup {
	lvgCh, found := c.lvgs.Load(name)
	if !found {
		c.log.Debug(fmt.Sprintf("[TryGetLVG] the LVMVolumeGroup %s was not found in the cache. Return nil", name))
		return nil
	}

	return lvgCh.(*lvgCache).lvg
}

func (c *Cache) GetLVGNamesByNodeName(nodeName string) []string {
	lvgs, found := c.nodeLVGs.Load(nodeName)
	if !found {
		c.log.Debug(fmt.Sprintf("[GetLVGNamesByNodeName] no LVMVolumeGroup was found in the cache for the node %s. Return empty slice", nodeName))
		return []string{}
	}

	return lvgs.([]string)
}

func (c *Cache) GetAllLVG() map[string]*v1alpha1.LvmVolumeGroup {
	lvgs := make(map[string]*v1alpha1.LvmVolumeGroup)
	c.lvgs.Range(func(lvgName, lvgCh any) bool {
		if lvgCh.(*lvgCache).lvg == nil {
			c.log.Error(fmt.Errorf("LVMVolumeGroup %s is not initialized", lvgName), fmt.Sprintf("[GetAllLVG] an error occurs while iterating the LVMVolumeGroups"))
			return true
		}

		lvgs[lvgName.(string)] = lvgCh.(*lvgCache).lvg
		return true
	})

	return lvgs
}

func (c *Cache) GetLVGReservedSpace(lvgName string) (int64, error) {
	lvg, found := c.lvgs.Load(lvgName)
	if !found {
		c.log.Debug(fmt.Sprintf("[GetLVGReservedSpace] the LVMVolumeGroup %s was not found in the cache. Returns 0", lvgName))
		return 0, nil
	}

	var space int64
	lvg.(*lvgCache).pvcs.Range(func(pvcName, pvcCh any) bool {
		space += pvcCh.(*pvcCache).pvc.Spec.Resources.Requests.Storage().Value()
		return true
	})

	return space, nil
}

func (c *Cache) DeleteLVG(lvgName string) {
	c.lvgs.Delete(lvgName)
}

func (c *Cache) AddPVC(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	if pvc.Status.Phase == v1.ClaimBound {
		c.log.Warning(fmt.Sprintf("[AddPVC] PVC %s/%s has status phase BOUND. It will not be added to the cache", pvc.Namespace, pvc.Name))
		return nil
	}

	pvcKey := configurePVCKey(pvc)

	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		err := fmt.Errorf("the LVMVolumeGroup %s was not found in the cache", lvgName)
		c.log.Error(err, fmt.Sprintf("[AddPVC] an error occured while trying to add PVC %s to the cache", pvcKey))
		return err
	}

	// this case might be triggered if the extender recovers after fail and finds some pending pvcs with selected nodes
	c.log.Trace(fmt.Sprintf("[AddPVC] PVC %s/%s annotations: %v", pvc.Namespace, pvc.Name, pvc.Annotations))
	if pvc.Annotations[SelectedNodeAnnotation] != "" {
		c.log.Debug(fmt.Sprintf("[AddPVC] PVC %s/%s has selected node anotation, selected node: %s", pvc.Namespace, pvc.Name, pvc.Annotations[SelectedNodeAnnotation]))

		lvgsOnTheNode, found := c.nodeLVGs.Load(pvc.Annotations[SelectedNodeAnnotation])
		if !found {
			err := fmt.Errorf("no LVMVolumeGroups found for the node %s", pvc.Annotations[SelectedNodeAnnotation])
			c.log.Error(err, fmt.Sprintf("[AddPVC] an error occured while trying to add PVC %s to the cache", pvcKey))
			return err
		}

		if !slices2.Contains(lvgsOnTheNode.([]string), lvgName) {
			c.log.Debug(fmt.Sprintf("[AddPVC] LVMVolumeGroup %s does not belong to PVC %s/%s selected node %s. It will be skipped", lvgName, pvc.Namespace, pvc.Name, pvc.Annotations[SelectedNodeAnnotation]))
			return nil
		}

		c.log.Debug(fmt.Sprintf("[AddPVC] LVMVolumeGroup %s belongs to PVC %s/%s selected node %s", lvgName, pvc.Namespace, pvc.Name, pvc.Annotations[SelectedNodeAnnotation]))

		_, found = lvgCh.(*lvgCache).pvcs.Load(pvcKey)
		if found {
			c.log.Debug(fmt.Sprintf("[AddPVC] PVC %s cache has been already added to the LVMVolumeGroup %s", pvcKey, lvgName))
			return nil
		}
	}

	c.log.Debug(fmt.Sprintf("[AddPVC] new PVC %s cache will be added to the LVMVolumeGroup %s", pvcKey, lvgName))
	c.addNewPVC(lvgCh.(*lvgCache), pvc)

	return nil
}

func (c *Cache) addNewPVC(lvgCh *lvgCache, pvc *v1.PersistentVolumeClaim) {
	pvcKey := configurePVCKey(pvc)
	lvgCh.pvcs.Store(pvcKey, &pvcCache{pvc: pvc, selectedNode: pvc.Annotations[SelectedNodeAnnotation]})

	lvgsForPVC, found := c.pvcLVGs.Load(pvcKey)
	if !found || lvgsForPVC == nil {
		lvgsForPVC = make([]string, 0, lvgsPerPVCCount)
	}

	c.log.Trace(fmt.Sprintf("[addNewPVC] LVMVolumeGroups from the cache for PVC %s before append: %v", pvcKey, lvgsForPVC))
	lvgsForPVC = append(lvgsForPVC.([]string), lvgCh.lvg.Name)
	c.log.Trace(fmt.Sprintf("[addNewPVC] LVMVolumeGroups from the cache for PVC %s after append: %v", pvcKey, lvgsForPVC))
	c.pvcLVGs.Store(pvcKey, lvgsForPVC)
}

func (c *Cache) UpdatePVC(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	pvcKey := configurePVCKey(pvc)

	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		return fmt.Errorf("the LVMVolumeGroup %s was not found in the cache", lvgName)
	}

	pvcCh, found := lvgCh.(*lvgCache).pvcs.Load(pvcKey)
	if !found {
		c.log.Warning(fmt.Sprintf("[UpdatePVC] PVC %s was not found in the cache for the LVMVolumeGroup %s. It will be added", pvcKey, lvgName))
		err := c.AddPVC(lvgName, pvc)
		if err != nil {
			c.log.Error(err, fmt.Sprintf("[UpdatePVC] an error occurred while trying to update the PVC %s", pvcKey))
			return err
		}
		return nil
	}

	pvcCh.(*pvcCache).pvc = pvc
	pvcCh.(*pvcCache).selectedNode = pvc.Annotations[SelectedNodeAnnotation]
	c.log.Debug(fmt.Sprintf("[UpdatePVC] successfully updated PVC %s with selected node %s in the cache for LVMVolumeGroup %s", pvcKey, pvc.Annotations[SelectedNodeAnnotation], lvgName))

	return nil
}

func (c *Cache) GetAllPVCForLVG(lvgName string) ([]*v1.PersistentVolumeClaim, error) {
	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		err := fmt.Errorf("cache was not found for the LVMVolumeGroup %s", lvgName)
		c.log.Error(err, fmt.Sprintf("[GetAllPVCForLVG] an error occured while trying to get all PVC for the LVMVolumeGroup %s", lvgName))
		return nil, err
	}

	result := make([]*v1.PersistentVolumeClaim, 0, pvcPerLVGCount)
	lvgCh.(*lvgCache).pvcs.Range(func(pvcName, pvcCh any) bool {
		result = append(result, pvcCh.(*pvcCache).pvc)
		return true
	})

	return result, nil
}

func (c *Cache) GetPVCSelectedNodeName(lvgName string, pvc *v1.PersistentVolumeClaim) (string, error) {
	pvcKey := configurePVCKey(pvc)
	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		err := fmt.Errorf("cache was not found for the LVMVolumeGroup %s", lvgName)
		c.log.Error(err, fmt.Sprintf("[GetPVCSelectedNodeName] an error occured while trying to get selected node name for PVC %s in the LVMVolumeGroup %s", pvcKey, lvgName))
		return "", err
	}

	pvcCh, found := lvgCh.(*lvgCache).pvcs.Load(pvcKey)
	if !found {
		err := fmt.Errorf("cache was not found for PVC %s", pvcKey)
		c.log.Error(err, fmt.Sprintf("[GetPVCSelectedNodeName] an error occured while trying to get selected node name for the PVC %s in the LVMVolumeGroup %s", pvcKey, lvgName))
		return "", err
	}

	return pvcCh.(*pvcCache).selectedNode, nil
}

func (c *Cache) GetLVGNamesForPVC(pvc *v1.PersistentVolumeClaim) []string {
	pvcKey := configurePVCKey(pvc)
	lvgNames, found := c.pvcLVGs.Load(pvcKey)
	if !found {
		c.log.Warning(fmt.Sprintf("[GetLVGNamesForPVC] no cached LVMVolumeGroups were found for PVC %s", pvcKey))
		return nil
	}

	return lvgNames.([]string)
}

func (c *Cache) RemoveBoundedPVCSpaceReservation(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	pvcKey := configurePVCKey(pvc)

	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		err := fmt.Errorf("LVMVolumeGroup %s was not found in the cache", lvgName)
		c.log.Error(err, fmt.Sprintf("[RemoveBoundedPVCSpaceReservation] an error occured while trying to remove space reservation for PVC %s in the LVMVolumeGroup %s", pvcKey, lvgName))
		return err
	}

	pvcCh, found := lvgCh.(*lvgCache).pvcs.Load(pvcKey)
	if !found || pvcCh == nil {
		err := fmt.Errorf("cache for PVC %s was not found", pvcKey)
		c.log.Error(err, fmt.Sprintf("[RemoveBoundedPVCSpaceReservation] an error occured while trying to remove space reservation for PVC %s in the LVMVolumeGroup %s", pvcKey, lvgName))
		return err
	}

	lvgCh.(*lvgCache).pvcs.Delete(pvcKey)
	c.pvcLVGs.Delete(pvcKey)

	return nil
}

func (c *Cache) CheckIsPVCCached(pvc *v1.PersistentVolumeClaim) bool {
	pvcKey := configurePVCKey(pvc)
	if _, found := c.pvcLVGs.Load(pvcKey); found {
		return true
	}

	return false
}

func (c *Cache) RemoveSpaceReservationForPVCWithSelectedNode(pvc *v1.PersistentVolumeClaim) error {
	pvcKey := configurePVCKey(pvc)
	selectedLVGName := ""

	lvgArray, found := c.pvcLVGs.Load(pvcKey)
	if !found {
		c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] cache for PVC %s has been already removed", pvcKey))
		return nil
	}

	for _, lvgName := range lvgArray.([]string) {
		lvgCh, found := c.lvgs.Load(lvgName)
		if !found || lvgCh == nil {
			err := fmt.Errorf("no cache found for the LVMVolumeGroup %s", lvgName)
			c.log.Error(err, fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] an error occured while trying to remove space reservation for PVC %s", pvcKey))
			return err
		}

		pvcCh, found := lvgCh.(*lvgCache).pvcs.Load(pvcKey)
		if !found {
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] PVC %s space reservation in the LVMVolumeGroup %s has been already removed", pvcKey, lvgName))
			continue
		}

		selectedNode := pvcCh.(*pvcCache).selectedNode
		if selectedNode == "" {
			lvgCh.(*lvgCache).pvcs.Delete(pvcKey)
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] removed space reservation for PVC %s in the LVMVolumeGroup %s due the PVC got selected to the node %s", pvcKey, lvgName, pvc.Annotations[SelectedNodeAnnotation]))
		} else {
			selectedLVGName = lvgName
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] PVC %s got selected to the node %s. It should not be revomed from the LVMVolumeGroup %s", pvcKey, pvc.Annotations[SelectedNodeAnnotation], lvgName))
		}
	}
	c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] PVC %s space reservation has been removed from LVMVolumeGroup cache", pvcKey))

	c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] cache for PVC %s will be wiped from unused LVMVolumeGroups", pvcKey))
	cleared := make([]string, 0, len(lvgArray.([]string)))
	for _, lvgName := range lvgArray.([]string) {
		if lvgName == selectedLVGName {
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] the LVMVolumeGroup %s will be saved for PVC %s cache as used", lvgName, pvcKey))
			cleared = append(cleared, lvgName)
		} else {
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] the LVMVolumeGroup %s will be removed from PVC %s cache as not used", lvgName, pvcKey))
		}
	}
	c.log.Trace(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] cleared LVMVolumeGroups for PVC %s: %v", pvcKey, cleared))
	c.pvcLVGs.Store(pvcKey, cleared)

	return nil
}

func (c *Cache) RemovePVCSpaceReservationForced(pvc *v1.PersistentVolumeClaim) {
	targetPvcKey := configurePVCKey(pvc)

	c.log.Debug(fmt.Sprintf("[RemovePVCSpaceReservationForced] run full cache wipe for PVC %s", targetPvcKey))
	c.pvcLVGs.Range(func(pvcKey, lvgArray any) bool {
		if pvcKey == targetPvcKey {
			for _, lvgName := range lvgArray.([]string) {
				lvgCh, found := c.lvgs.Load(lvgName)
				if found {
					lvgCh.(*lvgCache).pvcs.Delete(pvcKey.(string))
				}
			}
		}

		return true
	})

	c.pvcLVGs.Delete(targetPvcKey)
}

func (c *Cache) FindLVGForPVCBySelectedNode(pvc *v1.PersistentVolumeClaim, nodeName string) string {
	pvcKey := configurePVCKey(pvc)

	lvgsForPVC, found := c.pvcLVGs.Load(pvcKey)
	if !found {
		c.log.Debug(fmt.Sprintf("[FindLVGForPVCBySelectedNode] no LVMVolumeGroups were found in the cache for PVC %s. Returns empty string", pvcKey))
		return ""
	}

	lvgsOnTheNode, found := c.nodeLVGs.Load(nodeName)
	if !found {
		c.log.Debug(fmt.Sprintf("[FindLVGForPVCBySelectedNode] no LVMVolumeGroups were found in the cache for the node %s. Returns empty string", nodeName))
		return ""
	}

	var targetLVG string
	for _, lvgName := range lvgsForPVC.([]string) {
		if slices2.Contains(lvgsOnTheNode.([]string), lvgName) {
			targetLVG = lvgName
		}
	}

	if targetLVG == "" {
		c.log.Debug(fmt.Sprintf("[FindLVGForPVCBySelectedNode] no LVMVolumeGroup was found for PVC %s. Returns empty string", pvcKey))
	}

	return targetLVG
}

func (c *Cache) PrintTheCacheLog() {
	c.log.Cache("*******************CACHE BEGIN*******************")
	c.log.Cache("[LVMVolumeGroups BEGIN]")
	c.lvgs.Range(func(lvgName, lvgCh any) bool {
		c.log.Cache(fmt.Sprintf("[%s]", lvgName))

		lvgCh.(*lvgCache).pvcs.Range(func(pvcName, pvcCh any) bool {
			c.log.Cache(fmt.Sprintf("      PVC %s, selected node: %s", pvcName, pvcCh.(*pvcCache).selectedNode))
			return true
		})

		return true
	})

	c.log.Cache("[LVMVolumeGroups ENDS]")
	c.log.Cache("[PVC and LVG BEGINS]")
	c.pvcLVGs.Range(func(pvcName, lvgs any) bool {
		c.log.Cache(fmt.Sprintf("[PVC: %s]", pvcName))

		for _, lvgName := range lvgs.([]string) {
			c.log.Cache(fmt.Sprintf("      LVMVolumeGroup: %s", lvgName))
		}

		return true
	})

	c.log.Cache("[PVC and LVG ENDS]")
	c.log.Cache("[Node and LVG BEGINS]")
	c.nodeLVGs.Range(func(nodeName, lvgs any) bool {
		c.log.Cache(fmt.Sprintf("[Node: %s]", nodeName))

		for _, lvgName := range lvgs.([]string) {
			c.log.Cache(fmt.Sprintf("      LVMVolumeGroup name: %s", lvgName))
		}

		return true
	})
	c.log.Cache("[Node and LVG ENDS]")
	c.log.Cache("*******************CACHE END*******************")
}

func configurePVCKey(pvc *v1.PersistentVolumeClaim) string {
	return fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)
}
