/*
Copyright 2025 Flant JSC

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
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/internal"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

// lscUsesLabelSelector reports whether any of the LocalStorageClass
// lvmVolumeGroups entries selects its LVMVolumeGroups by a label selector.
func lscUsesLabelSelector(lsc *slv.LocalStorageClass) bool {
	if lsc.Spec.LVM == nil {
		return false
	}
	for _, entry := range lsc.Spec.LVM.LVMVolumeGroups {
		if entry.LabelSelector != nil {
			return true
		}
	}
	return false
}

// resolveEffectiveLVGs expands the LocalStorageClass lvmVolumeGroups list into
// the concrete set of named LVMVolumeGroups it targets. A name-based entry maps
// to itself; a labelSelector-based entry expands to every LVMVolumeGroup whose
// labels match, each inheriting that entry's thin pool. The result is
// deduplicated by name (a name selected more than once is an error) and sorted
// by name so the resulting StorageClass parameter is deterministic across
// reconciles.
func resolveEffectiveLVGs(lsc *slv.LocalStorageClass, lvgList *snc.LVMVolumeGroupList) ([]slv.LocalStorageClassLVG, error) {
	if lsc.Spec.LVM == nil {
		return nil, nil
	}

	resolved := make([]slv.LocalStorageClassLVG, 0, len(lsc.Spec.LVM.LVMVolumeGroups))
	seen := make(map[string]*slv.LocalStorageClassLVMThinPoolSpec, len(lsc.Spec.LVM.LVMVolumeGroups))

	// An LVMVolumeGroup matched by several entries (e.g. two overlapping label
	// selectors) is collapsed to a single resolved entry. It is only an error
	// when those entries disagree on the thin pool, which would be ambiguous.
	appendEntry := func(name string, thin *slv.LocalStorageClassLVMThinPoolSpec) error {
		if prev, dup := seen[name]; dup {
			if !sameThinPool(prev, thin) {
				return fmt.Errorf("LVMVolumeGroup %q is selected by more than one lvmVolumeGroups entry with different thin pools (%q vs %q)", name, thinPoolName(prev), thinPoolName(thin))
			}
			return nil
		}
		seen[name] = thin
		resolved = append(resolved, slv.LocalStorageClassLVG{Name: name, Thin: thin})
		return nil
	}

	for _, entry := range lsc.Spec.LVM.LVMVolumeGroups {
		if entry.LabelSelector == nil {
			if err := appendEntry(entry.Name, entry.Thin); err != nil {
				return nil, err
			}
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(entry.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid labelSelector: %w", err)
		}
		for _, lvg := range lvgList.Items {
			if !selector.Matches(labels.Set(lvg.Labels)) {
				continue
			}
			if err := appendEntry(lvg.Name, entry.Thin); err != nil {
				return nil, err
			}
		}
	}

	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].Name < resolved[j].Name
	})

	return resolved, nil
}

// sameThinPool reports whether two thin pool specs are equivalent (both absent,
// or both naming the same pool).
func sameThinPool(a, b *slv.LocalStorageClassLVMThinPoolSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.PoolName == b.PoolName
}

func thinPoolName(t *slv.LocalStorageClassLVMThinPoolSpec) string {
	if t == nil {
		return ""
	}
	return t.PoolName
}

func reconcileLSCDeleteFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	lsc *slv.LocalStorageClass,
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
	lsc *slv.LocalStorageClass,
	lvgList *snc.LVMVolumeGroupList,
	effectiveLVGs []slv.LocalStorageClassLVG,
	ignoredLabelPrefixes []string,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] starts the LocalStorageClass %s validation", lsc.Name))
	valid, msg := validateLocalStorageClass(scList, lsc, lvgList, effectiveLVGs)
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
	hasDiff, err := hasSCDiff(oldSC, lsc, effectiveLVGs, ignoredLabelPrefixes)
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
		newSC, err := updateStorageClass(lsc, oldSC, effectiveLVGs, ignoredLabelPrefixes)
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

func identifyReconcileFunc(scList *v1.StorageClassList, lsc *slv.LocalStorageClass, effectiveLVGs []slv.LocalStorageClassLVG, ignoredLabelPrefixes []string) (reconcileType, error) {
	if shouldReconcileByDeleteFunc(lsc) {
		return DeleteReconcile, nil
	}

	if shouldReconcileByCreateFunc(scList, lsc) {
		return CreateReconcile, nil
	}

	should, err := shouldReconcileByUpdateFunc(scList, lsc, effectiveLVGs, ignoredLabelPrefixes)
	if err != nil {
		return "none", err
	}
	if should {
		return UpdateReconcile, nil
	}

	return "none", nil
}

func shouldReconcileByDeleteFunc(lsc *slv.LocalStorageClass) bool {
	return lsc.DeletionTimestamp != nil
}

func shouldReconcileByUpdateFunc(scList *v1.StorageClassList, lsc *slv.LocalStorageClass, effectiveLVGs []slv.LocalStorageClassLVG, ignoredLabelPrefixes []string) (bool, error) {
	if lsc.DeletionTimestamp != nil {
		return false, nil
	}

	for _, sc := range scList.Items {
		if sc.Name == lsc.Name {
			if sc.Provisioner == LocalStorageClassProvisioner {
				diff, err := hasSCDiff(&sc, lsc, effectiveLVGs, ignoredLabelPrefixes)
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
			}

			err := fmt.Errorf("a storage class %s already exists and does not belong to %s provisioner", sc.Name, LocalStorageClassProvisioner)
			return false, err
		}
	}

	err := fmt.Errorf("a storage class %s does not exist", lsc.Name)
	return false, err
}

func hasSCDiff(sc *v1.StorageClass, lsc *slv.LocalStorageClass, effectiveLVGs []slv.LocalStorageClassLVG, ignoredLabelPrefixes []string) (bool, error) {
	currentLVGs, err := getLVGFromSCParams(sc)
	if err != nil {
		return false, err
	}

	if lsc.Spec.LVM.VolumeCleanup != sc.Parameters[LVMVolumeCleanupParamKey] {
		return true, nil
	}

	if !labelsMatchLSC(sc.Labels, lsc.Labels, ignoredLabelPrefixes) {
		return true, nil
	}

	if len(currentLVGs) != len(effectiveLVGs) {
		return true, nil
	}

	// Compare as sets keyed by LVMVolumeGroup name (not positionally): the
	// effective list is sorted, but a StorageClass created by an older module
	// version may store its LVMVolumeGroups in a different order, and a spurious
	// order-only diff would trigger an unnecessary delete+create of the SC.
	effectiveByName := make(map[string]*slv.LocalStorageClassLVMThinPoolSpec, len(effectiveLVGs))
	for i := range effectiveLVGs {
		effectiveByName[effectiveLVGs[i].Name] = effectiveLVGs[i].Thin
	}

	for i := range currentLVGs {
		effThin, ok := effectiveByName[currentLVGs[i].Name]
		if !ok {
			return true, nil
		}
		if lsc.Spec.LVM.Type == LVMThinType {
			curThin := currentLVGs[i].Thin
			switch {
			case curThin == nil && effThin == nil:
				return false, fmt.Errorf("LocalStorageClass type=%q: unable to identify the Thin pool differences for the LocalStorageClass %q. The current LVMVolumeGroup %q does not have a Thin pool configured in either the StorageClass or the LocalStorageClass", lsc.Spec.LVM.Type, lsc.Name, currentLVGs[i].Name)
			case curThin == nil || effThin == nil:
				return true, nil
			case curThin.PoolName != effThin.PoolName:
				return true, nil
			}
		}
	}

	return false, nil
}

// labelsMatchLSC reports whether the labels of the existing StorageClass match
// the labels propagated from the LocalStorageClass (CR labels + managed-by),
// taking the ignoredLabelPrefixes filter into account. Labels whose keys start
// with any of the ignoredLabelPrefixes are dropped before the comparison.
func labelsMatchLSC(scLabels, lscLabels map[string]string, ignoredLabelPrefixes []string) bool {
	filtered := filterLabelsForStorageClass(lscLabels, ignoredLabelPrefixes)
	expected := make(map[string]string, len(filtered)+1)
	for k, v := range filtered {
		expected[k] = v
	}
	expected[internal.SLVStorageManagedLabelKey] = internal.SLVStorageClassCtrlName

	return reflect.DeepEqual(scLabels, expected)
}

// filterLabelsForStorageClass returns a copy of lscLabels with all keys whose
// prefix matches any entry in ignoredLabelPrefixes removed. Empty entries in
// ignoredLabelPrefixes are skipped to avoid silently dropping every label.
func filterLabelsForStorageClass(lscLabels map[string]string, ignoredLabelPrefixes []string) map[string]string {
	if len(lscLabels) == 0 {
		return nil
	}
	out := make(map[string]string, len(lscLabels))
	for k, v := range lscLabels {
		if isIgnoredLabelKey(k, ignoredLabelPrefixes) {
			continue
		}
		out[k] = v
	}
	return out
}

func isIgnoredLabelKey(key string, ignoredLabelPrefixes []string) bool {
	for _, prefix := range ignoredLabelPrefixes {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func getLVGFromSCParams(sc *v1.StorageClass) ([]slv.LocalStorageClassLVG, error) {
	lvgsFromParams := sc.Parameters[LVMVolumeGroupsParamKey]
	var currentLVGs []slv.LocalStorageClassLVG

	err := yaml.Unmarshal([]byte(lvgsFromParams), &currentLVGs)
	if err != nil {
		return nil, err
	}

	return currentLVGs, nil
}

func shouldReconcileByCreateFunc(scList *v1.StorageClassList, lsc *slv.LocalStorageClass) bool {
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
	lsc *slv.LocalStorageClass,
	lvgList *snc.LVMVolumeGroupList,
	effectiveLVGs []slv.LocalStorageClassLVG,
	ignoredLabelPrefixes []string,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] starts the LocalStorageClass %s validation", lsc.Name))
	added, err := addFinalizerIfNotExistsForLSC(ctx, cl, lsc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to add a finalizer %s to the LocalStorageClass %s", LocalStorageClassFinalizerName, lsc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] finalizer %s was added to the LocalStorageClass %s: %t", LocalStorageClassFinalizerName, lsc.Name, added))

	valid, msg := validateLocalStorageClass(scList, lsc, lvgList, effectiveLVGs)
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
	sc, err := configureStorageClass(lsc, effectiveLVGs, ignoredLabelPrefixes)
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

func addFinalizerIfNotExistsForLSC(ctx context.Context, cl client.Client, lsc *slv.LocalStorageClass) (bool, error) {
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

func configureStorageClass(lsc *slv.LocalStorageClass, effectiveLVGs []slv.LocalStorageClassLVG, ignoredLabelPrefixes []string) (*v1.StorageClass, error) {
	reclaimPolicy := corev1.PersistentVolumeReclaimPolicy(lsc.Spec.ReclaimPolicy)
	volumeBindingMode := v1.VolumeBindingMode(lsc.Spec.VolumeBindingMode)
	AllowVolumeExpansion := AllowVolumeExpansionDefaultValue

	if lsc.Spec.LVM == nil {
		//TODO: add support for other LSC types
		return nil, fmt.Errorf("unable to identify the LocalStorageClass type")
	}

	lvgsParam, err := yaml.Marshal(effectiveLVGs)
	if err != nil {
		return nil, err
	}

	fsType := lsc.Spec.FSType
	if fsType == "" {
		fsType = DefaultFSType
	}

	params := map[string]string{
		TypeParamKey:                 LocalStorageClassLvmType,
		LVMTypeParamKey:              lsc.Spec.LVM.Type,
		LVMVolumeBindingModeParamKey: lsc.Spec.VolumeBindingMode,
		LVMVolumeGroupsParamKey:      string(lvgsParam),
		FSTypeParamKey:               fsType,
	}

	if lsc.Spec.LVM.Thick != nil && lsc.Spec.LVM.Thick.Contiguous != nil {
		if *lsc.Spec.LVM.Thick.Contiguous {
			params[LVMThickContiguousParamKey] = "true"
		}
	}

	if lsc.Spec.LVM.VolumeCleanup != "" {
		params[LVMVolumeCleanupParamKey] = lsc.Spec.LVM.VolumeCleanup
	}

	sc := &v1.StorageClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       StorageClassKind,
			APIVersion: StorageClassAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      lsc.Name,
			Namespace: lsc.Namespace,
			Annotations: map[string]string{
				internal.SLVStorageClassVolumeSnapshotClassAnnotationKey: internal.SLVStorageClassVolumeSnapshotClassAnnotationValue,
			},
			Finalizers: []string{LocalStorageClassFinalizerName},
		},
		Provisioner:          LocalStorageClassProvisioner,
		Parameters:           params,
		ReclaimPolicy:        &reclaimPolicy,
		AllowVolumeExpansion: &AllowVolumeExpansion,
		VolumeBindingMode:    &volumeBindingMode,
	}

	filteredLabels := filterLabelsForStorageClass(lsc.Labels, ignoredLabelPrefixes)
	if len(filteredLabels) > 0 {
		sc.Labels = filteredLabels
		sc.Labels[internal.SLVStorageManagedLabelKey] = internal.SLVStorageClassCtrlName
	} else {
		sc.Labels = map[string]string{
			internal.SLVStorageManagedLabelKey: internal.SLVStorageClassCtrlName,
		}
	}

	return sc, nil
}

func updateLocalStorageClassPhase(
	ctx context.Context,
	cl client.Client,
	lsc *slv.LocalStorageClass,
	phase,
	reason string,
) error {
	if lsc.Status == nil {
		lsc.Status = new(slv.LocalStorageClassStatus)
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
	scList *v1.StorageClassList,
	lsc *slv.LocalStorageClass,
	lvgList *snc.LVMVolumeGroupList,
	effectiveLVGs []slv.LocalStorageClassLVG,
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

	if lsc.Spec.LVM != nil {
		if len(lsc.Spec.LVM.LVMVolumeGroups) == 0 {
			valid = false
			failedMsgBuilder.WriteString("Field spec.lvm.lvmVolumeGroups must not be empty\n")
		}

		nameEntries, selectorEntries := 0, 0
		for i, entry := range lsc.Spec.LVM.LVMVolumeGroups {
			hasName := entry.Name != ""
			hasSelector := entry.LabelSelector != nil

			if hasName {
				nameEntries++
			}
			if hasSelector {
				selectorEntries++
			}

			if hasName == hasSelector {
				valid = false
				failedMsgBuilder.WriteString(fmt.Sprintf("Each spec.lvm.lvmVolumeGroups entry must set exactly one of name or labelSelector (entry #%d)\n", i))
			} else if hasSelector {
				if _, err := metav1.LabelSelectorAsSelector(entry.LabelSelector); err != nil {
					valid = false
					failedMsgBuilder.WriteString(fmt.Sprintf("Invalid labelSelector in spec.lvm.lvmVolumeGroups entry #%d: %s\n", i, err.Error()))
				} else if countLVGsMatchingSelector(lvgList, entry.LabelSelector) == 0 {
					valid = false
					failedMsgBuilder.WriteString(fmt.Sprintf("labelSelector in spec.lvm.lvmVolumeGroups entry #%d matched no LVMVolumeGroups\n", i))
				}
			}

			if lsc.Spec.LVM.Type == LVMThinType {
				if entry.Thin == nil || entry.Thin.PoolName == "" {
					valid = false
					failedMsgBuilder.WriteString(fmt.Sprintf("Field thin.poolName is required for spec.lvm.lvmVolumeGroups entry #%d when type is Thin\n", i))
				}
			} else if entry.Thin != nil {
				valid = false
				failedMsgBuilder.WriteString(fmt.Sprintf("Field thin must not be specified for spec.lvm.lvmVolumeGroups entry #%d when type is Thick\n", i))
			}
		}

		if nameEntries > 0 && selectorEntries > 0 {
			valid = false
			failedMsgBuilder.WriteString("spec.lvm.lvmVolumeGroups must use either name entries or labelSelector entries, not a mix\n")
		}

		LVGsFromTheSameNode := findLVMVolumeGroupsOnTheSameNode(lvgList, effectiveLVGs)
		if len(LVGsFromTheSameNode) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use the same node (|node: LVG names): %s\n", strings.Join(LVGsFromTheSameNode, "")))
		}

		nonexistentLVGs := findNonexistentLVGs(lvgList, effectiveLVGs)
		if len(nonexistentLVGs) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some of selected LVMVolumeGroups are nonexistent, LVG names: %s\n", strings.Join(nonexistentLVGs, ",")))
		}

		if lsc.Spec.LVM.Type == LVMThinType {
			LVGSWithNonexistentTps := findNonexistentThinPools(lvgList, effectiveLVGs)
			if len(LVGSWithNonexistentTps) != 0 {
				valid = false
				failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use nonexistent thin pools, LVG names: %s\n", strings.Join(LVGSWithNonexistentTps, ",")))
			}
		} else {
			LVGsWithTps := findAnyThinPool(effectiveLVGs)
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

func findUnmanagedDuplicatedSC(scList *v1.StorageClassList, lsc *slv.LocalStorageClass) string {
	for _, sc := range scList.Items {
		if sc.Name == lsc.Name && sc.Provisioner != LocalStorageClassProvisioner {
			return sc.Name
		}
	}

	return ""
}

func findAnyThinPool(effectiveLVGs []slv.LocalStorageClassLVG) []string {
	badLvgs := make([]string, 0, len(effectiveLVGs))
	for _, lvs := range effectiveLVGs {
		if lvs.Thin != nil {
			badLvgs = append(badLvgs, lvs.Name)
		}
	}

	return badLvgs
}

func findNonexistentThinPools(lvgList *snc.LVMVolumeGroupList, effectiveLVGs []slv.LocalStorageClassLVG) []string {
	lvgs := make(map[string]snc.LVMVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = lvg
	}

	badLvgs := make([]string, 0, len(effectiveLVGs))
	for _, lscLvg := range effectiveLVGs {
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

func countLVGsMatchingSelector(lvgList *snc.LVMVolumeGroupList, labelSelector *metav1.LabelSelector) int {
	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return 0
	}

	count := 0
	for _, lvg := range lvgList.Items {
		if selector.Matches(labels.Set(lvg.Labels)) {
			count++
		}
	}

	return count
}

func findNonexistentLVGs(lvgList *snc.LVMVolumeGroupList, effectiveLVGs []slv.LocalStorageClassLVG) []string {
	lvgs := make(map[string]struct{}, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = struct{}{}
	}

	nonexistent := make([]string, 0, len(effectiveLVGs))
	for _, lvg := range effectiveLVGs {
		if _, exist := lvgs[lvg.Name]; !exist {
			nonexistent = append(nonexistent, lvg.Name)
		}
	}

	return nonexistent
}

func findLVMVolumeGroupsOnTheSameNode(lvgList *snc.LVMVolumeGroupList, effectiveLVGs []slv.LocalStorageClassLVG) []string {
	nodesWithLVGs := make(map[string][]string, len(effectiveLVGs))
	usedLVGs := make(map[string]struct{}, len(effectiveLVGs))
	for _, lvg := range effectiveLVGs {
		usedLVGs[lvg.Name] = struct{}{}
	}

	badLVGs := make([]string, 0, len(effectiveLVGs))
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

func updateStorageClass(lsc *slv.LocalStorageClass, oldSC *v1.StorageClass, effectiveLVGs []slv.LocalStorageClassLVG, ignoredLabelPrefixes []string) (*v1.StorageClass, error) {
	newSC, err := configureStorageClass(lsc, effectiveLVGs, ignoredLabelPrefixes)
	if err != nil {
		return nil, err
	}

	if oldSC.Annotations != nil {
		if newSC.Annotations == nil {
			newSC.Annotations = make(map[string]string)
		}
		for k, v := range oldSC.Annotations {
			if _, exists := newSC.Annotations[k]; !exists {
				newSC.Annotations[k] = v
			}
		}
	}

	return newSC, nil
}
