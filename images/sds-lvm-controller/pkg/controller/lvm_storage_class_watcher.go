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
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
	metrics monitoring.Metrics,
) (controller.Controller, error) {
	cl := mgr.GetClient()

	c, err := controller.New(LVMStorageClassCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log.Info("[LVMStorageClassReconciler] starts Reconcile")
			lsc := &v1alpha1.LvmStorageClass{}
			err := cl.Get(ctx, request.NamespacedName, lsc)
			if err != nil && !errors2.IsNotFound(err) {
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
			recType := identifyReconcileFunc(scList, lsc)
			log.Debug(fmt.Sprintf("[LVMStorageClassReconciler] reconcile operation: %s", recType))
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
					RequeueAfter: cfg.RequeueInterval * time.Second,
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
			log.Info(fmt.Sprintf("[CreateFunc] starts the reconciliation for the LVMStorageClass %s", e.Object.GetName()))
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
				err = updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to list storage classes, err: %s", err.Error()))
				q.AddAfter(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: lsc.Namespace,
						Name:      lsc.Name,
					},
				}, cfg.RequeueInterval*time.Second)
				return
			}

			shouldRequeue := false
			recType := identifyReconcileFunc(scList, lsc)
			log.Debug(fmt.Sprintf("[CreateFunc] reconcile operation: %s", recType))
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
				}, cfg.RequeueInterval*time.Second)
			}
			log.Info(fmt.Sprintf("[CreateFunc] ends the reconciliation for the LVMStorageClass %s", e.Object.GetName()))
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[UpdateFunc] starts the reconciliation for the LVMStorageClass %s", e.ObjectNew.GetName()))
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
				err = updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, fmt.Sprintf("Unable to list storage classes, err: %s", err.Error()))
				q.AddAfter(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: lsc.Namespace,
						Name:      lsc.Name,
					},
				}, cfg.RequeueInterval*time.Second)
				return
			}

			shouldRequeue := false
			recType := identifyReconcileFunc(scList, lsc)
			log.Debug(fmt.Sprintf("[UpdateFunc] reconcile operation: %s", recType))
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
				}, cfg.RequeueInterval*time.Second)
			}

			log.Info(fmt.Sprintf("[UpdateFunc] ends the reconciliation for the LVMStorageClass %s", e.ObjectNew.GetName()))
		},
	})
	if err != nil {
		log.Error(err, "[RunLVMStorageClassWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}

func reconcileLSCDeleteFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	lsc *v1alpha1.LvmStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCDeleteFunc] tries to get a storage class for the LVMStorageClass %s", lsc.Name))
	sc := &v1.StorageClass{}
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
		}
	}

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

func reconcileLSCUpdateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	lsc *v1alpha1.LvmStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] starts the LVMStorageClass %s validation", lsc.Name))
	valid, msg := validateLVMStorageClass(ctx, cl, scList, lsc)
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
	if sc == nil {
		err := errors.New(fmt.Sprintf("a storage class %s does not exist", lsc.Name))
		log.Error(err, fmt.Sprintf("[reconcileLSCUpdateFunc] unable to find a storage class for the LVMStorageClass, name: %s", lsc.Name))
		return true, err
	}

	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] successfully found a storage class for the LVMStorageClass, name: %s", lsc.Name))

	sc = patchSCByLSC(sc, lsc)
	log.Debug(fmt.Sprintf("[reconcileLSCUpdateFunc] patched a storage class by the LVMStorageClass, name: %s", lsc.Name))

	err := cl.Update(ctx, sc)
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

func patchSCByLSC(sc *v1.StorageClass, lsc *v1alpha1.LvmStorageClass) *v1.StorageClass {
	lscDefault := "false"
	if lsc.Spec.IsDefault {
		lscDefault = "true"
	}

	sc.Annotations[DefaultStorageClassAnnotationKey] = lscDefault
	sc.AllowVolumeExpansion = &lsc.Spec.AllowVolumeExpansion

	return sc
}

func identifyReconcileFunc(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) reconcileType {
	should := shouldReconcileByCreateFunc(scList, lsc)
	if should {
		return CreateReconcile
	}

	should = shouldReconcileByUpdateFunc(scList, lsc)
	if should {
		return UpdateReconcile
	}

	should = shouldReconcileByDeleteFunc(lsc)
	if should {
		return DeleteReconcile
	}

	return "none"
}

func shouldReconcileByDeleteFunc(lsc *v1alpha1.LvmStorageClass) bool {
	if lsc.DeletionTimestamp != nil {
		return true
	}

	return false
}

func shouldReconcileByUpdateFunc(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) bool {
	if lsc.DeletionTimestamp != nil {
		return false
	}

	if lsc.Status.Phase == FailedStatusPhase {
		return true
	}

	for _, sc := range scList.Items {
		if sc.Name == lsc.Name && sc.Provisioner == LVMStorageClassProvisioner {
			lscDefault := "false"
			if lsc.Spec.IsDefault {
				lscDefault = "true"
			}

			if sc.Annotations[DefaultStorageClassAnnotationKey] != lscDefault {
				return true
			}

			if *sc.AllowVolumeExpansion != lsc.Spec.AllowVolumeExpansion {
				return true
			}
		}
	}

	return false
}

func shouldReconcileByCreateFunc(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) bool {
	for _, sc := range scList.Items {
		if sc.Name == lsc.Name &&
			lsc.Status != nil {
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
	lsc *v1alpha1.LvmStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] starts the LVMStorageClass %s validation", lsc.Name))
	valid, msg := validateLVMStorageClass(ctx, cl, scList, lsc)
	if !valid {
		err := errors.New("validation failed. Check the resource's Status.Message for more information")
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] Unable to reconcile the LVMStorageClass, name: %s", lsc.Name))
		err = updateLVMStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, msg)
		return false, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully validated the LVMStorageClass, name: %s", lsc.Name))

	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] starts storage class configuration for the LVMStorageClass, name: %s", lsc.Name))
	sc, err := configureStorageClass(lsc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to configure Storage Class for LVMStorageClass, name: %s", lsc.Name))
		return false, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully configurated storage class for the LVMStorageClass, name: %s", lsc.Name))

	created, err := createStorageClassIfNotExists(ctx, cl, scList, sc)
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to create a Storage Class, name: %s", sc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] a storage class %s was created: %t", sc.Name, created))
	if created {
		log.Info(fmt.Sprintf("[reconcileLSCCreateFunc] successfully create storage class, name: %s", sc.Name))
	}

	err = updateLVMStorageClassPhase(ctx, cl, lsc, CreatedStatusPhase, "")
	if err != nil {
		log.Error(err, fmt.Sprintf("[reconcileLSCCreateFunc] unable to update the LVMStorageClass, name: %s", lsc.Name))
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] successfully updated the LVMStorageClass %s status", sc.Name))

	added, err := addFinalizerIfNotExists(ctx, cl, lsc)
	log.Debug(fmt.Sprintf("[reconcileLSCCreateFunc] finalizer %s was added to the LVMStorageClass %s: %t", LVMStorageClassFinalizerName, lsc.Name, added))

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

func updateLVMStorageClassPhase(
	ctx context.Context,
	cl client.Client,
	lsc *v1alpha1.LvmStorageClass,
	phase,
	reason string,
) error {
	if lsc.Status == nil {
		lsc.Status = new(v1alpha1.LvmStorageClassStatus)
	}
	lsc.Status.Phase = phase
	lsc.Status.Reason = reason

	err := cl.Update(ctx, lsc)
	if err != nil {
		return err
	}

	return nil
}

func validateLVMStorageClass(
	ctx context.Context,
	cl client.Client,
	scList *v1.StorageClassList,
	lsc *v1alpha1.LvmStorageClass,
) (bool, string) {
	var (
		failedMsgBuilder strings.Builder
		valid            = true
	)

	unmanagedScName := findUnmanagedDuplicatedSC(scList, lsc)
	if unmanagedScName != "" {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("There already is a storage class with the same name: %s\n", unmanagedScName))
	}

	if lsc.Spec.IsDefault {
		exsDefaultSCName := findOtherDefaultStorageClass(scList, lsc)
		if exsDefaultSCName != "" {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("There already is a default storage class, name: %s\n", exsDefaultSCName))
		}
	}

	lvgList := &v1alpha1.LvmVolumeGroupList{}
	err := cl.List(ctx, lvgList)
	if err != nil {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Unable to validate selected LVMVolumeGroups, err: %s\n", err.Error()))
		return valid, failedMsgBuilder.String()
	}

	nonexistentLVGs := findNonexistentLVGs(lvgList, lsc)
	if len(nonexistentLVGs) != 0 {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Some of selected LVMVolumeGroups are nonexistent, LVG names: %s\n", strings.Join(nonexistentLVGs, ",")))
	}

	LVGsFromTheSameNode := findLVMVolumeGroupsOnTheSameNode(lvgList, lsc)
	if len(LVGsFromTheSameNode) != 0 {
		valid = false
		failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use the same node, LVG names: %s\n", strings.Join(LVGsFromTheSameNode, "")))
	}

	if lsc.Spec.Type == LVMThin {
		LVGSWithNonexistentTps := findNonexistentThinPools(lvgList, lsc)
		if len(LVGSWithNonexistentTps) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use nonexistent thin pools, LVG names: %s\n", strings.Join(LVGSWithNonexistentTps, ",")))
		}
	} else {
		LVGsWithTps := findAnyThinPool(lsc)
		if len(LVGsWithTps) != 0 {
			valid = false
			failedMsgBuilder.WriteString(fmt.Sprintf("Some LVMVolumeGroups use thin pools though device type is LVM, LVG names: %s\n", strings.Join(LVGsWithTps, ",")))
		}
	}

	return valid, failedMsgBuilder.String()
}

func findUnmanagedDuplicatedSC(scList *v1.StorageClassList, lsc *v1alpha1.LvmStorageClass) string {
	for _, sc := range scList.Items {
		if sc.Name == lsc.Name && sc.Provisioner != LVMStorageClassProvisioner {
			return sc.Name
		}
	}

	return ""
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
	for _, lscLvg := range lsc.Spec.LVMVolumeGroups {
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

func findLVMVolumeGroupsOnTheSameNode(lvgList *v1alpha1.LvmVolumeGroupList, lsc *v1alpha1.LvmStorageClass) []string {
	nodesWithLVGs := make(map[string][]string, len(lsc.Spec.LVMVolumeGroups))
	usedLVGs := make(map[string]struct{}, len(lsc.Spec.LVMVolumeGroups))
	for _, lvg := range lsc.Spec.LVMVolumeGroups {
		usedLVGs[lvg.Name] = struct{}{}
	}

	badLVGs := make([]string, 0, len(lsc.Spec.LVMVolumeGroups))
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
			msgBuilder.WriteString(fmt.Sprintf("%s:", nodeName))
			for _, lvgName := range lvgs {
				msgBuilder.WriteString(fmt.Sprintf("%s,", lvgName))
			}

			badLVGs = append(badLVGs, msgBuilder.String())
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
