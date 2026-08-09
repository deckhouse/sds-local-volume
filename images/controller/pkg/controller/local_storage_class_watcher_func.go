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
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/deckhouse/sds-common-lib/conditions"
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
// labels match, each inheriting that entry's thin pool. The result is sorted by
// name so the resulting StorageClass parameter is deterministic across
// reconciles.
//
// Deduplication rules:
//   - Duplicate name-based entries are always an error (same as the admission
//     webhook uniqueness check), even when they agree on the thin pool.
//   - An LVMVolumeGroup matched by several overlapping labelSelector entries is
//     collapsed to a single resolved entry when those entries agree on the thin
//     pool; disagreeing thin pools are an error (ambiguous).
func resolveEffectiveLVGs(lsc *slv.LocalStorageClass, lvgList *snc.LVMVolumeGroupList) ([]slv.LocalStorageClassLVG, error) {
	if lsc.Spec.LVM == nil {
		return nil, nil
	}

	resolved := make([]slv.LocalStorageClassLVG, 0, len(lsc.Spec.LVM.LVMVolumeGroups))
	seen := make(map[string]*slv.LocalStorageClassLVMThinPoolSpec, len(lsc.Spec.LVM.LVMVolumeGroups))

	// allowCollapse=true is used for selector expansion: overlapping selectors
	// that agree on the thin pool collapse to one entry. allowCollapse=false is
	// used for name-based entries: any repeated name is a hard error.
	appendEntry := func(name string, thin *slv.LocalStorageClassLVMThinPoolSpec, allowCollapse bool) error {
		if prev, dup := seen[name]; dup {
			if !allowCollapse {
				return fmt.Errorf("LVMVolumeGroup %q is listed more than once in lvmVolumeGroups", name)
			}
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
			if err := appendEntry(entry.Name, entry.Thin, false); err != nil {
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
			if err := appendEntry(lvg.Name, entry.Thin, true); err != nil {
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
	log = log.Named("delete").With("localStorageClass", lsc.Name)

	log.Debug("looking for the managed StorageClass")
	var sc *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == lsc.Name {
			sc = &s
			break
		}
	}
	if sc == nil {
		log.Info("no managed StorageClass found")
	}

	if sc != nil {
		log.Info("found the managed StorageClass")
		log.Debug("identifying the StorageClass provisioner", "storageClass", sc.Name)

		if sc.Provisioner != LocalStorageClassProvisioner {
			log.Info("the StorageClass belongs to another provisioner, not deleting it", "storageClass", sc.Name, "provisioner", LocalStorageClassProvisioner)
		} else {
			log.Info("deleting the StorageClass", "storageClass", sc.Name, "provisioner", LocalStorageClassProvisioner)

			err := deleteStorageClass(ctx, cl, sc)
			if err != nil {
				log.Error("unable to delete the StorageClass", logger.Err(err), slog.String("storageClass", sc.Name))
				upErr := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to delete a storage class, err: %s", err.Error()))
				if upErr != nil {
					log.Error("unable to update the LocalStorageClass status", logger.Err(upErr))
				}
				return true, err
			}
			log.Info("deleted the StorageClass", "storageClass", sc.Name)
		}
	}

	log.Debug("removing the finalizer", "finalizer", LocalStorageClassFinalizerName)
	removed, err := removeControllerFinalizers(ctx, cl, lsc)
	if err != nil {
		log.Error("unable to remove the finalizer", logger.Err(err), slog.String("finalizer", LocalStorageClassFinalizerName))
		upErr := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to remove a finalizer, err: %s", err.Error()))
		if upErr != nil {
			log.Error("unable to update the LocalStorageClass status", logger.Err(upErr))
		}
		return true, err
	}
	log.Debug("finalizer removal result", "finalizer", LocalStorageClassFinalizerName, "removed", removed)

	// Published last, once nothing is left that could conflict with it.
	//
	// The obvious place for this is the top of the function — say "deleting"
	// before anything can go wrong — and that is where it does not work. The
	// status write bumps resourceVersion on the server while the caller's copy
	// and the informer cache still hold the old one, so the finalizer removal
	// that followed would land on a stale revision and 409. Retrying does not
	// help: the re-read goes through the same cache, which needs the watch
	// event to arrive before it can return anything newer. The deletion would
	// stall and the resource would be marked Failed for a teardown that was
	// going fine.
	//
	// Publishing here loses nothing. The two ways a teardown fails are covered:
	// a step that errors takes an error branch above and publishes Ready=False
	// with reason ReconcileFailed, and a resource held by somebody else's
	// finalizer survives our removal and keeps the Deleting condition set here.
	//
	// Not fatal on its own — failing to say "deleting" must not stop the
	// deletion — and a NotFound simply means ours was the last finalizer and the
	// object finished going away, which is the normal case and not worth a log
	// line.
	if err := setLocalStorageClassDeleting(ctx, cl, lsc); err != nil && !apierrors.IsNotFound(err) {
		log.Error("unable to publish the Deleting condition", logger.Err(err))
	}

	log.Debug("done")
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
	log = log.Named("update").With("localStorageClass", lsc.Name)

	log.Debug("validating the LocalStorageClass")
	valid, msg := validateLocalStorageClass(scList, lsc, lvgList, effectiveLVGs)
	if !valid {
		err := fmt.Errorf("validation failed: %s", msg)
		log.Error("the LocalStorageClass is invalid", logger.Err(err))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, msg)
		if upError != nil {
			log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
		}

		return true, err
	}
	log.Debug("the LocalStorageClass is valid")

	var oldSC *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == lsc.Name {
			oldSC = &s
			break
		}
	}
	if oldSC == nil {
		err := fmt.Errorf("a storage class %s does not exist", lsc.Name)
		log.Error("unable to find the managed StorageClass", logger.Err(err))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
		}
		return true, err
	}

	log.Debug("found the managed StorageClass")

	log.Trace("current StorageClass parameters", "storageClass", oldSC.Name, "parameters", fmt.Sprintf("%+v", oldSC.Parameters))
	log.Trace("LocalStorageClass LVM spec", "lvm", fmt.Sprintf("%+v", lsc.Spec.LVM))
	hasDiff, err := hasSCDiff(oldSC, lsc, effectiveLVGs, ignoredLabelPrefixes)
	if err != nil {
		log.Error("unable to determine the LVMVolumeGroup difference", logger.Err(err))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
		}
		return true, err
	}

	if hasDiff {
		log.Info("the StorageClass LVMVolumeGroups no longer match, recreating the StorageClass")
		newSC, err := updateStorageClass(lsc, oldSC, effectiveLVGs, ignoredLabelPrefixes)
		if err != nil {
			log.Error("unable to build the StorageClass", logger.Err(err))
			upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
			if upError != nil {
				log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
				return true, upError
			}
			return false, err
		}

		err = recreateStorageClass(ctx, cl, oldSC, newSC)
		if err != nil {
			log.Error("unable to recreate the StorageClass", logger.Err(err), slog.String("storageClass", newSC.Name))
			upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
			if upError != nil {
				log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
			}
			return true, err
		}

		log.Info("recreated the StorageClass", "storageClass", newSC.Name)
	}

	err = updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	if err != nil {
		log.Error("unable to update the LocalStorageClass status", logger.Err(err))
		return true, err
	}
	log.Debug("updated the LocalStorageClass status")

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

				// Retry a LocalStorageClass whose last verdict is either not a
				// success or not about the generation currently in the spec,
				// even when the StorageClass itself needs no change.
				//
				// The staleness half is not symmetry for its own sake. Not every
				// spec edit reaches hasSCDiff: it compares the LVMVolumeGroups as
				// a set, so reordering lvmVolumeGroups — or rewriting a
				// matchLabels selector as the equivalent matchExpressions — bumps
				// metadata.generation and produces no difference. Without this
				// check that generation is never observed, identifyReconcileFunc
				// answers "none", and status.observedGeneration trails
				// metadata.generation for the lifetime of the resource, which is
				// exactly the state the field is documented to rule out. The pass
				// it costs is cheap: conditions.UpdateStatus skips the write when
				// the only thing that moved is a resync.
				//
				// Checked against the Ready condition rather than the phase, so an
				// object the controller has never written a status for — and one
				// carried over from a release that predates the conditions — is
				// picked up as well. IsStale covers a missing condition; the nil
				// check is for the pointer above it, which is absent until the
				// first status write.
				if lsc.Status == nil ||
					conditions.IsStale(lsc.Status.Conditions, slv.ConditionTypeReady, lsc.Generation) ||
					!conditions.IsTrue(lsc.Status.Conditions, slv.ConditionTypeReady) {
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
	log = log.Named("create").With("localStorageClass", lsc.Name)

	log.Debug("validating the LocalStorageClass")
	if err := ensureFinalizer(ctx, cl, lsc); err != nil {
		log.Error("unable to add the finalizer to the LocalStorageClass", logger.Err(err), slog.String("finalizer", LocalStorageClassFinalizerName))
		return true, err
	}
	log.Debug("ensured the finalizer", "finalizer", LocalStorageClassFinalizerName)

	valid, msg := validateLocalStorageClass(scList, lsc, lvgList, effectiveLVGs)
	if !valid {
		err := fmt.Errorf("validation failed: %s", msg)
		log.Error("the LocalStorageClass is invalid", logger.Err(err))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, msg)
		if upError != nil {
			log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
		}

		return true, err
	}
	log.Debug("the LocalStorageClass is valid")

	log.Debug("building the StorageClass")
	sc, err := configureStorageClass(lsc, effectiveLVGs, ignoredLabelPrefixes)
	if err != nil {
		log.Error("unable to build the StorageClass", logger.Err(err))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
			return true, upError
		}
		return false, err
	}
	log.Debug("built the StorageClass")

	created, err := createStorageClassIfNotExists(ctx, cl, scList, sc)
	if err != nil {
		log.Error("unable to create the StorageClass", logger.Err(err), slog.String("storageClass", sc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			log.Error("unable to update the LocalStorageClass status", logger.Err(upError))
			return true, upError
		}
		return true, err
	}
	log.Debug("StorageClass creation result", "storageClass", sc.Name, "created", created)
	if created {
		log.Info("created the StorageClass", "storageClass", sc.Name)
	} else {
		log.Warn("the StorageClass already exists, requeueing", "storageClass", sc.Name)
		return true, nil
	}

	added, err := addFinalizerIfNotExistsForSC(ctx, cl, sc)
	if err != nil {
		log.Error("unable to add the finalizer to the StorageClass", logger.Err(err), slog.String("finalizer", LocalStorageClassFinalizerName), slog.String("storageClass", sc.Name))
		return true, err
	}
	log.Debug("StorageClass finalizer addition result", "finalizer", LocalStorageClassFinalizerName, "storageClass", sc.Name, "added", added)

	err = updateLocalStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	if err != nil {
		log.Error("unable to update the LocalStorageClass status", logger.Err(err))
		return true, err
	}
	log.Debug("updated the LocalStorageClass status")

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

// addFinalizerIfNotExistsForSC puts the controller finalizer on the managed
// StorageClass and reports whether it had to write anything.
//
// The early return is not a micro-optimisation. Its only caller runs it
// straight after createStorageClassIfNotExists, on a StorageClass that
// configureStorageClass built with the finalizer already in its ObjectMeta —
// so the previous unconditional Update was a no-op write on every create pass,
// costing a round-trip and a resourceVersion bump, and the "added" it returned
// was true regardless of whether anything had been added.
//
// This is the same defect ensureFinalizer was written to replace on the
// LocalStorageClass side.
func addFinalizerIfNotExistsForSC(ctx context.Context, cl client.Client, sc *v1.StorageClass) (bool, error) {
	if slices.Contains(sc.Finalizers, LocalStorageClassFinalizerName) {
		return false, nil
	}

	sc.Finalizers = append(sc.Finalizers, LocalStorageClassFinalizerName)
	if err := cl.Update(ctx, sc); err != nil {
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

// updateLocalStorageClassPhase records the outcome of a reconcile pass as both
// the coarse phase and the Ready condition.
//
// The phase is the input rather than something derived from the condition,
// unlike in the other storage modules. This controller reports its verdict from
// sixteen different call sites, and the phase vocabulary is exactly two values,
// so the two representations are in one-to-one correspondence: rewriting every
// call site to pass an error instead would be a large change with no observable
// difference in the result.
//
// The finalizer used to be appended as a side effect of the same full-object
// Update that carried the status. Now that status lives on its own subresource
// those are two calls, but the ordering is preserved — the finalizer is in place
// before the status is published. It matters because this function runs on
// failure paths that are reached before the regular finalizer step. The one
// exception is a resource that is already being deleted; see ensureFinalizer.
func updateLocalStorageClassPhase(
	ctx context.Context,
	cl client.Client,
	lsc *slv.LocalStorageClass,
	phase,
	reason string,
) error {
	if err := ensureFinalizer(ctx, cl, lsc); err != nil {
		return err
	}

	cond := metav1.Condition{
		Type:               slv.ConditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             conditions.ReasonReconciled,
		Message:            reason,
		ObservedGeneration: lsc.Generation,
	}
	if phase != CreatedStatusPhase {
		cond.Status = metav1.ConditionFalse
		cond.Reason = conditions.ReasonReconcileFailed
	}
	if cond.Message == "" {
		// The success path passes no reason, and a condition with an empty
		// message tells `kubectl describe` nothing.
		cond.Message = fmt.Sprintf("the LocalStorageClass is in the %s phase", phase)
	}

	return publishLocalStorageClassStatus(ctx, cl, lsc, cond, phase, reason)
}

// setLocalStorageClassDeleting reports that a teardown is under way, without
// touching the phase.
//
// The phase vocabulary is exactly Created and Failed, so it has nothing to say
// about a resource that is being deleted — which is why the delete path used to
// stay silent. Conditions do not have that limitation, and silence is the wrong
// answer here: a LocalStorageClass whose finalizer removal is blocked sits in
// Terminating indefinitely while still advertising Ready=True from its last
// successful pass, so an alert on "Ready=False for longer than N minutes" is
// exactly what it never fires.
func setLocalStorageClassDeleting(ctx context.Context, cl client.Client, lsc *slv.LocalStorageClass) error {
	return publishLocalStorageClassStatus(ctx, cl, lsc, metav1.Condition{
		Type:               slv.ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             conditions.ReasonDeleting,
		Message:            "the LocalStorageClass is being deleted",
		ObservedGeneration: lsc.Generation,
	}, "", "")
}

// publishLocalStorageClassStatus writes cond, and — when phase is non-empty —
// the coarse phase and its reason, through the status subresource.
//
// An empty phase leaves status.phase and status.reason as they are: the
// deletion path has a verdict to report but no phase value that could carry it.
func publishLocalStorageClassStatus(
	ctx context.Context,
	cl client.Client,
	lsc *slv.LocalStorageClass,
	cond metav1.Condition,
	phase,
	reason string,
) error {
	// The schema caps the message at 32768, and the callers pass err.Error()
	// straight through — an error carrying a command's output or an aggregate
	// of per-node failures reaches that. Over the cap the API server rejects
	// the whole status write, so the resource would keep reporting its previous
	// verdict and the reconcile would fail on the write rather than on what
	// actually went wrong. conditions.Set does not truncate: it is a thin
	// wrapper over meta.SetStatusCondition, and only the library's Ready and
	// ReadyWithMessage builders truncate for you.
	cond.Message = conditions.TruncateMessage(cond.Message)

	apply := func(sc *slv.LocalStorageClass) {
		if sc.Status == nil {
			sc.Status = new(slv.LocalStorageClassStatus)
		}
		// The generation that was actually reconciled. Carried on cond by the
		// caller, taken from the object the caller reconciled rather than from
		// the one read inside UpdateStatus: if the spec changed in between,
		// observedGeneration must still point at the generation this verdict is
		// about.
		sc.Status.ObservedGeneration = cond.ObservedGeneration
		conditions.Set(&sc.Status.Conditions, cond)
		if phase != "" {
			sc.Status.Phase = phase
			sc.Status.Reason = reason
		}
	}

	if err := conditions.UpdateStatus(ctx, cl, lsc, apply); err != nil {
		return err
	}

	// Applied to the caller's object as well, not only to the copy that gets
	// persisted. The reconcile loop reads lsc.Status straight after this to
	// publish the phase and readiness gauges, and UpdateStatus deliberately
	// leaves the object it was handed untouched — without this the metrics
	// would go on reporting the previous phase, and would find no status at
	// all on a resource that has never had one written.
	//
	// After the write rather than before it, so that the gauges cannot report
	// a status the API server rejected. The case that matters is the
	// localstorageclasses/status RBAC rule going missing: every status write
	// comes back Forbidden, the resource shows an empty phase and no Ready
	// column, and metrics claiming otherwise would be the last place anyone
	// looks. A skipped no-op write returns nil here, which is correct — the
	// server already holds what apply would have produced.
	apply(lsc)
	return nil
}

// ensureFinalizer adds the controller finalizer to lsc unless it is already
// there or the resource is being deleted. It is the only path that puts the
// finalizer on a LocalStorageClass.
//
// Skipping a terminating object is not an optimisation. The API server rejects
// any new finalizer on an object that already carries a deletionTimestamp
// ("no new finalizers can be added if the object is being deleted"), and the
// delete path strips the finalizer from the in-memory object before its own
// Update — so a failed removal would be followed by an attempt to put it back,
// the rejection would surface as this function's error, and the Failed verdict
// explaining why the deletion is stuck would never be published.
//
// The write re-reads the object and retries on conflict for the same reason
// conditions.UpdateStatus does: lsc comes from the informer cache and can be a
// revision behind, and a conflict here aborts the status write that follows.
// The same caveat as in removeControllerFinalizers applies — the re-read is served
// from the cache, so the retry buys time for the cache to catch up rather than
// guaranteeing fresh state.
func ensureFinalizer(ctx context.Context, cl client.Client, lsc *slv.LocalStorageClass) error {
	if lsc.DeletionTimestamp != nil || slices.Contains(lsc.Finalizers, LocalStorageClassFinalizerName) {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &slv.LocalStorageClass{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(lsc), fresh); err != nil {
			return err
		}
		if fresh.DeletionTimestamp != nil || slices.Contains(fresh.Finalizers, LocalStorageClassFinalizerName) {
			return nil
		}

		fresh.Finalizers = append(fresh.Finalizers, LocalStorageClassFinalizerName)
		if err := cl.Update(ctx, fresh); err != nil {
			return err
		}

		lsc.Finalizers = fresh.Finalizers
		lsc.ResourceVersion = fresh.ResourceVersion
		return nil
	})
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

	_, err := removeControllerFinalizers(ctx, cl, sc)
	if err != nil {
		return err
	}

	err = cl.Delete(ctx, sc)
	if err != nil {
		return err
	}

	return nil
}

// localStorageClassFinalizers is every spelling this controller has ever put on
// a LocalStorageClass or on the StorageClass it manages. All of them are removed
// together; see removeControllerFinalizers.
var localStorageClassFinalizers = []string{
	LocalStorageClassFinalizerName,
	LocalStorageClassFinalizerNameOld,
}

// removeControllerFinalizers drops every spelling of this controller's
// finalizer from obj and reports whether any of them was there to begin with.
//
// Taking the whole set rather than a name from the caller is the point rather
// than tidiness. Nothing migrates one name to the other: the add path tests for
// the current name only, so a resource created by a module version that used
// the old name ends up carrying both. Removing one and stopping would leave the
// other attached, report success, and return without requeueing — the resource
// would sit in Terminating forever with no event source left to revisit it. On
// the StorageClass path the same slip orphans the StorageClass in Terminating
// after its owning LocalStorageClass is already gone.
//
// The removal is done against a re-read object and retried on conflict, because
// obj comes from the informer cache and can be a revision behind. Note what
// that retry can and cannot do: cl is the manager's client, which serves
// structured reads from the same cache, so the re-read is only as current as
// the cache is. The retry absorbs the window in which the cache is catching up;
// it cannot absorb a cache that is durably behind. Callers must therefore not
// hand this function a conflict of their own making by writing to the object
// immediately beforehand — see the ordering note in reconcileLSCDeleteFunc.
func removeControllerFinalizers(ctx context.Context, cl client.Client, obj client.Object) (bool, error) {
	removed := false

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		removed = false

		fresh, ok := reflect.New(reflect.TypeOf(obj).Elem()).Interface().(client.Object)
		if !ok {
			return fmt.Errorf("%T is not a pointer to a client.Object", obj)
		}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
			// Already gone: there is no finalizer left to remove, which is the
			// outcome the caller wanted.
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		finalizers := fresh.GetFinalizers()
		kept := make([]string, 0, len(finalizers))
		for _, f := range finalizers {
			if slices.Contains(localStorageClassFinalizers, f) {
				removed = true
				continue
			}
			kept = append(kept, f)
		}
		if !removed {
			return nil
		}

		fresh.SetFinalizers(kept)
		if err := cl.Update(ctx, fresh); err != nil {
			return err
		}

		obj.SetFinalizers(kept)
		obj.SetResourceVersion(fresh.GetResourceVersion())
		return nil
	})
	if err != nil {
		return false, err
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
