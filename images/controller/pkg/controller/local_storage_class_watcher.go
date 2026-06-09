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
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/yaml"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/config"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
)

const (
	LocalStorageClassCtrlName = "local-storage-class-controller"

	LVMThinType  = "Thin"
	LVMThickType = "Thick"

	LocalStorageClassLvmType = "lvm"

	StorageClassKind       = "StorageClass"
	StorageClassAPIVersion = "storage.k8s.io/v1"

	LocalStorageClassProvisioner = "local.csi.storage.deckhouse.io"
	TypeParamKey                 = LocalStorageClassProvisioner + "/type"
	LVMTypeParamKey              = LocalStorageClassProvisioner + "/lvm-type"
	LVMVolumeBindingModeParamKey = LocalStorageClassProvisioner + "/volume-binding-mode"
	LVMVolumeGroupsParamKey      = LocalStorageClassProvisioner + "/lvm-volume-groups"
	LVMThickContiguousParamKey   = LocalStorageClassProvisioner + "/lvm-thick-contiguous"
	LVMVolumeCleanupParamKey     = LocalStorageClassProvisioner + "/lvm-volume-cleanup"

	FSTypeParamKey = "csi.storage.k8s.io/fstype"
	DefaultFSType  = "ext4"

	LocalStorageClassFinalizerName    = "storage.deckhouse.io/local-storage-class-controller"
	LocalStorageClassFinalizerNameOld = "localstorageclass.storage.deckhouse.io"

	StorageClassDefaultAnnotationKey     = "storageclass.kubernetes.io/is-default-class"
	StorageClassDefaultAnnotationValTrue = "true"

	AllowVolumeExpansionDefaultValue = true

	FailedStatusPhase  = "Failed"
	CreatedStatusPhase = "Created"

	CreateReconcile reconcileType = "Create"
	UpdateReconcile reconcileType = "Update"
	DeleteReconcile reconcileType = "Delete"
)

type (
	reconcileType string
)

func RunLocalStorageClassWatcherController(
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
) (controller.Controller, error) {
	cl := mgr.GetClient()

	c, err := controller.New(LocalStorageClassCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log.Info("[LocalStorageClassReconciler] starts Reconcile for the LocalStorageClass %s", request.Name)
			lsc := &slv.LocalStorageClass{}
			err := cl.Get(ctx, request.NamespacedName, lsc)
			if err != nil && !errors2.IsNotFound(err) {
				log.Error(err, fmt.Sprintf("[LocalStorageClassReconciler] unable to get LocalStorageClass, name: %s", request.Name))
				return reconcile.Result{}, err
			}

			if lsc.Name == "" {
				log.Info(fmt.Sprintf("[LocalStorageClassReconciler] seems like the LocalStorageClass for the request %s was deleted. Reconcile retrying will stop.", request.Name))
				return reconcile.Result{}, nil
			}

			scList := &v1.StorageClassList{}
			err = cl.List(ctx, scList)
			if err != nil {
				log.Error(err, "[LocalStorageClassReconciler] unable to list Storage Classes")
				return reconcile.Result{}, err
			}

			// Treat a Secret-load failure as a hard error and requeue: proceeding
			// with a nil filter would silently regress to pre-PR-#221 behaviour
			// and propagate GitOps / system labels onto the managed StorageClass,
			// which is exactly the bug this controller is meant to prevent.
			ignoredLabelPrefixes, err := getStorageClassLabelIgnoredPrefixes(ctx, cl, cfg.ControllerNamespace, cfg.ConfigSecretName)
			if err != nil {
				log.Error(err, fmt.Sprintf("[LocalStorageClassReconciler] unable to load storage class label ignored prefixes from secret %s/%s; requeueing without applying reconcile", cfg.ControllerNamespace, cfg.ConfigSecretName))
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			shouldRequeue, err := RunEventReconcile(ctx, cl, log, scList, lsc, ignoredLabelPrefixes)
			if err != nil {
				log.Error(err, fmt.Sprintf("[LocalStorageClassReconciler] an error occurred while reconciles the LocalStorageClass, name: %s", lsc.Name))
			}

			if shouldRequeue {
				log.Warning(fmt.Sprintf("[LocalStorageClassReconciler] Reconciler will requeue the request, name: %s", request.Name))
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			log.Info("[LocalStorageClassReconciler] ends Reconcile for the LocalStorageClass %q", request.Name)
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunLocalStorageClassWatcherController] unable to create controller")
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &slv.LocalStorageClass{}, handler.TypedFuncs[*slv.LocalStorageClass, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*slv.LocalStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Info(fmt.Sprintf("[CreateFunc] get event for LocalStorageClass %q. Add to the queue", e.Object.GetName()))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
			q.Add(request)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*slv.LocalStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Info(fmt.Sprintf("[UpdateFunc] get event for LocalStorageClass %q. Check if it should be reconciled", e.ObjectNew.GetName()))

			oldLsc := e.ObjectOld
			newLsc := e.ObjectNew

			if reflect.DeepEqual(oldLsc.Spec, newLsc.Spec) &&
				reflect.DeepEqual(oldLsc.Labels, newLsc.Labels) &&
				newLsc.DeletionTimestamp == nil {
				log.Info(fmt.Sprintf("[UpdateFunc] an update event for the LocalStorageClass %s has no Spec or Labels updates. It will not be reconciled", newLsc.Name))
				return
			}

			log.Info(fmt.Sprintf("[UpdateFunc] the LocalStorageClass %q will be reconciled. Add to the queue", newLsc.Name))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newLsc.Namespace, Name: newLsc.Name}}
			q.Add(request)
		},
	},
	),
	)
	if err != nil {
		log.Error(err, "[RunLocalStorageClassWatcherController] unable to watch the events")
		return nil, err
	}

	// Watch the controller-config Secret so that a ModuleConfig update to
	// storageClassLabelIgnoredPrefixes re-renders the Secret via Helm and
	// triggers re-reconciliation of every existing LocalStorageClass without
	// requiring a controller restart or an unrelated LSC event.
	err = c.Watch(source.Kind(
		mgr.GetCache(),
		&corev1.Secret{},
		handler.TypedEnqueueRequestsFromMapFunc[*corev1.Secret, reconcile.Request](
			func(ctx context.Context, s *corev1.Secret) []reconcile.Request {
				if s == nil || s.Namespace != cfg.ControllerNamespace || s.Name != cfg.ConfigSecretName {
					return nil
				}
				log.Info(fmt.Sprintf("[SecretWatcher] controller-config Secret %s/%s changed; enqueueing all LocalStorageClasses", s.Namespace, s.Name))
				lscList := &slv.LocalStorageClassList{}
				if err := cl.List(ctx, lscList); err != nil {
					log.Error(err, "[SecretWatcher] unable to list LocalStorageClasses for re-reconcile after config Secret change")
					return nil
				}
				reqs := make([]reconcile.Request, 0, len(lscList.Items))
				for _, lsc := range lscList.Items {
					reqs = append(reqs, reconcile.Request{
						NamespacedName: types.NamespacedName{Namespace: lsc.Namespace, Name: lsc.Name},
					})
				}
				return reqs
			},
		),
	))
	if err != nil {
		log.Error(err, "[RunLocalStorageClassWatcherController] unable to watch the controller-config Secret")
		return nil, err
	}

	return c, nil
}

func RunEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, lsc *slv.LocalStorageClass, ignoredLabelPrefixes []string) (bool, error) {
	recType, err := identifyReconcileFunc(scList, lsc, ignoredLabelPrefixes)
	if err != nil {
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[runEventReconcile] unable to update the LocalStorageClass %s status: %w", lsc.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}

	log.Debug(fmt.Sprintf("[runEventReconcile] reconcile operation: %s", recType))
	switch recType {
	case CreateReconcile:
		log.Debug(fmt.Sprintf("[runEventReconcile] CreateReconcile starts reconciliataion for the LocalStorageClass, name: %s", lsc.Name))
		return reconcileLSCCreateFunc(ctx, cl, log, scList, lsc, ignoredLabelPrefixes)
	case UpdateReconcile:
		log.Debug(fmt.Sprintf("[runEventReconcile] UpdateReconcile starts reconciliataion for the LocalStorageClass, name: %s", lsc.Name))
		return reconcileLSCUpdateFunc(ctx, cl, log, scList, lsc, ignoredLabelPrefixes)
	case DeleteReconcile:
		log.Debug(fmt.Sprintf("[runEventReconcile] DeleteReconcile starts reconciliataion for the LocalStorageClass, name: %s", lsc.Name))
		return reconcileLSCDeleteFunc(ctx, cl, log, scList, lsc)
	default:
		log.Debug(fmt.Sprintf("[runEventReconcile] the LocalStorageClass %s should not be reconciled", lsc.Name))
	}

	return false, nil
}

// getStorageClassLabelIgnoredPrefixes reads the controller config Secret and
// extracts the union of system and user label-key prefixes that must NOT be
// propagated from a LocalStorageClass to the managed StorageClass.
//
// Returns a nil slice (no filtering) if the secret is missing or the field is
// empty. This keeps the controller forward/backward compatible with secrets
// produced by older module versions.
func getStorageClassLabelIgnoredPrefixes(ctx context.Context, cl client.Client, namespace, name string) ([]string, error) {
	if namespace == "" || name == "" {
		return nil, nil
	}
	secret := &corev1.Secret{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		if errors2.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	raw, ok := secret.Data["config"]
	if !ok || len(raw) == 0 {
		return nil, nil
	}

	var parsed config.SdsLocalVolumeConfig
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unable to parse config from secret %s/%s: %w", namespace, name, err)
	}

	return parsed.StorageClassLabelIgnoredPrefixes, nil
}
