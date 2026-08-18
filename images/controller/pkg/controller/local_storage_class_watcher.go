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
	"log/slog"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/yaml"

	"github.com/deckhouse/sds-common-lib/conditions"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/config"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
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
	metrics monitoring.Recorder,
) (controller.Controller, error) {
	cl := mgr.GetClient()
	log = log.Named(LocalStorageClassCtrlName)

	c, err := controller.New(LocalStorageClassCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (res reconcile.Result, err error) {
			defer metrics.ObserveReconcile(LocalStorageClassCtrlName, time.Now(), &res, &err)

			log := log.Named("reconcile").With("localStorageClass", request.Name)

			log.Info("start")
			lsc := &slv.LocalStorageClass{}
			err = cl.Get(ctx, request.NamespacedName, lsc)
			if err != nil && !apierrors.IsNotFound(err) {
				log.Error("unable to get the LocalStorageClass", logger.Err(err))
				return reconcile.Result{}, err
			}

			if lsc.Name == "" {
				log.Info("the LocalStorageClass was deleted, stopping the reconcile")
				// The resource is gone, so stop exporting its phase gauge.
				metrics.ForgetLocalStorageClass(request.Name)
				return reconcile.Result{}, nil
			}

			scList := &v1.StorageClassList{}
			err = cl.List(ctx, scList)
			if err != nil {
				log.Error("unable to list the StorageClasses", logger.Err(err))
				return reconcile.Result{}, err
			}

			// Treat a Secret-load failure as a hard error and requeue: proceeding
			// with a nil filter would silently regress to pre-PR-#221 behaviour
			// and propagate GitOps / system labels onto the managed StorageClass,
			// which is exactly the bug this controller is meant to prevent.
			ignoredLabelPrefixes, err := getStorageClassLabelIgnoredPrefixes(ctx, cl, cfg.ControllerNamespace, cfg.ConfigSecretName)
			if err != nil {
				log.Error("unable to load the ignored storage class label prefixes, requeueing without reconciling", logger.Err(err), slog.String("secretNamespace", cfg.ControllerNamespace), slog.String("secretName", cfg.ConfigSecretName))
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			shouldRequeue, err := RunEventReconcile(ctx, cl, log, scList, lsc, ignoredLabelPrefixes)
			if err != nil {
				log.Error("unable to reconcile the LocalStorageClass", logger.Err(err))
			}

			// RunEventReconcile updates lsc.Status.Phase in place, so this reads
			// the phase the reconcile just settled on.
			//
			// Status is a pointer and stays nil on every path that returns before
			// reaching updateLocalStorageClassPhase: the finalizer step failing,
			// the managed StorageClass already existing, and the whole deletion
			// path. It is also nil for the lifetime of an object created while the
			// new CRD was applied but the old controller image was still running,
			// since that controller's full-object Updates could no longer carry a
			// status. Dereferencing it there is a panic, not an empty phase.
			if lsc.Status != nil && lsc.Status.Phase != "" {
				metrics.SetLocalStorageClassPhase(lsc.Name, lsc.Status.Phase)
			}

			// Ready is published on every pass, including as 0 for a resource
			// whose status has never been written. The phase gauge cannot
			// carry this: its vocabulary is Created/Failed, so a resource
			// wedged in Terminating keeps reporting Created while the Ready
			// condition says otherwise. Publishing 0 rather than nothing is
			// deliberate — an absent series is indistinguishable from a scrape
			// gap, and this is the series an alert would be written against.
			metrics.SetLocalStorageClassReady(
				lsc.Name,
				lsc.Status != nil && conditions.IsTrue(lsc.Status.Conditions, slv.ConditionTypeReady),
			)

			if shouldRequeue {
				log.Warn("requeueing the request")
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			log.Info("done")
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error("unable to create the controller", logger.Err(err))
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &slv.LocalStorageClass{}, localStorageClassEventHandler(log)))
	if err != nil {
		log.Error("unable to watch LocalStorageClass events", logger.Err(err))
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
				log.Named("secret-watcher").Info("the controller-config Secret changed, enqueueing all LocalStorageClasses", "secretNamespace", s.Namespace, "secretName", s.Name)
				lscList := &slv.LocalStorageClassList{}
				if err := cl.List(ctx, lscList); err != nil {
					log.Named("secret-watcher").Error("unable to list the LocalStorageClasses after a config Secret change", logger.Err(err))
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
		log.Error("unable to watch the controller-config Secret", logger.Err(err))
		return nil, err
	}

	// Watch LVMVolumeGroups so that adding, removing or relabeling an LVG
	// re-reconciles every selector-based LocalStorageClass and keeps its
	// managed StorageClass in sync with the current selector match set.
	// Explicit-list LocalStorageClasses are unaffected by LVG label churn and
	// are therefore skipped.
	err = c.Watch(source.Kind(
		mgr.GetCache(),
		&snc.LVMVolumeGroup{},
		handler.TypedEnqueueRequestsFromMapFunc[*snc.LVMVolumeGroup, reconcile.Request](
			func(ctx context.Context, _ *snc.LVMVolumeGroup) []reconcile.Request {
				lscList := &slv.LocalStorageClassList{}
				if err := cl.List(ctx, lscList); err != nil {
					log.Named("lvg-watcher").Error("unable to list the LocalStorageClasses after an LVMVolumeGroup change", logger.Err(err))
					return nil
				}
				reqs := make([]reconcile.Request, 0, len(lscList.Items))
				for i := range lscList.Items {
					lsc := &lscList.Items[i]
					if !lscUsesLabelSelector(lsc) {
						continue
					}
					reqs = append(reqs, reconcile.Request{
						NamespacedName: types.NamespacedName{Namespace: lsc.Namespace, Name: lsc.Name},
					})
				}
				return reqs
			},
		),
		// Only Create/Delete and label changes affect selector membership;
		// ignore the frequent status-only updates the agent writes, otherwise
		// every LVMVolumeGroup heartbeat would re-reconcile all selector-based
		// LocalStorageClasses.
		predicate.TypedFuncs[*snc.LVMVolumeGroup]{
			CreateFunc: func(event.TypedCreateEvent[*snc.LVMVolumeGroup]) bool { return true },
			DeleteFunc: func(event.TypedDeleteEvent[*snc.LVMVolumeGroup]) bool { return true },
			UpdateFunc: func(e event.TypedUpdateEvent[*snc.LVMVolumeGroup]) bool {
				return !reflect.DeepEqual(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels())
			},
			GenericFunc: func(event.TypedGenericEvent[*snc.LVMVolumeGroup]) bool { return false },
		},
	))
	if err != nil {
		log.Error("unable to watch the LVMVolumeGroups", logger.Err(err))
		return nil, err
	}

	return c, nil
}

// localStorageClassEventHandler maps LocalStorageClass watch events onto
// reconcile requests.
func localStorageClassEventHandler(log logger.Logger) handler.TypedFuncs[*slv.LocalStorageClass, reconcile.Request] {
	enqueue := func(q workqueue.TypedRateLimitingInterface[reconcile.Request], obj *slv.LocalStorageClass) {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}})
	}

	return handler.TypedFuncs[*slv.LocalStorageClass, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*slv.LocalStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Named("create-event").Info("enqueueing the LocalStorageClass", "localStorageClass", e.Object.GetName())
			enqueue(q, e.Object)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*slv.LocalStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Named("update-event").Info("checking whether the LocalStorageClass should be reconciled", "localStorageClass", e.ObjectNew.GetName())

			oldLsc := e.ObjectOld
			newLsc := e.ObjectNew

			if reflect.DeepEqual(oldLsc.Spec, newLsc.Spec) &&
				reflect.DeepEqual(oldLsc.Labels, newLsc.Labels) &&
				newLsc.DeletionTimestamp == nil {
				log.Named("update-event").Info("the update touched neither spec nor labels, not reconciling", "localStorageClass", newLsc.Name)
				return
			}

			log.Named("update-event").Info("enqueueing the LocalStorageClass", "localStorageClass", newLsc.Name)
			enqueue(q, newLsc)
		},
		// There is nothing left to reconcile, and that is precisely why the
		// event has to be delivered: the reconciler is the only thing that
		// calls ForgetLocalStorageClass, and it reaches it through the "the
		// object is gone" branch, which needs a request for a name the API
		// server no longer knows.
		//
		// Dropping this event leaves the gauges keyed by that name exported
		// until the process restarts, holding whatever the final pass
		// published — for a teardown that succeeded, ready=0, which is the
		// value an alert is written against.
		//
		// Enqueueing rather than forgetting the series here is deliberate. The
		// workqueue serialises by key, so this request is handled after the
		// delete pass that set those gauges has finished; dropping them from
		// this handler would race that pass and could be undone by it.
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[*slv.LocalStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Named("delete-event").Info("enqueueing the deleted LocalStorageClass", "localStorageClass", e.Object.GetName())
			enqueue(q, e.Object)
		},
	}
}

func RunEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, lsc *slv.LocalStorageClass, ignoredLabelPrefixes []string) (bool, error) {
	// Resolve the effective LVMVolumeGroups (explicit list or the ones matched
	// by the label selector) once per reconcile so that every downstream step
	// operates on the same deterministic set. The LVMVolumeGroup list is only
	// needed when the resource is not being deleted.
	lvgList := &snc.LVMVolumeGroupList{}
	if lsc.DeletionTimestamp == nil {
		if err := cl.List(ctx, lvgList); err != nil {
			log.Error("unable to list the LVMVolumeGroups for the LocalStorageClass", logger.Err(err), slog.String("localStorageClass", lsc.Name))
			upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
			if upError != nil {
				err = errors.Join(err, fmt.Errorf("[runEventReconcile] unable to update the LocalStorageClass %s status: %w", lsc.Name, upError))
			}
			return true, err
		}
	}

	effectiveLVGs, err := resolveEffectiveLVGs(lsc, lvgList)
	if err != nil && lsc.DeletionTimestamp == nil {
		log.Error("unable to resolve the LVMVolumeGroups for the LocalStorageClass", logger.Err(err), slog.String("localStorageClass", lsc.Name))
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			err = errors.Join(err, fmt.Errorf("[runEventReconcile] unable to update the LocalStorageClass %s status: %w", lsc.Name, upError))
		}
		return true, err
	}

	recType, err := identifyReconcileFunc(scList, lsc, effectiveLVGs, ignoredLabelPrefixes)
	if err != nil {
		upError := updateLocalStorageClassPhase(ctx, cl, lsc, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[runEventReconcile] unable to update the LocalStorageClass %s status: %w", lsc.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}

	log.Debug("resolved the reconcile type", "reconcileType", recType)
	switch recType {
	case CreateReconcile:
		log.Debug("running the create reconcile", "localStorageClass", lsc.Name)
		return reconcileLSCCreateFunc(ctx, cl, log, scList, lsc, lvgList, effectiveLVGs, ignoredLabelPrefixes)
	case UpdateReconcile:
		log.Debug("running the update reconcile", "localStorageClass", lsc.Name)
		return reconcileLSCUpdateFunc(ctx, cl, log, scList, lsc, lvgList, effectiveLVGs, ignoredLabelPrefixes)
	case DeleteReconcile:
		log.Debug("running the delete reconcile", "localStorageClass", lsc.Name)
		return reconcileLSCDeleteFunc(ctx, cl, log, scList, lsc)
	default:
		log.Debug("the LocalStorageClass should not be reconciled", "localStorageClass", lsc.Name)
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
		if apierrors.IsNotFound(err) {
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
