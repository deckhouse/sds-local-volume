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
	pvcs map[string]*pvcCache
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
		pvcs: make(map[string]*pvcCache, pvcPerLVGCount),
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
	if lvg.(*lvgCache).pvcs == nil {
		err := fmt.Errorf("LVMVolumeGroup %s has no cached PVC", lvgName)
		c.log.Error(err, fmt.Sprintf("[GetLVGReservedSpace] an error occurs for the LVMVolumeGroup %s", lvgName))
		return 0, err
	}

	var space int64
	for _, pvc := range lvg.(*lvgCache).pvcs {
		space += pvc.pvc.Spec.Resources.Requests.Storage().Value()
	}

	return space, nil
}

func (c *Cache) DeleteLVG(lvgName string) {
	c.lvgs.Delete(lvgName)
}

func (c *Cache) AddPVCToLVG(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	if pvc.Status.Phase == v1.ClaimBound {
		c.log.Debug(fmt.Sprintf("[AddPVCToLVG] PVC %s/%s has status phase BOUND. It will not be added to the cache", pvc.Namespace, pvc.Name))
		return nil
	}

	// basically this case will be triggered if the controller recovers after fail and finds some pending pvcs with selected nodes,
	// but also it might be true if kube scheduler will retry the request for the same PVC
	c.log.Trace(fmt.Sprintf("[AddPVCToLVG] PVC %s/%s annotations: %v", pvc.Namespace, pvc.Name, pvc.Annotations))
	if pvc.Annotations[SelectedNodeAnnotation] != "" {
		c.log.Debug(fmt.Sprintf("PVC %s/%s has selected node anotation, selected node: %s", pvc.Namespace, pvc.Name, pvc.Annotations[SelectedNodeAnnotation]))
		pvcKey := configurePVCKey(pvc)

		lvgsOnTheNode, found := c.nodeLVGs.Load(pvc.Annotations[SelectedNodeAnnotation])
		if !found {
			err := fmt.Errorf("no LVMVolumeGroups found for the node %s", pvc.Annotations[SelectedNodeAnnotation])
			c.log.Error(err, fmt.Sprintf("[AddPVCToLVG] an error occured while trying to add PVC %s to the cache", pvcKey))
			return err
		}

		if !slices2.Contains(lvgsOnTheNode.([]string), lvgName) {
			c.log.Debug(fmt.Sprintf("[AddPVCToLVG] LVMVolumeGroup %s does not belong to PVC %s/%s selected node %s. It will be skipped", lvgName, pvc.Namespace, pvc.Name, pvc.Annotations[SelectedNodeAnnotation]))
			return nil
		}

		c.log.Debug(fmt.Sprintf("[AddPVCToLVG] LVMVolumeGroup %s belongs to PVC %s/%s selected node %s", lvgName, pvc.Namespace, pvc.Name, pvc.Annotations[SelectedNodeAnnotation]))
		lvgCh, found := c.lvgs.Load(lvgName)
		if !found {
			err := fmt.Errorf("the LVMVolumeGroup %s was not found in the cache", lvgName)
			c.log.Error(err, fmt.Sprintf("[AddPVCToLVG] an error occured while trying to add PVC %s to the cache", pvcKey))
			return err
		}

		_, found = lvgCh.(*lvgCache).pvcs[pvcKey]
		if found {
			c.log.Debug(fmt.Sprintf("[AddPVCToLVG] PVC %s cache has been already added to the LVMVolumeGroup %s. It will be updated", pvcKey, lvgName))
			err := c.UpdatePVC(lvgName, pvc)
			if err != nil {
				c.log.Error(err, fmt.Sprintf("[AddPVCToLVG] an error occured while trying to add PVC %s to the cache", pvcKey))
				return err
			}
		} else {
			c.log.Debug(fmt.Sprintf("[AddPVCToLVG] PVC %s cache was not found in LVMVolumeGroup %s. It will be added", pvcKey, lvgName))
			c.addNewPVC(lvgCh.(*lvgCache), pvc, lvgName)
		}

		return nil
	}

	pvcKey := configurePVCKey(pvc)

	c.log.Debug(fmt.Sprintf("[AddPVCToLVG] add new PVC %s cache to the LVMVolumeGroup %s", pvcKey, lvgName))
	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		return fmt.Errorf("the LVMVolumeGroup %s was not found in the cache", lvgName)
	}

	if pvcCh, found := lvgCh.(*lvgCache).pvcs[pvcKey]; found {
		c.log.Debug(fmt.Sprintf("[AddPVCToLVG] PVC %s has been already added to the cache for the LVMVolumeGroup %s. It will be updated", pvcKey, lvgName))
		if pvcCh == nil {
			err := fmt.Errorf("cache is not initialized for PVC %s", pvcKey)
			c.log.Error(err, fmt.Sprintf("[AddPVCToLVG] an error occured while trying to add PVC %s to the cache", pvcKey))
			return err
		}

		err := c.UpdatePVC(lvgName, pvc)
		if err != nil {
			c.log.Error(err, fmt.Sprintf("[AddPVCToLVG] an error occured while trying to add PVC %s to the cache", pvcKey))
			return err
		}
		return nil
	}

	c.log.Debug(fmt.Sprintf("new cache will be initialized for PVC %s in LVMVolumeGroup %s", pvcKey, lvgName))
	c.addNewPVC(lvgCh.(*lvgCache), pvc, lvgName)

	return nil
}

func (c *Cache) addNewPVC(lvgCh *lvgCache, pvc *v1.PersistentVolumeClaim, lvgName string) {
	pvcKey := configurePVCKey(pvc)
	lvgCh.pvcs[pvcKey] = &pvcCache{pvc: pvc, selectedNode: pvc.Annotations[SelectedNodeAnnotation]}

	lvgsForPVC, found := c.pvcLVGs.Load(pvcKey)
	if !found || lvgsForPVC == nil {
		lvgsForPVC = make([]string, 0, lvgsPerPVCCount)
	}

	c.log.Trace(fmt.Sprintf("[addNewPVC] LVMVolumeGroups from the cache for PVC %s before append: %v", pvcKey, lvgsForPVC))
	lvgsForPVC = append(lvgsForPVC.([]string), lvgName)
	c.log.Trace(fmt.Sprintf("[addNewPVC] LVMVolumeGroups from the cache for PVC %s after append: %v", pvcKey, lvgsForPVC))
	c.pvcLVGs.Store(pvcKey, lvgsForPVC)
}

func (c *Cache) UpdatePVC(lvgName string, pvc *v1.PersistentVolumeClaim) error {
	pvcKey := configurePVCKey(pvc)

	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		return fmt.Errorf("the LVMVolumeGroup %s was not found in the cache", lvgName)
	}

	if lvgCh.(*lvgCache).pvcs[pvcKey] == nil {
		c.log.Warning(fmt.Sprintf("[UpdatePVC] PVC %s was not found in the cache for the LVMVolumeGroup %s. It will be added", pvcKey, lvgName))
		err := c.AddPVCToLVG(lvgName, pvc)
		if err != nil {
			c.log.Error(err, fmt.Sprintf("[UpdatePVC] an error occurred while trying to update the PVC %s", pvcKey))
			return err
		}
		return nil
	}

	lvgCh.(*lvgCache).pvcs[pvcKey].pvc = pvc
	lvgCh.(*lvgCache).pvcs[pvcKey].selectedNode = pvc.Annotations[SelectedNodeAnnotation]
	c.log.Debug(fmt.Sprintf("[UpdatePVC] successfully updated PVC %s with selected node %s in the cache for LVMVolumeGroup %s", pvcKey, pvc.Annotations[SelectedNodeAnnotation], lvgName))

	lvgsForPVC, found := c.pvcLVGs.Load(pvcKey)
	if lvgsForPVC == nil || !found {
		lvgsForPVC = make([]string, 0, lvgsPerPVCCount)
	}

	if !slices2.Contains(lvgsForPVC.([]string), lvgName) {
		lvgsForPVC = append(lvgsForPVC.([]string), lvgName)
	}

	return nil
}

func (c *Cache) GetAllPVCForLVG(lvgName string) ([]*v1.PersistentVolumeClaim, error) {
	lvgCh, found := c.lvgs.Load(lvgName)
	if !found {
		err := fmt.Errorf("cache was not found for the LVMVolumeGroup %s", lvgName)
		c.log.Error(err, fmt.Sprintf("[GetAllPVCForLVG] an error occured while trying to get all PVC for the LVMVolumeGroup %s", lvgName))
		return nil, err
	}

	result := make([]*v1.PersistentVolumeClaim, 0, len(lvgCh.(*lvgCache).pvcs))
	for _, pvcCh := range lvgCh.(*lvgCache).pvcs {
		result = append(result, pvcCh.pvc)
	}

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

	if lvgCh.(*lvgCache).pvcs[pvcKey] == nil {
		err := fmt.Errorf("cache was not found for PVC %s", pvcKey)
		c.log.Error(err, fmt.Sprintf("[GetPVCSelectedNodeName] an error occured while trying to get selected node name for the PVC %s in the LVMVolumeGroup %s", pvcKey, lvgName))
		return "", err
	}

	return lvgCh.(*lvgCache).pvcs[pvcKey].selectedNode, nil
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

	pvcCh, found := lvgCh.(*lvgCache).pvcs[pvcKey]
	if !found || pvcCh == nil {
		err := fmt.Errorf("cache for PVC %s was not found", pvcKey)
		c.log.Error(err, fmt.Sprintf("[RemoveBoundedPVCSpaceReservation] an error occured while trying to remove space reservation for PVC %s in the LVMVolumeGroup %s", pvcKey, lvgName))
		return err
	}

	delete(lvgCh.(*lvgCache).pvcs, pvcKey)
	c.pvcLVGs.Delete(pvcKey)

	return nil
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

		if _, found := lvgCh.(*lvgCache).pvcs[pvcKey]; !found {
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] PVC %s space reservation in the LVMVolumeGroup %s has been already removed", pvcKey, lvgName))
			continue
		}

		selectedNode := lvgCh.(*lvgCache).pvcs[pvcKey].selectedNode
		if selectedNode == "" {
			delete(lvgCh.(*lvgCache).pvcs, pvcKey)
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] removed space reservation for PVC %s in the LVMVolumeGroup %s due the PVC got selected to the node %s", pvcKey, lvgName, selectedNode))
		} else {
			selectedLVGName = lvgName
			c.log.Debug(fmt.Sprintf("[RemoveSpaceReservationForPVCWithSelectedNode] PVC %s got selected to the node %s. It should not be revomed from the LVMVolumeGroup %s", pvcKey, selectedNode, lvgName))
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
					delete(lvgCh.(*lvgCache).pvcs, pvcKey.(string))
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

func (c *Cache) PrintTheCacheTraceLog() {
	c.log.Trace("*******************CACHE BEGIN*******************")
	c.log.Trace("[LVMVolumeGroups BEGIN]")
	c.lvgs.Range(func(lvgName, lvgCh any) bool {
		c.log.Trace(fmt.Sprintf("[%s]", lvgName))

		for pvcName, pvcCh := range lvgCh.(*lvgCache).pvcs {
			c.log.Trace(fmt.Sprintf("      PVC %s, selected node: %s", pvcName, pvcCh.selectedNode))
		}

		return true
	})

	c.log.Trace("[LVMVolumeGroups ENDS]")
	c.log.Trace("[PVC and LVG BEGINS]")
	c.pvcLVGs.Range(func(pvcName, lvgs any) bool {
		c.log.Trace(fmt.Sprintf("[PVC: %s]", pvcName))

		for _, lvgName := range lvgs.([]string) {
			c.log.Trace(fmt.Sprintf("      LVMVolumeGroup: %s", lvgName))
		}

		return true
	})

	c.log.Trace("[PVC and LVG ENDS]")
	c.log.Trace("[Node and LVG BEGINS]")
	c.nodeLVGs.Range(func(nodeName, lvgs any) bool {
		c.log.Trace(fmt.Sprintf("[Node: %s]", nodeName))

		for _, lvgName := range lvgs.([]string) {
			c.log.Trace(fmt.Sprintf("      LVMVolumeGroup name: %s", lvgName))
		}

		return true
	})
	c.log.Trace("[Node and LVG ENDS]")
	c.log.Trace("*******************CACHE END*******************")
}

func configurePVCKey(pvc *v1.PersistentVolumeClaim) string {
	return fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)
}
