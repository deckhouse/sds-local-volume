/*
Copyright 2023 Flant JSC

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
	"errors"
	"fmt"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/strings/slices"
	v1alpha1 "sds-lvm-controller/api/v1alpha1"
	"sds-lvm-controller/pkg/config"
	"sds-lvm-controller/pkg/logger"
	"sds-lvm-controller/pkg/monitoring"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	LVMStorageClassCtrlName = "lvm-storage-class-controller"

	LVMThin = "LVMThin"
	LVM     = "LVM"

	StorageClassKind       = "StorageClass"
	StorageClassAPIVersion = "storage.k8s.io/v1"

	LVMStorageClassProvisioner   = "lvm.csi.storage.deckhouse.io"
	LVMTypeParamKey              = LVMStorageClassProvisioner + "/lvm-type"
	LVMVolumeBindingModeParamKey = LVMStorageClassProvisioner + "/volume-binding-mode"
	LVMVolumeGroupsParamKey      = LVMStorageClassProvisioner + "/lvm-volume-groups"

	DefaultStorageClassAnnotationKey = "storageclass.kubernetes.io/is-default-class"
	LVMStorageClassFinalizerName     = "lvmstorageclass.storage.deckhouse.io"

	FailedStatusPhase  = "Failed"
	CreatedStatusPhase = "Created"

	CreateReconcile reconcileType = "Create"
	UpdateReconcile reconcileType = "Update"
	DeleteReconcile reconcileType = "Delete"
)

type (
	reconcileType string
)

func RunLVMStorageClassWatcherController(
	ctx context.Context,
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
	metrics monitoring.Metrics,
) (controller.Controller, error) {
	cl := mgr.GetClient()
	interval := 5

	c, err := controller.New(LVMStorageClassCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log.Info("[LVMStorageClassReconciler] starts Reconcile")
			lsc := &v1alpha1.LvmStorageClass{}
			err := cl.Get(ctx, request.NamespacedName, lsc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[LVMStorageClassReconciler] unable to get LVMStorageClass, name: %s", request.Name))
				return reconcile.Result{}, err
			}

			scList := &v1.StorageClassList{}
			err = cl.List(ctx, scList)
			if err != nil {
				log.Error(err, "[LVMStorageClassReconciler] unable to list Storage Classes")
				return reconcile.Result{}, err
			}

			shouldRequeue := false
			recType, err := identifyReconcileFunc(scList, lsc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[LVMStorageClassReconciler] unable to identify reconcile func for the LVMStorageClass %s", lsc.Name))
			}
			switch recType {
			case CreateReconcile:
				shouldRequeue, err = reconcileLSCCreateFunc(ctx, cl, log, scList, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[LVMStorageClassReconciler] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			case UpdateReconcile:
				shouldRequeue, err = reconcileLSCUpdateFunc(ctx, cl, log, scList, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[LVMStorageClassReconciler] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			case DeleteReconcile:
				shouldRequeue, err = reconcileLSCDeleteFunc(ctx, cl, log, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[LVMStorageClassReconciler] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			default:
				log.Debug(fmt.Sprintf("[LVMStorageClassReconciler] the LVMStorageClass %s should not be reconciled", lsc.Name))
			}

			if shouldRequeue {
				log.Warning(fmt.Sprintf("[LVMStorageClassReconciler] Reconciler will requeue the request, name: %s", request.Name))
				return reconcile.Result{
					RequeueAfter: time.Duration(interval) * time.Second,
				}, nil
			}

			log.Info("[LVMStorageClassReconciler] ends Reconcile")
			return reconcile.Result{}, err
		}),
	})
	if err != nil {
		log.Error(err, "[RunLVMStorageClassWatcherController] unable to create controller")
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.LvmStorageClass{}), handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			lsc, ok := e.Object.(*v1alpha1.LvmStorageClass)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[CreateFunc] an error occurred while handling create event")
				return
			}

			scList := &v1.StorageClassList{}
			err = cl.List(ctx, scList)
			if err != nil {
				log.Error(err, "[CreateFunc] unable to list Storage Classes")
				return
			}

			shouldRequeue := false
			recType, err := identifyReconcileFunc(scList, lsc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[CreateFunc] unable to identify reconcile func for the LVMStorageClass %s", lsc.Name))
			}
			switch recType {
			case CreateReconcile:
				shouldRequeue, err = reconcileLSCCreateFunc(ctx, cl, log, scList, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[CreateFunc] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			case UpdateReconcile:
				shouldRequeue, err = reconcileLSCUpdateFunc(ctx, cl, log, scList, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[CreateFunc] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			case DeleteReconcile:
				shouldRequeue, err = reconcileLSCDeleteFunc(ctx, cl, log, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[CreateFunc] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			default:
				log.Debug(fmt.Sprintf("[CreateFunc] the LVMStorageClass %s should not be reconciled", lsc.Name))
			}

			if shouldRequeue {
				log.Warning(fmt.Sprintf("[CreateFunc] the LVMStorageClass %s event will be requeued", lsc.Name))
				q.AddAfter(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: lsc.Namespace,
						Name:      lsc.Name,
					},
				}, time.Duration(interval)*time.Second)
			}

		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			lsc, ok := e.ObjectNew.(*v1alpha1.LvmStorageClass)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[UpdateFunc] an error occurred while handling create event")
				return
			}

			scList := &v1.StorageClassList{}
			err = cl.List(ctx, scList)
			if err != nil {
				log.Error(err, "[UpdateFunc] unable to list Storage Classes")
				return
			}

			shouldRequeue := false
			recType, err := identifyReconcileFunc(scList, lsc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[UpdateFunc] unable to identify reconcile func for the LVMStorageClass %s", lsc.Name))
			}
			switch recType {
			case UpdateReconcile:
				shouldRequeue, err = reconcileLSCUpdateFunc(ctx, cl, log, scList, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[UpdateFunc] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			case DeleteReconcile:
				shouldRequeue, err = reconcileLSCDeleteFunc(ctx, cl, log, lsc)
				if err != nil {
					log.Error(err, fmt.Sprintf("[UpdateFunc] an error occured while reconciles the LVMStorageClass, name: %s", lsc.Name))
				}
			default:
				log.Debug(fmt.Sprintf("[UpdateFunc] the LVMStorageClass %s should not be reconciled", lsc.Name))
			}

			if shouldRequeue {
				log.Warning(fmt.Sprintf("[UpdateFunc] the LVMStorageClass %s event will be requeued", lsc.Name))
				q.AddAfter(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: lsc.Namespace,
						Name:      lsc.Name,
					},
				}, time.Duration(interval)*time.Second)
			}
		},
	})
	if err != nil {
		log.Error(err, "[RunLVMStorageClassWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}

func reconcileLSCDeleteFunc(ctx context.Context, cl client.Client, log logger.Logger, lsc *v1alpha1.LvmStorageClass) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] tries to get a storage class for the LVMStorageClass %s", lsc.Name))
	var sc *v1.StorageClass
	err := cl.Get(ctx, client.ObjectKey{
		Namespace: lsc.Namespace,
		Name:      lsc.Name,
	}, sc)
	if err != nil {
		if !errors2.IsNotFound(err) {
			log.Error(err, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to get a storage class for the LVMStorageClass %s", lsc.Name))
			upErr := updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to get a storage class, err: %s", err.Error()))
			if upErr != nil {
				log.Error(upErr, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to update the LVMStorageClass, name: %s", lsc.Name))
			}
			return true, err
		}

		log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] did not find a storage class for the LVMStorageClass %s", lsc.Name))
	}

	if sc != nil {
		log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] successfully found a storage class for the LVMStorageClass %s", lsc.Name))
		log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] starts identifing a provisioner for the storage class %s", sc.Name))

		if sc.Provisioner != LVMStorageClassProvisioner {
			log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] the storage class %s does not belongs to %s provisioner. It will not be deleted", sc.Name, LVMStorageClassProvisioner))
		} else {
			log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] the storage class %s belongs to %s provisioner. It will be deleted", sc.Name, LVMStorageClassProvisioner))

			err = cl.Delete(ctx, sc)
			if err != nil {
				log.Error(err, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to delete a storage class, name: %s", sc.Name))
				upErr := updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to delete a storage class, err: %s", err.Error()))
				if upErr != nil {
					log.Error(upErr, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to update the LVMStorageClass, name: %s", lsc.Name))
				}
				return true, err
			}
			log.Info(fmt.Sprintf("[reconcileLSCDeleteFunc] successfully deleted a storage class, name: %s", sc.Name))

			log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] starts removing a finalizer %s from the LVMStorageClass, name: %s", LVMStorageClassFinalizerName, lsc.Name))
			removed, err := removeLVMSCFinalizerIfExists(ctx, cl, lsc)
			if err != nil {
				log.Error(err, "[reconcileLSCDeleteFunc] unable to remove a finalizer %s from the LVMStorageClass, name: %s", LVMStorageClassFinalizerName, lsc.Name)
				upErr := updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to remove a finalizer, err: %s", err.Error()))
				if upErr != nil {
					log.Error(upErr, fmt.Sprintf("[reconcileLSCDeleteFunc] unable to update the LVMStorageClass, name: %s", lsc.Name))
				}
				return true, err
			}
			log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] the LVMStorageClass %s finalizer %s was removed: %t", lsc.Name, LVMStorageClassFinalizerName, removed))
		}
	}

	log.Debug("[reconcileLSCDeleteFunc] ends the reconciliation")
	return false, nil
}

func removeLVMSCFinalizerIfExists(ctx context.Context, cl client.Client, lsc *v1alpha1.LvmStorageClass) (bool, error) {
	removed := false
	for i, f := range lsc.Finalizers {
		if f == LVMStorageClassFinalizerName {
			lsc.Finalizers = append(lsc.Finalizers[:i], lsc.Finalizers[i+1:]...)
			removed = true
			break
		}
	}

	if removed {
		err := cl.Update(ctx, lsc)
		if err != nil {
			return false, err
		}
	}

	return removed, nil
}

func reconcileLSCUpdateFunc(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] starts the LVMStorageClass %s validation", lsc.Name))
	valid, msg := validateLVMStorageClass(ctx, cl, log, scList, lsc)
	if !valid {
		err := errors.New("validation failed. Check the resource's Status.Message for more information")
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] Unable to reconcile the LVMStorageClass, name: %s", lsc.Name))
		err = updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, msg)
		return false, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully validated the LVMStorageClass, name: %s", lsc.Name))

	var sc *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == lsc.Name {
			sc = &s
			break
		}
	}
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully got a storage class for the LVMStorageClass, name: %s", lsc.Name))

	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] starts patch a storage class by the LVMStorageClass, name: %s", lsc.Name))
	sc, err := patchSCByLSC(sc, lsc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to patch a storage class %s by the LVMStorageClass %s", sc.Name, lsc.Name))
		return false, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully patched a storage class by the LVMStorageClass, name: %s", lsc.Name))

	err = cl.Update(ctx, sc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update a storage class, name: %s", sc.Name))
		return true, err
	}

	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully updated the storage class, name: %s", sc.Name))

	err = updateLVMStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to update the LVMStorageClass, name: %s", lsc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully updated the LVMStorageClass %s status", sc.Name))

	return false, nil
}

func patchSCByLSC(sc *v1.StorageClass, lsc *v1alpha1.LvmStorageClass) (*v1.StorageClass, error) {
	lscDefault := "false"
	if lsc.Spec.IsDefault {
		lscDefault = "true"
	}

	lscLvg, err := yaml.Marshal(lsc.Spec.LVMVolumeGroups)
	if err != nil {
		return nil, err
	}

	sc.Annotations[DefaultStorageClassAnnotationKey] = lscDefault
	sc.Parameters[LVMVolumeGroupsParamKey] = string(lscLvg)
	sc.AllowVolumeExpansion = &lsc.Spec.AllowVolumeExpansion

	return sc, nil
}

func identifyReconcileFunc(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) (reconcileType, error) {
	should := shouldReconcileByCreateFunc(scList, lsc)
	if should {
		return CreateReconcile, nil
	}

	should, err := shouldReconcileByUpdateFunc(scList, lsc)
	if err != nil {
		return "", err
	}
	if should {
		return UpdateReconcile, nil
	}

	return "", nil
}

func shouldReconcileByDeleteFunc(lsc *v1alpha1.LvmStorageClass) reconcileType {
	if lsc.DeletionTimestamp != nil {
		return DeleteReconcile
	}

	return ""
}

func shouldReconcileByUpdateFunc(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) (bool, error) {
	for _, sc := range scList.Items {
		if sc.Name == lsc.Name {
			lscDefault := "false"
			if lsc.Spec.IsDefault {
				lscDefault = "true"
			}

			if sc.Annotations[DefaultStorageClassAnnotationKey] != lscDefault {
				return true, nil
			}

			lvgs, err := yaml.Marshal(lsc.Spec.LVMVolumeGroups)
			if err != nil {
				return false, err
			}

			if sc.Parameters[LVMVolumeGroupsParamKey] != string(lvgs) {
				return true, nil
			}

			if *sc.AllowVolumeExpansion != lsc.Spec.AllowVolumeExpansion {
				return true, nil
			}
		}
	}

	return false, nil
}

func shouldReconcileByCreateFunc(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) bool {
	for _, sc := range scList.Items {
		if sc.Name == lsc.Name {
			return false
		}
	}

	return true
}

func reconcileLSCCreateFunc(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] starts the LVMStorageClass %s validation", lsc.Name))
	valid, msg := validateLVMStorageClass(ctx, cl, log, scList, lsc)
	if !valid {
		err := errors.New("validation failed. Check the resource's Status.Message for more information")
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] Unable to reconcile the LVMStorageClass, name: %s", lsc.Name))
		err = updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, msg)
		return false, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully validated the LVMStorageClass, name: %s", lsc.Name))

	added, err := addFinalizerIfNotExists(ctx, cl, lsc)
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] finalizer %s was added to the LVMStorageClass %s: %t", LVMStorageClassFinalizerName, lsc.Name, added))

	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] starts storage class configuration for the LVMStorageClass, name: %s", lsc.Name))
	sc, err := configureStorageClass(lsc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to configure Storage Class for LVMStorageClass, name: %s", lsc.Name))
		return false, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully configurated storage class for the LVMStorageClass, name: %s", lsc.Name))

	err = cl.Create(ctx, sc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to create Storage Class, name: %s", sc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully create storage class, name: %s", sc.Name))

	err = updateLVMStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to update the LVMStorageClass, name: %s", lsc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully updated the LVMStorageClass %s status", sc.Name))

	return false, nil
}

func addFinalizerIfNotExists(ctx context.Context, cl client.Client, lsc *v1alpha1.LvmStorageClass) (bool, error) {
	if !slices.Contains(lsc.Finalizers, LVMStorageClassFinalizerName) {
		lsc.Finalizers = append(lsc.Finalizers, LVMStorageClassFinalizerName)
	}

	err := cl.Update(ctx, lsc)
	if err != nil {
		return false, err
	}

	return true, nil
}

func configureStorageClass(lsc *v1alpha1.LvmStorageClass) (*v1.StorageClass, error) {
	reclaimPolicy := corev1.PersistentVolumeReclaimPolicy(lsc.Spec.ReclaimPolicy)
	volumeBindingMode := v1.VolumeBindingMode(lsc.Spec.VolumeBindingMode)

	lvgsParam, err := yaml.Marshal(lsc.Spec.LVMVolumeGroups)
	if err != nil {
		return nil, err
	}

	isDefault := "false"
	if lsc.Spec.IsDefault {
		isDefault = "true"
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
				DefaultStorageClassAnnotationKey: isDefault,
			},
		},
		Provisioner: LVMStorageClassProvisioner,
		Parameters: map[string]string{
			LVMTypeParamKey:              lsc.Spec.Type,
			LVMVolumeBindingModeParamKey: lsc.Spec.VolumeBindingMode,
			LVMVolumeGroupsParamKey:      string(lvgsParam),
		},
		ReclaimPolicy:        &reclaimPolicy,
		AllowVolumeExpansion: &lsc.Spec.AllowVolumeExpansion,
		VolumeBindingMode:    &volumeBindingMode,
	}

	return sc, nil
}

func updateLVMStorageClassPhase(ctx context.Context, cl client.Client, lsc *v1alpha1.LvmStorageClass, phase, reason string) error {
	lsc.Status.Phase = phase
	lsc.Status.Reason = reason

	err := cl.Update(ctx, lsc)
	if err != nil {
		return err
	}

	return nil
}

func validateLVMStorageClass(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) (bool, string) {
	var (
		failedMsgBuilder strings.Builder
		valid            = true
	)

	if lsc.Spec.IsDefault {
		exsDefaultSCName := findOtherDefaultStorageClass(scList, lsc)
		if exsDefaultSCName != "" {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("There already is a default storage class, name: %s", exsDefaultSCName))
		}
	}

	lvgList := &v1alpha1.LvmVolumeGroupList{}
	err := cl.List(ctx, lvgList)
	if err != nil {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Unable to validate selected LVMVolumeGroups, err: %s", err.Error()))
		return valid, failedMsgBuilder.String()
	}

	nonexistentLVGs := findNonexistentLVGs(lvgList, lsc)
	if len(nonexistentLVGs) != 0 {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use the same node, LVG names: %s", strings.Join(nonexistentLVGs, ",")))
	}

	LVGsFromTheSameNode := findLVMVolumeGroupsOnTheSameNode(lvgList, lsc)
	if len(LVGsFromTheSameNode) != 0 {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use the same node, LVG names: %s", strings.Join(LVGsFromTheSameNode, ",")))
	}

	// TODO: Check if the device type checks might be configured through x-kubernetes-validations rules.
	if lsc.Spec.Type == LVMThin {
		LVGSWithNonexistentTps := findNonexistentThinPools(lvgList, lsc)
		if len(LVGSWithNonexistentTps) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use nonexistent thin pools, LVG names: %s", strings.Join(LVGsFromTheSameNode, ",")))
		}
	} else {
		LVGsWithTps := findAnyThinPool(lsc)
		if len(LVGsWithTps) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use thin pools though device type is LVM, LVG names: %s", strings.Join(LVGsFromTheSameNode, ",")))
		}
	}

	return valid, failedMsgBuilder.String()
}

func findAnyThinPool(lsc *v1alpha1.LvmStorageClass) []string {
	badLvgs := make([]string, 0, len(lsc.Spec.LVMVolumeGroups))
	for _, lvs := range lsc.Spec.LVMVolumeGroups {
		if lvs.Thin != nil {
			badLvgs = append(badLvgs, lvs.Name)
		}
	}

	return badLvgs
}

func findNonexistentThinPools(lvgList *v1alpha1.LvmVolumeGroupList, lsc *v1alpha1.LvmStorageClass) []string {
	lvgs := make(map[string]v1alpha1.LvmVolumeGroup, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = lvg
	}

	badLvgs := make([]string, 0, len(lsc.Spec.LVMVolumeGroups))
	for _, lvg := range lsc.Spec.LVMVolumeGroups {
		lvgRes := lvgs[lvg.Name]

		exist := false
		for _, tp := range lvgRes.Status.ThinPools {
			if lvg.Thin == nil {
				badLvgs = append(badLvgs, lvg.Name)
				break
			}

			if tp.Name == lvg.Thin.PoolName {
				exist = true
				break
			}
		}

		if !exist {
			badLvgs = append(badLvgs, lvg.Name)
		}
	}

	return badLvgs
}

func findNonexistentLVGs(lvgList *v1alpha1.LvmVolumeGroupList, lsc *v1alpha1.LvmStorageClass) []string {
	lvgs := make(map[string]struct{}, len(lvgList.Items))
	for _, lvg := range lvgList.Items {
		lvgs[lvg.Name] = struct{}{}
	}

	nonexistent := make([]string, 0, len(lsc.Spec.LVMVolumeGroups))
	for _, lvg := range lsc.Spec.LVMVolumeGroups {
		if _, exist := lvgs[lvg.Name]; !exist {
			nonexistent = append(nonexistent, lvg.Name)
		}
	}

	return nonexistent
}

func checkIfStorageClassExists(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) bool {
	for _, sc := range scList.Items {
		if sc.Name == lsc.Name {
			return true
		}
	}

	return false
}

func findLVMVolumeGroupsOnTheSameNode(lvgList *v1alpha1.LvmVolumeGroupList, lsc *v1alpha1.LvmStorageClass) []string {
	usedNodes := make(map[string]bool, len(lsc.Spec.LVMVolumeGroups))
	usedLVGs := make(map[string]struct{}, len(lsc.Spec.LVMVolumeGroups))
	for _, lvg := range lsc.Spec.LVMVolumeGroups {
		usedLVGs[lvg.Name] = struct{}{}
	}

	badLVGs := make([]string, 0, len(lsc.Spec.LVMVolumeGroups))
	for _, lvg := range lvgList.Items {
		if _, used := usedLVGs[lvg.Name]; used {
			for _, node := range lvg.Status.Nodes {
				if alreadyUsed := usedNodes[node.Name]; alreadyUsed {
					badLVGs = append(badLVGs, lvg.Name)
					continue
				}

				usedNodes[node.Name] = true
			}
		}
	}

	return badLVGs
}

func findOtherDefaultStorageClass(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) string {
	for _, sc := range scList.Items {
		isDefault := sc.Annotations[DefaultStorageClassAnnotationKey]
		if isDefault == "true" && sc.Name != lsc.Name {
			return sc.Name
		}
	}

	return ""
}
