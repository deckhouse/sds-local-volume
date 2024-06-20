/*
Copyright 2024 Flant JSC

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

package controller

import (
	"context"
	"fmt"
	v1alpha1 "sds-local-volume-controller/api/v1alpha1"
	"sds-local-volume-controller/pkg/logger"
	"strings"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func reconcileLSCDeleteFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	lsc *v1alpha1.LocalStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] tries to find a storage class for the LocalStorageClass %s", lsc.Name))
	var sc *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == lsc.Name {
			sc = &s
			break
		}
	}
	if sc == nil {
		log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] no storage class found for the LocalStorageClass, name: %s", lsc.Name))
	}

	if sc != nil {
		log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] successfully found a storage class for the LocalStorageClass %s", lsc.Name))
		log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] starts identifing a provisioner for the storage class %s", sc.Name))

		if sc.Provisioner != LocalStorageClassProvisioner {
			log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] the storage class %s does not belongs to %s provisioner. It will not be deleted", sc.Name, LocalStorageClassProvisioner))
		} else {
			log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] the storage class %s belongs to %s provisioner. It will be deleted", sc.Name, LocalStorageClassProvisioner))

			err := deleteStorageClass(ctx, cl, sc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to delete a storage class, name: %s", sc.Name))
				upErr := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to delete a storage class, err: %s", err.Error()))
				if upErr != nil {
					log.Error(upErr, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to update the LocalStorageClass, name: %s", lsc.Name))
				}
				return true, err
			}
			log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] successfully deleted a storage class, name: %s", sc.Name))
		}
	}

	log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] starts removing a finalizer %s from the LocalStorageClass, name: %s", LocalStorageClassFinalizerName, lsc.Name))
	removed, err := removeFinalizerIfExists(ctx, cl, lsc, LocalStorageClassFinalizerName)
	if err != nil {
		log.Error(err, "[reconcileLSCDeleteFunc] unable to remove a finalizer %s from the LocalStorageClass, name: %s", LocalStorageClassFinalizerName, lsc.Name)
		upErr := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to remove a finalizer, err: %s", err.Error()))
		if upErr != nil {
			log.Error(upErr, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to update the LocalStorageClass, name: %s", lsc.Name))
		}
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] the LocalStorageClass %s finalizer %s was removed: %t", lsc.Name, LocalStorageClassFinalizerName, removed))

	log.Debug("[reconcileLSCDeleteFunc] ends the reconciliation")
	return false, nil
}

func reconcileLSCUpdateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	lsc *v1alpha1.LocalStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] starts the LocalStorageClass %s validation", lsc.Name))
	valid, msg := validateLocalStorageClass(ctx, cl, scList, lsc)
	if !valid {
		err := fmt.Errorf("validation failed: %s", msg)
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] Unable to reconcile the LocalStorageClass, name: %s", lsc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, msg)
		if upError != nil {
			log.Error(upError, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update the LocalStorageClass %s", lsc.Name))
		}

		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully validated the LocalStorageClass, name: %s", lsc.Name))

	var oldSC *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == lsc.Name {
			oldSC = &s
			break
		}
	}
	if oldSC == nil {
		err := fmt.Errorf("a storage class %s does not exist", lsc.Name)
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to find a storage class for the LocalStorageClass, name: %s", lsc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error(upError, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update the LocalStorageClass %s", lsc.Name))
		}
		return true, err
	}

	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully found a storage class for the LocalStorageClass, name: %s", lsc.Name))

	log.Trace(fmt.Sprintf("[reconcileLSCUpdateFunc] storage class %s params: %+v", oldSC.Name, oldSC.Parameters))
	log.Trace(fmt.Sprintf("[reconcileLSCUpdateFunc] LocalStorageClass %s Spec.LVM: %+v", lsc.Name, lsc.Spec.LVM))
	hasDiff, err := hasLVGDiff(oldSC, lsc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to identify the LVMVolumeGroup difference for the LocalStorageClass %s", lsc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error(upError, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update the LocalStorageClass %s", lsc.Name))
		}
		return true, err
	}

	if hasDiff {
		log.Info(fmt.Sprintf("[reconcileLSCUpdateFunc] current Storage Class LVMVolumeGroups do not match LocalStorageClass ones. The Storage Class %s will be recreated with new ones", lsc.Name))
		newSC, err := updateStorageClass(lsc, oldSC)
		if err != nil {
			log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to configure a Storage Class for the LocalStorageClass %s", lsc.Name))
			upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
			if upError != nil {
				log.Error(upError, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update the LocalStorageClass %s", lsc.Name))
				return true, upError
			}
			return false, err
		}

		err = recreateStorageClass(ctx, cl, oldSC, newSC)
		if err != nil {
			log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to recreate a Storage Class %s", newSC.Name))
			upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
			if upError != nil {
				log.Error(upError, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update the LocalStorageClass %s", lsc.Name))
			}
			return true, err
		}

		log.Info(fmt.Sprintf("[reconcileLSCUpdateFunc] a Storage Class %s was successfully recreated", newSC.Name))
	}

	err = updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update the LocalStorageClass, name: %s", lsc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully updated the LocalStorageClass %s status", lsc.Name))

	return false, nil
}

func identifyReconcileFunc(scList *v1.StorageClassList, lsc *v1alpha1.LocalStorageClass) (reconcileType, error) {
	if shouldReconcileByDeleteFunc(lsc) {
		return DeleteReconcile, nil
	}

	if shouldReconcileByCreateFunc(scList, lsc) {
		return CreateReconcile, nil
	}

	should, err := shouldReconcileByUpdateFunc(scList, lsc)
	if err != nil {
		return "none", err
	}
	if should {
		return UpdateReconcile, nil
	}

	return "none", nil
}

func shouldReconcileByDeleteFunc(lsc *v1alpha1.LocalStorageClass) bool {
	if lsc.DeletionTimestamp != nil {
		return true
	}

	return false
}

func shouldReconcileByUpdateFunc(scList *v1.StorageClassList, lsc *v1alpha1.LocalStorageClass) (bool, error) {
	if lsc.DeletionTimestamp != nil {
		return false, nil
	}

	for _, sc := range scList.Items {
		if sc.Name == lsc.Name {
			if sc.Provisioner == LocalStorageClassProvisioner {
				diff, err := hasLVGDiff(&sc, lsc)
				if err != nil {
					return false, err
				}

				if diff {
					return true, nil
				}

				if lsc.Status.Phase == FailedStatusPhase {
					return true, nil
				}

				return false, nil

			} else {
				err := fmt.Errorf("a storage class %s already exists and does not belong to %s provisioner", sc.Name, LocalStorageClassProvisioner)
				return false, err
			}
		}
	}

	err := fmt.Errorf("a storage class %s does not exist", lsc.Name)
	return false, err

}

func hasLVGDiff(sc *v1.StorageClass, lsc *v1alpha1.LocalStorageClass) (bool, error) {
	currentLVGs, err := getLVGFromSCParams(sc)
	if err != nil {
		return false, err
	}

	if len(currentLVGs) != len(lsc.Spec.LVM.LVMVolumeGroups) {
		return true, nil
	}

	for i := range currentLVGs {
		if currentLVGs[i].Name != lsc.Spec.LVM.LVMVolumeGroups[i].Name {
			return true, nil
		}
		if lsc.Spec.LVM.Type == LVMThinType {
			if currentLVGs[i].Thin == nil && lsc.Spec.LVM.LVMVolumeGroups[i].Thin != nil {
				return true, nil
			}
			if currentLVGs[i].Thin == nil && lsc.Spec.LVM.LVMVolumeGroups[i].Thin == nil {
				err := fmt.Errorf("LocalStorageClass type=%q: unable to identify the Thin pool differences for the LocalStorageClass %q. The current LVMVolumeGroup %q does not have a Thin pool configured in either the StorageClass or the LocalStorageClass", lsc.Spec.LVM.Type, lsc.Name, currentLVGs[i].Name)
				return false, err
			}
			if currentLVGs[i].Thin.PoolName != lsc.Spec.LVM.LVMVolumeGroups[i].Thin.PoolName {
				return true, nil
			}
		}
	}

	return false, nil
}

func getLVGFromSCParams(sc *v1.StorageClass) ([]v1alpha1.LocalStorageClassLVG, error) {
	lvgsFromParams := sc.Parameters[LVMVolumeGroupsParamKey]
	var currentLVGs []v1alpha1.LocalStorageClassLVG

	err := yaml.Unmarshal([]byte(lvgsFromParams), &currentLVGs)
	if err != nil {
		return nil, err
	}

	return currentLVGs, nil
}

func shouldReconcileByCreateFunc(scList *v1.StorageClassList, lsc *v1alpha1.LocalStorageClass) bool {
	if lsc.DeletionTimestamp != nil {
		return false
	}

	for _, sc := range scList.Items {
		if sc.Name == lsc.Name {
			return false
		}
	}

	return true
}

func reconcileLSCCreateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	lsc *v1alpha1.LocalStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] starts the LocalStorageClass %s validation", lsc.Name))
	added, err := addFinalizerIfNotExistsForLSC(ctx, cl, lsc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to add a finalizer %s to the LocalStorageClass %s", LocalStorageClassFinalizerName, lsc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] finalizer %s was added to the LocalStorageClass %s: %t", LocalStorageClassFinalizerName, lsc.Name, added))

	valid, msg := validateLocalStorageClass(ctx, cl, scList, lsc)
	if !valid {
		err := fmt.Errorf("validation failed: %s", msg)
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] Unable to reconcile the LocalStorageClass, name: %s", lsc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, msg)
		if upError != nil {
			log.Error(upError, fmt.Sprintf("[reconcileLSCCreateFunc] unable to update the LocalStorageClass %s", lsc.Name))
		}

		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully validated the LocalStorageClass, name: %s", lsc.Name))

	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] starts storage class configuration for the LocalStorageClass, name: %s", lsc.Name))
	sc, err := configureStorageClass(lsc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to configure Storage Class for LocalStorageClass, name: %s", lsc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error(upError, fmt.Sprintf("[reconcileLSCCreateFunc] unable to update the LocalStorageClass %s", lsc.Name))
			return true, upError
		}
		return false, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully configurated storage class for the LocalStorageClass, name: %s", lsc.Name))

	created, err := createStorageClassIfNotExists(ctx, cl, scList, sc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to create a Storage Class, name: %s", sc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error(upError, fmt.Sprintf("[reconcileLSCCreateFunc] unable to update the LocalStorageClass %s", lsc.Name))
			return true, upError
		}
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] a storage class %s was created: %t", sc.Name, created))
	if created {
		log.Info(fmt.Sprintf("[reconcileLSCCreateFunc] successfully create storage class, name: %s", sc.Name))
	} else {
		log.Warning(fmt.Sprintf("[reconcileLSCCreateFunc] Storage class %s already exists. Adding event to requeue.", sc.Name))
		return true, nil
	}

	added, err = addFinalizerIfNotExistsForSC(ctx, cl, sc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to add a finalizer %s to the StorageClass %s", LocalStorageClassFinalizerName, sc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] finalizer %s was added to the StorageClass %s: %t", LocalStorageClassFinalizerName, sc.Name, added))

	err = updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to update the LocalStorageClass, name: %s", lsc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully updated the LocalStorageClass %s status", sc.Name))

	return false, nil
}

func createStorageClassIfNotExists(
	ctx context.Context,
	cl client.Client,
	scList *v1.StorageClassList,
	sc *v1.StorageClass,
) (bool, error) {
	for _, s := range scList.Items {
		if s.Name == sc.Name {
			return false, nil
		}
	}

	err := cl.Create(ctx, sc)
	if err != nil {
		return false, err
	}

	return true, err
}

func addFinalizerIfNotExistsForLSC(ctx context.Context, cl client.Client, lsc *v1alpha1.LocalStorageClass) (bool, error) {
	if !slices.Contains(lsc.Finalizers, LocalStorageClassFinalizerName) {
		lsc.Finalizers = append(lsc.Finalizers, LocalStorageClassFinalizerName)
	}

	err := cl.Update(ctx, lsc)
	if err != nil {
		return false, err
	}

	return true, nil
}

func addFinalizerIfNotExistsForSC(ctx context.Context, cl client.Client, sc *v1.StorageClass) (bool, error) {
	if !slices.Contains(sc.Finalizers, LocalStorageClassFinalizerName) {
		sc.Finalizers = append(sc.Finalizers, LocalStorageClassFinalizerName)
	}

	err := cl.Update(ctx, sc)
	if err != nil {
		return false, err
	}

	return true, nil
}

func configureStorageClass(lsc *v1alpha1.LocalStorageClass) (*v1.StorageClass, error) {
	reclaimPolicy := corev1.PersistentVolumeReclaimPolicy(lsc.Spec.ReclaimPolicy)
	volumeBindingMode := v1.VolumeBindingMode(lsc.Spec.VolumeBindingMode)
	AllowVolumeExpansion := AllowVolumeExpansionDefaultValue

	if lsc.Spec.LVM == nil {
		//TODO: add support for other LSC types
		return nil, fmt.Errorf("unable to identify the LocalStorageClass type")
	}

	lvgsParam, err := yaml.Marshal(lsc.Spec.LVM.LVMVolumeGroups)
	if err != nil {
		return nil, err
	}

	params := map[string]string{
		TypeParamKey:                 LocalStorageClassLvmType,
		LVMTypeParamKey:              lsc.Spec.LVM.Type,
		LVMVolumeBindingModeParamKey: lsc.Spec.VolumeBindingMode,
		LVMVolumeGroupsParamKey:      string(lvgsParam),
	}

	sc := &v1.StorageClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       StorageClassKind,
			APIVersion: StorageClassAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       lsc.Name,
			Namespace:  lsc.Namespace,
			Finalizers: []string{LocalStorageClassFinalizerName},
		},
		Provisioner:          LocalStorageClassProvisioner,
		Parameters:           params,
		ReclaimPolicy:        &reclaimPolicy,
		AllowVolumeExpansion: &AllowVolumeExpansion,
		VolumeBindingMode:    &volumeBindingMode,
	}

	return sc, nil
}

func updateLocalStorageClassPhase(
	ctx context.Context,
	cl client.Client,
	lsc *v1alpha1.LocalStorageClass,
	phase,
	reason string,
) error {
	if lsc.Status == nil {
		lsc.Status = new(v1alpha1.LocalStorageClassStatus)
	}
	lsc.Status.Phase = phase
	lsc.Status.Reason = reason

	if !slices.Contains(lsc.Finalizers, LocalStorageClassFinalizerName) {
		lsc.Finalizers = append(lsc.Finalizers, LocalStorageClassFinalizerName)
	}

	// TODO: add retry logic
	err := cl.Update(ctx, lsc)
	if err != nil {
		return err
	}

	return nil
}

func validateLocalStorageClass(
	ctx context.Context,
	cl client.Client,
	scList *v1.StorageClassList,
	lsc *v1alpha1.LocalStorageClass,
) (bool, string) {
	var (
		failedMsgBuilder strings.Builder
		valid            = true
	)

	unmanagedScName := findUnmanagedDuplicatedSC(scList, lsc)
	if unmanagedScName != "" {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("There already is a storage class with the same name: %s but it is not managed by the LocalStorageClass controller\n", unmanagedScName))
	}

	lvgList := &v1alpha1.LvmVolumeGroupList{}
	err := cl.List(ctx, lvgList)
	if err != nil {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Unable to validate selected LVMVolumeGroups, err: %s\n", err.Error()))
		return valid, failedMsgBuilder.String()
	}

	if lsc.Spec.LVM != nil {
		LVGsFromTheSameNode := findLVMVolumeGroupsOnTheSameNode(lvgList, lsc)
		if len(LVGsFromTheSameNode) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use the same node (|node: LVG names): %s\n", strings.Join(LVGsFromTheSameNode, "")))
		}

		nonexistentLVGs := findNonexistentLVGs(lvgList, lsc)
		if len(nonexistentLVGs) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some of selected LVMVolumeGroups are nonexistent, LVG names: %s\n", strings.Join(nonexistentLVGs, ",")))
		}

		if lsc.Spec.LVM.Type == LVMThinType {
			LVGSWithNonexistentTps := findNonexistentThinPools(lvgList, lsc)
			if len(LVGSWithNonexistentTps) != 0 {
				valid = false
				failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use nonexistent thin pools, LVG names: %s\n", strings.Join(LVGSWithNonexistentTps, ",")))
			}
		} else {
			LVGsWithTps := findAnyThinPool(lsc)
			if len(LVGsWithTps) != 0 {
				valid = false
				failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use thin pools though device type is Thick, LVG names: %s\n", strings.Join(LVGsWithTps, ",")))
			}
		}
	} else {
		// TODO: add support for other types
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Unable to identify a type of LocalStorageClass %s", lsc.Name))
	}

	return valid, failedMsgBuilder.String()
}

func findUnmanagedDuplicatedSC(scList *v1.StorageClassList, lsc *v1alpha1.LocalStorageClass) string {
	for _, sc := range scList.Items {
		if sc.Name == lsc.Name && sc.Provisioner != LocalStorageClassProvisioner {
			return sc.Name
		}
	}

	return ""
}

func findAnyThinPool(lsc *v1alpha1.LocalStorageClass) []string {
	badLvgs := make([]string, 0, len(lsc.Spec.LVM.LVMVolumeGroups))
	for _, lvs := range lsc.Spec.LVM.LVMVolumeGroups {
		if lvs.Thin != nil {
			badLvgs = append(badLvgs, lvs.Name)
		}
	}

	return badLvgs
}

func findNonexistentThinPools(lvgList *v1alpha1.LvmVolumeGroupList, lsc *v1alpha1.LocalStorageClass) []string {
	lvgs := make(map[string]v1alpha1.LvmVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = lvg
	}

	badLvgs := make([]string, 0, len(lsc.Spec.LVM.LVMVolumeGroups))
	for _, lscLvg := range lsc.Spec.LVM.LVMVolumeGroups {
		if lscLvg.Thin == nil {
			badLvgs = append(badLvgs, lscLvg.Name)
			continue
		}

		lvgRes := lvgs[lscLvg.Name]
		exist := false

		for _, tp := range lvgRes.Status.ThinPools {
			if tp.Name == lscLvg.Thin.PoolName {
				exist = true
				break
			}
		}

		if !exist {
			badLvgs = append(badLvgs, lscLvg.Name)
		}
	}

	return badLvgs
}

func findNonexistentLVGs(lvgList *v1alpha1.LvmVolumeGroupList, lsc *v1alpha1.LocalStorageClass) []string {
	lvgs := make(map[string]struct{}, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = struct{}{}
	}

	nonexistent := make([]string, 0, len(lsc.Spec.LVM.LVMVolumeGroups))
	for _, lvg := range lsc.Spec.LVM.LVMVolumeGroups {
		if _, exist := lvgs[lvg.Name]; !exist {
			nonexistent = append(nonexistent, lvg.Name)
		}
	}

	return nonexistent
}

func findLVMVolumeGroupsOnTheSameNode(lvgList *v1alpha1.LvmVolumeGroupList, lsc *v1alpha1.LocalStorageClass) []string {
	nodesWithLVGs := make(map[string][]string, len(lsc.Spec.LVM.LVMVolumeGroups))
	usedLVGs := make(map[string]struct{}, len(lsc.Spec.LVM.LVMVolumeGroups))
	for _, lvg := range lsc.Spec.LVM.LVMVolumeGroups {
		usedLVGs[lvg.Name] = struct{}{}
	}

	badLVGs := make([]string, 0, len(lsc.Spec.LVM.LVMVolumeGroups))
	for _, lvg := range lvgList.Items {
		if _, used := usedLVGs[lvg.Name]; used {
			for _, node := range lvg.Status.Nodes {
				nodesWithLVGs[node.Name] = append(nodesWithLVGs[node.Name], lvg.Name)
			}
		}
	}

	for nodeName, lvgs := range nodesWithLVGs {
		if len(lvgs) > 1 {
			var msgBuilder strings.Builder
			msgBuilder.WriteString(fmt.Sprintf("|%s: ", nodeName))
			for _, lvgName := range lvgs {
				msgBuilder.WriteString(fmt.Sprintf("%s,", lvgName))
			}

			badLVGs = append(badLVGs, msgBuilder.String())
		}
	}

	return badLVGs
}

func recreateStorageClass(ctx context.Context, cl client.Client, oldSC, newSC *v1.StorageClass) error {
	// It is necessary to pass the original StorageClass to the delete operation because
	// the deletion will not succeed if the fields in the StorageClass provided to delete
	// differ from those currently in the cluster.
	err := deleteStorageClass(ctx, cl, oldSC)
	if err != nil {
		err = fmt.Errorf("[recreateStorageClass] unable to delete a storage class %s: %s", oldSC.Name, err.Error())
		return err
	}

	err = cl.Create(ctx, newSC)
	if err != nil {
		err = fmt.Errorf("[recreateStorageClass] unable to create a storage class %s: %s", newSC.Name, err.Error())
		return err
	}

	return nil
}

func deleteStorageClass(ctx context.Context, cl client.Client, sc *v1.StorageClass) error {
	if sc.Provisioner != LocalStorageClassProvisioner {
		return fmt.Errorf("a storage class %s does not belong to %s provisioner", sc.Name, LocalStorageClassProvisioner)
	}

	_, err := removeFinalizerIfExists(ctx, cl, sc, LocalStorageClassFinalizerName)
	if err != nil {
		return err
	}

	err = cl.Delete(ctx, sc)
	if err != nil {
		return err
	}

	return nil
}

func removeFinalizerIfExists(ctx context.Context, cl client.Client, obj metav1.Object, finalizerName string) (bool, error) {
	removed := false
	finalizers := obj.GetFinalizers()
	for i, f := range finalizers {
		if f == finalizerName || f == LocalStorageClassFinalizerNameOld {
			finalizers = append(finalizers[:i], finalizers[i+1:]...)
			removed = true
			break
		}
	}

	if removed {
		obj.SetFinalizers(finalizers)
		err := cl.Update(ctx, obj.(client.Object))
		if err != nil {
			return false, err
		}
	}

	return removed, nil
}

func updateStorageClass(lsc *v1alpha1.LocalStorageClass, oldSC *v1.StorageClass) (*v1.StorageClass, error) {
	newSC, err := configureStorageClass(lsc)
	if err != nil {
		return nil, err
	}

	if oldSC.Annotations != nil {
		newSC.Annotations = oldSC.Annotations
	}

	return newSC, nil
}
