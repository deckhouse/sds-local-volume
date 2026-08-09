/*
Copyright 2026 Flant JSC

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
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/deckhouse/sds-local-volume/images/controller/pkg/config"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/logger"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

const (
	LVMLogicalVolumeGCCtrlName = "lvm-logical-volume-gc-controller"

	// SDSLocalVolumeCSIFinalizer is the finalizer the CSI driver puts on every
	// LVMLogicalVolume and every LVMLogicalVolumeSnapshot it creates, and removes
	// from DeleteVolume and DeleteSnapshot respectively.
	//
	// The literal is restated here rather than imported because the controller and
	// the CSI driver are separate Go modules; images/sds-local-volume-csi/pkg/utils
	// holds the other copy.
	SDSLocalVolumeCSIFinalizer = "storage.deckhouse.io/sds-local-volume-csi"

	// SDSNodeConfiguratorFinalizer is the finalizer the sds-node-configurator
	// agent puts on an LVMLogicalVolume and on an LVMLogicalVolumeSnapshot. The
	// agent refuses to run lvremove while the resource carries any finalizer other
	// than its own, which is why leaving SDSLocalVolumeCSIFinalizer behind strands
	// both the resource and its logical volume.
	SDSNodeConfiguratorFinalizer = "storage.deckhouse.io/sds-node-configurator"

	// RetainAcknowledgedAnnotation marks an LVMLogicalVolume that is meant to
	// outlive its PersistentVolume, so that a cluster which keeps Retain volumes on
	// purpose can say so instead of silencing the orphan alert wholesale.
	//
	// Only the value "true" counts. The annotation changes nothing about what the
	// collector does — an acknowledged volume is still reclaimed once somebody
	// deletes it — it only moves the volume out of the state the alert reads.
	RetainAcknowledgedAnnotation = "storage.deckhouse.io/retain-acknowledged"
)

// errAgentFinalizerAbsent is returned when the live object turns out to carry no
// sds-node-configurator finalizer. It is a deliberate refusal, not a failed write,
// and the two are reported differently: the refusal is a property of the volume
// that an operator resolves on the node, a failed write is a fault in this module.
var errAgentFinalizerAbsent = errors.New("the sds-node-configurator finalizer is absent")

// errLiveObjectIsASuccessor is returned when the name the sweep classified is held
// by a different object by the time the write is attempted. Like
// errAgentFinalizerAbsent it is a deliberate refusal rather than a failed write,
// and unlike it there is nothing for an operator to fix: the successor is
// classified on its own terms by the next sweep.
var errLiveObjectIsASuccessor = errors.New("the live object is a different one")

// errSweepThrottled is returned by a sweep asked for sooner than minInterval after
// the previous one. It is not a failure — the sweep that will do the work is
// already due — so it is neither returned to the work queue nor counted as a
// reconcile.
var errSweepThrottled = errors.New("a sweep ran too recently")

// minSweepInterval floors how often a sweep may run.
//
// Collapsing every watch onto one work item makes a burst of events one sweep, but
// only while a sweep is in flight: an event arriving after one finishes is
// dispatched immediately. A sweep lists every LVMLogicalVolume and every
// PersistentVolume from the cache, and a cache List deep-copies each item, so
// under sustained PersistentVolume churn — the normal state of a large cluster,
// which this controller now watches in full — sweeps would otherwise run back to
// back to recompute an unchanged answer.
//
// The floor is capped by the configured sweep interval so that a test can drive
// the collector faster than this.
const minSweepInterval = 5 * time.Second

// maxReclaimsPerSweep bounds how many finalizers one pass writes off.
//
// A reclaim is two sequential uncached round-trips, and the uncached picture behind
// it is re-read whenever it goes stale, so a large backlog — the reported cluster
// had 189 orphans — would otherwise be one multi-minute pass holding the sweep lock
// and freezing the gauges at their pre-sweep values for its whole duration, exactly
// while an operator is watching them.
//
// A pass that runs out of budget classifies and publishes everything as usual and
// only defers the remaining writes, so the picture stays complete; the volumes it
// did not get to are reported as terminating, which is what they are, and picked up
// by the requeue below.
const maxReclaimsPerSweep = 50

// reclaimBacklogRequeue is the requeue a pass truncated by maxReclaimsPerSweep asks
// for. It is deliberately shorter than minSweepInterval: the floor is what decides
// when the next pass really runs, and the backlog is worked on as soon as it does.
const reclaimBacklogRequeue = time.Second

// sweepTimeout bounds one pass so that a slow or wedged API server cannot hold the
// sweep lock — and with it the metrics refresh — until the process is restarted. It
// has to cover a full budget of reclaims through the client's rate limiter with room
// to spare; a pass that hits it fails like any other and is retried by the work
// queue.
const sweepTimeout = 2 * time.Minute

// liveConfirmationMaxAge bounds how stale the uncached picture may be when a
// finalizer is written off against it.
//
// The picture is read once and then reused for the rest of the sweep, which on a
// cluster with hundreds of orphans is hundreds of sequential writes — seconds
// during which a snapshot created meanwhile would not be seen, and its source
// would be reclaimed out from under it. Re-reading when the picture is older than
// this bounds that window without turning every reclaim into a fresh pair of
// uncached lists.
const liveConfirmationMaxAge = time.Second

// volumeSnapshotContentListGVK is read through the unstructured client rather than
// a typed one: snapshot-controller is an optional dependency of this module, so
// the CRD may not be installed at all, and a missing kind has to be a normal
// answer ("nothing refers to the snapshot") rather than a start-up failure.
var volumeSnapshotContentListGVK = schema.GroupVersionKind{
	Group:   "snapshot.storage.k8s.io",
	Version: "v1",
	Kind:    "VolumeSnapshotContentList",
}

// lvmLogicalVolumeSweepKey is the single work item of this controller. Every
// watch maps onto it, so a burst of events collapses into one sweep instead of
// one reconcile per object.
var lvmLogicalVolumeSweepKey = reconcile.Request{
	NamespacedName: types.NamespacedName{Name: "lvm-logical-volume-sweep"},
}

// RunLVMLogicalVolumeGCController starts the garbage collector that unblocks the
// deletion of orphaned LVMLogicalVolumes and exports how much space they hold.
//
// An LVMLogicalVolume is orphaned when its PersistentVolume no longer exists.
// That happens on the documented reclaim flow for a StorageClass with
// reclaimPolicy: Retain: deleting the PersistentVolume is not routed to the CSI
// driver, so nothing ever calls DeleteVolume, so nothing removes
// SDSLocalVolumeCSIFinalizer — and while that finalizer is present
// sds-node-configurator will not remove the logical volume. Without this
// controller `d8 k delete lvmlogicalvolume` hangs forever and the space stays
// allocated in the volume group.
//
// LVMLogicalVolumeSnapshots are collected on the same rule and for the same
// reason. Their case is not merely symmetric but load-bearing: a snapshot keeps
// its source from being reclaimed, so a snapshot stuck on its own CSI finalizer
// would strand the volume it was taken from as well.
//
// The controller deliberately does not delete orphans on its own: with
// reclaimPolicy: Retain the data is meant to survive until an administrator asks
// for it to go. It only takes its own finalizer off the ones an administrator has
// already asked to delete, and reports the rest.
func RunLVMLogicalVolumeGCController(
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
	metrics monitoring.Recorder,
) (controller.Controller, error) {
	log = log.Named(LVMLogicalVolumeGCCtrlName)

	gc := &llvGarbageCollector{
		cl: mgr.GetClient(),
		// The confirmation reads and the finalizer update go straight to the API
		// server: acting on a stale cache here would remove the finalizer of a
		// volume whose PersistentVolume the cache has simply not seen yet.
		liveReader:  mgr.GetAPIReader(),
		log:         log,
		metrics:     metrics,
		gracePeriod: cfg.LLVOrphanGracePeriod,
		minInterval: min(minSweepInterval, cfg.LLVSweepInterval),
		maxReclaims: maxReclaimsPerSweep,
		now:         time.Now,
	}

	c, err := controller.New(LVMLogicalVolumeGCCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, _ reconcile.Request) (res reconcile.Result, err error) {
			start := time.Now()

			// A throttled pass did no work, so counting it would make the reconcile
			// metrics report passes rather than sweeps. Everything else — including a
			// panic unwinding through here — is observed exactly once.
			observe := true
			defer func() {
				if observe {
					metrics.ObserveReconcile(LVMLogicalVolumeGCCtrlName, start, &res, &err)
				}
			}()

			_, requeueAfter, err := gc.sweep(ctx)
			if errors.Is(err, errSweepThrottled) {
				observe = false
				return reconcile.Result{RequeueAfter: requeueAfter}, nil
			}
			if err != nil {
				return reconcile.Result{}, err
			}

			// A sweep always asks to be run again: the periodic pass bounds how
			// stale the orphan metrics can get, and a shorter requeue is returned
			// while an orphan is still inside its grace period.
			if requeueAfter <= 0 || requeueAfter > cfg.LLVSweepInterval {
				requeueAfter = cfg.LLVSweepInterval
			}
			return reconcile.Result{RequeueAfter: requeueAfter}, nil
		}),
	})
	if err != nil {
		log.Error("unable to create the controller", logger.Err(err))
		return nil, err
	}

	// LVMLogicalVolume events drive the reclaim itself: `d8 k delete
	// lvmlogicalvolume` shows up here as an update that sets the deletion
	// timestamp. Status-only updates from the node agent are frequent, so only the
	// fields this controller reads are allowed through.
	err = c.Watch(source.Kind(
		mgr.GetCache(),
		&snc.LVMLogicalVolume{},
		enqueueLVMLogicalVolumeSweep[*snc.LVMLogicalVolume](log.Named("llv-watcher")),
		predicate.TypedFuncs[*snc.LVMLogicalVolume]{
			CreateFunc:  func(event.TypedCreateEvent[*snc.LVMLogicalVolume]) bool { return true },
			DeleteFunc:  func(event.TypedDeleteEvent[*snc.LVMLogicalVolume]) bool { return true },
			GenericFunc: func(event.TypedGenericEvent[*snc.LVMLogicalVolume]) bool { return true },
			UpdateFunc: func(e event.TypedUpdateEvent[*snc.LVMLogicalVolume]) bool {
				return llvSweepInputChanged(e.ObjectOld, e.ObjectNew)
			},
		},
	))
	if err != nil {
		log.Error("unable to watch LVMLogicalVolume events", logger.Err(err))
		return nil, err
	}

	// A PersistentVolume being deleted is what turns a volume into an orphan, and
	// one being created is what takes it back out of the orphan set. An update can
	// do neither.
	err = c.Watch(source.Kind(
		mgr.GetCache(),
		&corev1.PersistentVolume{},
		enqueueLVMLogicalVolumeSweep[*corev1.PersistentVolume](log.Named("pv-watcher")),
		predicate.TypedFuncs[*corev1.PersistentVolume]{
			CreateFunc:  func(event.TypedCreateEvent[*corev1.PersistentVolume]) bool { return true },
			DeleteFunc:  func(event.TypedDeleteEvent[*corev1.PersistentVolume]) bool { return true },
			GenericFunc: func(event.TypedGenericEvent[*corev1.PersistentVolume]) bool { return true },
			UpdateFunc:  func(event.TypedUpdateEvent[*corev1.PersistentVolume]) bool { return false },
		},
	))
	if err != nil {
		log.Error("unable to watch PersistentVolume events", logger.Err(err))
		return nil, err
	}

	// Snapshots are both collected in their own right and a reason to re-run a
	// sweep that refused to unblock their source, so an event about one has to
	// arrive here on both counts. Status writes from the node agent cannot change
	// either answer and are filtered out.
	err = c.Watch(source.Kind(
		mgr.GetCache(),
		&snc.LVMLogicalVolumeSnapshot{},
		enqueueLVMLogicalVolumeSweep[*snc.LVMLogicalVolumeSnapshot](log.Named("llvs-watcher")),
		predicate.TypedFuncs[*snc.LVMLogicalVolumeSnapshot]{
			CreateFunc:  func(event.TypedCreateEvent[*snc.LVMLogicalVolumeSnapshot]) bool { return true },
			DeleteFunc:  func(event.TypedDeleteEvent[*snc.LVMLogicalVolumeSnapshot]) bool { return true },
			GenericFunc: func(event.TypedGenericEvent[*snc.LVMLogicalVolumeSnapshot]) bool { return true },
			UpdateFunc: func(e event.TypedUpdateEvent[*snc.LVMLogicalVolumeSnapshot]) bool {
				return llvsSweepInputChanged(e.ObjectOld, e.ObjectNew)
			},
		},
	))
	if err != nil {
		log.Error("unable to watch LVMLogicalVolumeSnapshot events", logger.Err(err))
		return nil, err
	}

	// Without an initial sweep a cluster that produces no events at all — no
	// LVMLogicalVolumes, no PersistentVolumes — would export no orphan series,
	// which reads as "no data" rather than as a zero. Leader-election runnables
	// start after the cache has synced, so the sweep sees a populated cache.
	err = mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		// This runnable and the work queue start together once leadership is won, so
		// a watch event can dispatch the first sweep before this one is scheduled.
		// The throttle then refuses it, which is the sweep that would have done this
		// work having already run — not a failure, and not worth an ERROR line on an
		// otherwise healthy start-up.
		if _, _, err := gc.sweep(ctx); err != nil && !errors.Is(err, errSweepThrottled) {
			log.Error("the initial sweep failed, the periodic one will retry", logger.Err(err))
		}
		return nil
	}))
	if err != nil {
		log.Error("unable to add the initial sweep runnable", logger.Err(err))
		return nil, err
	}

	return c, nil
}

// enqueueLVMLogicalVolumeSweep maps any event of any watched type onto the single
// sweep work item.
func enqueueLVMLogicalVolumeSweep[T client.Object](log logger.Logger) handler.TypedEventHandler[T, reconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc[T, reconcile.Request](
		func(_ context.Context, obj T) []reconcile.Request {
			log.Trace("enqueueing a sweep", "name", obj.GetName())
			return []reconcile.Request{lvmLogicalVolumeSweepKey}
		},
	)
}

// llvSweepInputChanged reports whether an update touched anything a sweep reads:
// the deletion timestamp and the finalizers drive the reclaim, the retain
// acknowledgement, the volume group, the LVM type, the phase and the actual size
// all end up in a published series.
func llvSweepInputChanged(oldLLV, newLLV *snc.LVMLogicalVolume) bool {
	if (oldLLV.DeletionTimestamp == nil) != (newLLV.DeletionTimestamp == nil) {
		return true
	}
	if !slices.Equal(oldLLV.Finalizers, newLLV.Finalizers) {
		return true
	}
	if retainAcknowledged(oldLLV) != retainAcknowledged(newLLV) {
		return true
	}
	if oldLLV.Spec.LVMVolumeGroupName != newLLV.Spec.LVMVolumeGroupName ||
		oldLLV.Spec.Type != newLLV.Spec.Type {
		return true
	}
	return llvPhase(oldLLV) != llvPhase(newLLV) ||
		llvAllocatedBytes(oldLLV) != llvAllocatedBytes(newLLV)
}

// llvsSweepInputChanged is llvSweepInputChanged for a snapshot. A snapshot has no
// phase of its own in any published series, but its source name is a label and
// changing it moves the blocking relationship to another volume.
func llvsSweepInputChanged(oldLLVS, newLLVS *snc.LVMLogicalVolumeSnapshot) bool {
	if (oldLLVS.DeletionTimestamp == nil) != (newLLVS.DeletionTimestamp == nil) {
		return true
	}
	if !slices.Equal(oldLLVS.Finalizers, newLLVS.Finalizers) {
		return true
	}
	return oldLLVS.Spec.LVMLogicalVolumeName != newLLVS.Spec.LVMLogicalVolumeName ||
		llvsUsedBytes(oldLLVS) != llvsUsedBytes(newLLVS)
}

// llvGarbageCollector holds everything a sweep needs. It is split out of the
// controller wiring so the sweep can be driven directly from a test.
type llvGarbageCollector struct {
	// cl reads through the manager cache and writes to the API server.
	cl client.Client
	// liveReader bypasses the cache.
	liveReader  client.Reader
	log         logger.Logger
	metrics     monitoring.Recorder
	gracePeriod time.Duration
	// minInterval is the shortest gap between two sweeps; see minSweepInterval.
	minInterval time.Duration
	// maxReclaims is how many finalizers one sweep may write off; see
	// maxReclaimsPerSweep. It is a field so that a test can drive the budget without
	// building fifty objects.
	maxReclaims int
	// now is injectable so a test can control the grace period.
	now func() time.Time

	// sweeping serialises sweeps and guards lastSweep. The reconciler runs one at a
	// time by itself, but the start-up sweep runs outside the work queue, and two
	// sweeps publishing the same metric group at once could leave a series from the
	// older picture behind.
	sweeping sync.Mutex
	// lastSweep is when the previous sweep finished its work, zero before the first
	// one. A sweep that failed does not stamp it: throttling the retry of a sweep
	// that never ran would turn a listing error into a silent skipped pass.
	lastSweep time.Time
}

// sweep classifies every LVMLogicalVolume and every LVMLogicalVolumeSnapshot the
// CSI driver owns, unblocks the ones an administrator has asked to delete, and
// publishes the result.
//
// The published picture is also returned, so that a test can assert on it without
// scraping the metrics. The returned duration is how long to wait before the next
// sweep when something is still inside its grace period or was deferred by
// maxReclaimsPerSweep, so that the reclaim happens shortly after the period elapses
// rather than on the next periodic pass — bounded below by minInterval, which the
// requeued reconcile still has to clear.
//
// A sweep asked for sooner than minInterval after the previous one does nothing
// and returns errSweepThrottled along with the remaining wait, leaving the
// published picture as it was; the gauges keep their last values until the sweep
// that actually runs.
func (g *llvGarbageCollector) sweep(ctx context.Context) (monitoring.LVMLogicalVolumeSweep, time.Duration, error) {
	g.sweeping.Lock()
	defer g.sweeping.Unlock()

	var empty monitoring.LVMLogicalVolumeSweep

	if !g.lastSweep.IsZero() {
		if since := g.now().Sub(g.lastSweep); since < g.minInterval {
			return empty, g.minInterval - since, errSweepThrottled
		}
	}

	// Taken after the lock, so that waiting for the pass in front does not eat this
	// one's budget. The lock itself is not context-aware; this deadline is what
	// bounds how long it can be held.
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()

	llvList := &snc.LVMLogicalVolumeList{}
	if err := g.cl.List(ctx, llvList); err != nil {
		return empty, 0, fmt.Errorf("list the LVMLogicalVolumes: %w", err)
	}

	llvsList := &snc.LVMLogicalVolumeSnapshotList{}
	if err := g.cl.List(ctx, llvsList); err != nil {
		return empty, 0, fmt.Errorf("list the LVMLogicalVolumeSnapshots: %w", err)
	}

	referenced, err := g.referencingPersistentVolumes(ctx, g.cl)
	if err != nil {
		return empty, 0, err
	}

	sweep := monitoring.LVMLogicalVolumeSweep{
		CountBy:             map[monitoring.PhaseAndType]int{},
		AllocatedBytesBy:    map[monitoring.PhaseAndType]float64{},
		AwaitingAgentByKind: map[string]int{},
	}

	// The uncached picture is read at most once per sweep — and re-read only once
	// it goes stale — and only when something has actually reached the point of
	// losing its finalizer. On a healthy cluster nothing reaches that point and the
	// sweep does no uncached reads at all.
	live := &liveConfirmation{g: g}

	// Shared by both halves: the bound is on the writes one pass performs, not on
	// how many of them are volumes.
	budget := &reclaimBudget{remaining: g.maxReclaims}

	// Snapshots first. A snapshot is what keeps its source volume blocked, so the
	// half that removes a blocker must not be scheduled behind the half it unblocks:
	// a volume backlog big enough to exhaust the budget every pass would otherwise
	// starve it. The reverse cannot happen — the snapshot set is empty on a healthy
	// cluster and tiny on any other, so it can never crowd the volumes out.
	//
	// Neither half can see the other's writes within one pass: both read the lists
	// taken above, so a volume whose snapshot is reclaimed here is still held back
	// until the next pass, which is what keeps the reclaim from racing the agent.
	snapshotsNext := g.sweepSnapshots(ctx, llvsList.Items, live, budget, &sweep)
	volumesNext := g.sweepVolumes(ctx, llvList.Items, llvsList.Items, referenced, live, budget, &sweep)

	g.metrics.SetLVMLogicalVolumeSweep(sweep)
	g.lastSweep = g.now()

	if len(sweep.Orphans) > 0 {
		g.log.Info("the sweep found LVMLogicalVolumes whose PersistentVolume is gone", "orphans", len(sweep.Orphans))
	}
	if len(sweep.OrphanedSnapshots) > 0 {
		g.log.Info("the sweep found LVMLogicalVolumeSnapshots waiting to be reclaimed", "snapshots", len(sweep.OrphanedSnapshots))
	}

	nextSweep := soonest(volumesNext, snapshotsNext)
	if budget.exhausted {
		// Logged, because a truncated pass published a complete picture and would
		// otherwise be indistinguishable from one that had nothing left to do.
		g.log.Info("the sweep used up its reclaim budget, the rest is deferred to the next pass",
			"budget", g.maxReclaims, "deferred", budget.deferred)
		nextSweep = soonest(nextSweep, reclaimBacklogRequeue)
	}

	return sweep, nextSweep, nil
}

// reclaimBudget bounds how many finalizer writes one sweep performs; see
// maxReclaimsPerSweep.
//
// It is spent only by a reclaim that has passed every cached refusal and its grace
// period, so a cluster full of volumes this module deliberately will not unblock
// costs nothing from it.
type reclaimBudget struct {
	remaining int
	// deferred counts the reclaims this pass declined to start, for the log line.
	deferred int
	// exhausted records that at least one reclaim was deferred, so that the sweep
	// asks to be run again even if the budget ran out on its very last item.
	exhausted bool
}

// spend reports whether this sweep may still write, accounting for the attempt.
func (b *reclaimBudget) spend() bool {
	if b.remaining <= 0 {
		b.exhausted = true
		b.deferred++
		return false
	}

	b.remaining--
	return true
}

// sweepVolumes classifies the LVMLogicalVolumes, filling in the volume half of the
// picture, and returns when it wants to be run again.
func (g *llvGarbageCollector) sweepVolumes(
	ctx context.Context,
	llvs []snc.LVMLogicalVolume,
	snapshots []snc.LVMLogicalVolumeSnapshot,
	referenced map[string]pvReference,
	live *liveConfirmation,
	budget *reclaimBudget,
	sweep *monitoring.LVMLogicalVolumeSweep,
) time.Duration {
	snapshotted := snapshottedVolumeNames(snapshots)

	var nextSweep time.Duration
	for i := range llvs {
		llv := &llvs[i]

		// Every volume is counted, whoever owns it. The CSI driver removes its
		// finalizer before issuing the delete, so a restart in between leaves a
		// volume this module created, cannot recognise any more, and would otherwise
		// report nowhere — the same invisible leak the collector exists to end.
		key := monitoring.PhaseAndType{Phase: llvPhase(llv), Type: llvType(llv)}
		sweep.CountBy[key]++
		sweep.AllocatedBytesBy[key] += llvAllocatedBytes(llv)

		if !slices.Contains(llv.Finalizers, SDSLocalVolumeCSIFinalizer) {
			// Either not ours, or a volume whose reclaim is already under way and
			// now waits only on sds-node-configurator. Nothing here to unblock, and
			// classifying it as an orphan would report every hand-made
			// LVMLogicalVolume as a leak.
			//
			// The second case is still counted, though: once this module has let go,
			// a deletion that never completes is exactly the leak the collector
			// exists to end, one step further down the chain, and no orphan series
			// covers it any more.
			if llv.DeletionTimestamp != nil && slices.Contains(llv.Finalizers, SDSNodeConfiguratorFinalizer) {
				sweep.AwaitingAgentByKind[monitoring.KindLVMLogicalVolume]++
			}
			continue
		}

		if pv, inUse := referenced[llv.Name]; inUse {
			g.log.Trace("a PersistentVolume still refers to the volume",
				"lvmLogicalVolume", llv.Name, "persistentVolume", pv.Name)
			continue
		}

		state, reason, requeueAfter := g.reclaim(ctx, llv, snapshotted, live, budget)
		nextSweep = soonest(nextSweep, requeueAfter)

		sweep.Orphans = append(sweep.Orphans, monitoring.OrphanedLVMLogicalVolume{
			Name:           llv.Name,
			LVMVolumeGroup: llv.Spec.LVMVolumeGroupName,
			Type:           llvType(llv),
			State:          state,
			Reason:         reason,
			AllocatedBytes: llvAllocatedBytes(llv),
		})
	}

	return nextSweep
}

// sweepSnapshots does for LVMLogicalVolumeSnapshots what sweepVolumes does for
// volumes.
//
// A snapshot has no PersistentVolume, so there is no orphan state to report for
// one nobody asked to delete: the only interesting snapshot here is one whose
// deletion this module is holding up. That set is empty on a healthy cluster and
// tiny on any other, which is why the snapshot half needs no cached pre-filter.
func (g *llvGarbageCollector) sweepSnapshots(
	ctx context.Context,
	snapshots []snc.LVMLogicalVolumeSnapshot,
	live *liveConfirmation,
	budget *reclaimBudget,
	sweep *monitoring.LVMLogicalVolumeSweep,
) time.Duration {
	var nextSweep time.Duration
	for i := range snapshots {
		llvs := &snapshots[i]

		if !slices.Contains(llvs.Finalizers, SDSLocalVolumeCSIFinalizer) {
			if llvs.DeletionTimestamp != nil && slices.Contains(llvs.Finalizers, SDSNodeConfiguratorFinalizer) {
				sweep.AwaitingAgentByKind[monitoring.KindLVMLogicalVolumeSnapshot]++
			}
			continue
		}

		if llvs.DeletionTimestamp == nil {
			continue
		}

		state, reason, requeueAfter := g.reclaimSnapshot(ctx, llvs, live, budget)
		nextSweep = soonest(nextSweep, requeueAfter)

		sweep.OrphanedSnapshots = append(sweep.OrphanedSnapshots, monitoring.OrphanedLVMLogicalVolumeSnapshot{
			Name:             llvs.Name,
			LVMLogicalVolume: llvs.Spec.LVMLogicalVolumeName,
			State:            state,
			Reason:           reason,
			UsedBytes:        llvsUsedBytes(llvs),
		})
	}

	return nextSweep
}

// reclaim decides what to do with a single orphan and returns the state and the
// block reason to report for it, plus how long to wait before looking at it again.
//
// An orphan nobody asked to delete is only reported: with reclaimPolicy: Retain
// the volume is expected to outlive its PersistentVolume until an administrator
// deletes it.
//
// snapshotted is the cached snapshot picture, used as a cheap first pass so that a
// blocked volume is reported as blocked without any uncached read. Everything that
// would actually destroy data is decided from live, which reads the API server.
func (g *llvGarbageCollector) reclaim(
	ctx context.Context,
	llv *snc.LVMLogicalVolume,
	snapshotted map[string]struct{},
	live *liveConfirmation,
	budget *reclaimBudget,
) (state, reason string, requeueAfter time.Duration) {
	log := g.log.Named("reclaim").With("lvmLogicalVolume", llv.Name)

	if llv.DeletionTimestamp == nil {
		if retainAcknowledged(llv) {
			log.Trace("the PersistentVolume is gone and the volume is acknowledged as retained, only reporting it")
			return monitoring.OrphanStateRetained, "", 0
		}
		log.Debug("the PersistentVolume is gone but nobody asked to delete the volume, only reporting it")
		return monitoring.OrphanStateActive, "", 0
	}

	if _, hasSnapshots := snapshotted[llv.Name]; hasSnapshots {
		// A snapshot that is itself terminating still counts. It is reclaimed by the
		// snapshot half of this same sweep, so the source unblocks on a later pass
		// rather than racing the agent's lvremove of the snapshot here.
		log.Warn("refusing to unblock the deletion: the logical volume still has LVMLogicalVolumeSnapshots on it, delete them first")
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonSnapshotsPresent, 0
	}

	// Removing the last finalizer would delete the resource outright, and with it
	// the only record of a logical volume that may well still exist on the node —
	// a worse leak than the one being fixed, because nothing would point at it any
	// more. sds-node-configurator adds its finalizer as the first thing it does
	// with an LVMLogicalVolume, so its absence means no agent has taken ownership
	// (a node that is down, a missing LVMVolumeGroup) and the reclaim has to wait.
	if !slices.Contains(llv.Finalizers, SDSNodeConfiguratorFinalizer) {
		log.Warn("refusing to unblock the deletion: the sds-node-configurator finalizer is absent, so nothing would remove the logical volume",
			slog.String("finalizer", SDSNodeConfiguratorFinalizer),
			slog.String("lvmVolumeGroup", llv.Spec.LVMVolumeGroupName),
		)
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonAgentFinalizerAbsent, 0
	}

	// The grace period lets an in-flight DeleteVolume remove the finalizer itself,
	// and keeps the collector from acting on the very first sweep after a deletion
	// is requested. It is measured from the deletion timestamp, so it says nothing
	// about how long the volume has existed — the live confirmation below is what
	// covers a volume whose PersistentVolume has not been created yet.
	if waited := g.now().Sub(llv.DeletionTimestamp.Time); waited < g.gracePeriod {
		remaining := g.gracePeriod - waited
		log.Debug("the volume is still inside its grace period", "remaining", remaining.String())
		return monitoring.OrphanStateTerminating, "", remaining
	}

	// Everything above is decided from the cache and costs nothing, so the budget is
	// spent here, where the uncached reads and the write begin. Still terminating as
	// far as this pass is concerned — no refusal has been decided, the work is simply
	// deferred — and the sweep asks to be run again for it.
	if !budget.spend() {
		log.Debug("the sweep has used up its reclaim budget, deferring this volume to the next pass")
		return monitoring.OrphanStateTerminating, "", reclaimBacklogRequeue
	}

	// The cache said the volume can go; confirm against the API server before
	// destroying anything.
	if err := live.load(ctx); err != nil {
		log.Error("unable to confirm against the API server that the volume can be reclaimed, keeping the finalizer", logger.Err(err))
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonAPIError, 0
	}

	if pv, inUse := live.referenced[llv.Name]; inUse {
		// The driver is logged so that a name collision with some other
		// provisioner's PersistentVolume is not mistaken for a live reference.
		log.Warn("a PersistentVolume refers to the volume after all, keeping the finalizer",
			slog.String("persistentVolume", pv.Name),
			slog.String("driver", pv.Driver),
		)
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonPersistentVolumeExists, 0
	}

	if _, hasSnapshots := live.snapshotted[llv.Name]; hasSnapshots {
		// A snapshot created while this sweep was running is not in the cache yet.
		log.Warn("refusing to unblock the deletion: the API server reports LVMLogicalVolumeSnapshots on the logical volume, delete them first")
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonSnapshotsPresent, 0
	}

	log.Info("removing the CSI finalizer so that sds-node-configurator can reclaim the logical volume",
		slog.String("finalizer", SDSLocalVolumeCSIFinalizer),
		slog.String("lvmVolumeGroup", llv.Spec.LVMVolumeGroupName),
		slog.String("actualLVNameOnTheNode", llv.Spec.ActualLVNameOnTheNode),
	)

	const kind = monitoring.KindLVMLogicalVolume

	removed, err := g.removeCSIFinalizer(ctx, kind, llv, func() client.Object { return &snc.LVMLogicalVolume{} })
	switch {
	case errors.Is(err, errAgentFinalizerAbsent):
		// The cached object said the agent owns the volume and the live one says it
		// does not. A refusal, not a failed write: the counter of write failures
		// stays untouched, and the reason names the state an operator has to fix.
		log.Warn("refusing to unblock the deletion: the live object carries no sds-node-configurator finalizer", logger.Err(err))
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonAgentFinalizerAbsent, 0
	case errors.Is(err, errLiveObjectIsASuccessor):
		// Also a refusal rather than a failed write, and the one refusal that needs
		// no operator at all: external-provisioner reused the volume ID on a retry,
		// and the object now holding the name is classified on its own terms by the
		// next sweep. Counting it as an error would raise an alert whose text asserts
		// a fault in this module.
		log.Info("the live object is a same-named successor, leaving it to the next sweep", logger.Err(err))
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonSuccessorInPlace, 0
	case err != nil && ctx.Err() != nil:
		// The sweep ran out of sweepTimeout, or the manager is shutting down. The
		// write never got an answer, so this is neither a refusal nor a fault, and
		// counting it as one would raise an alert asserting a fault in this module
		// about a routine restart. The next sweep starts this volume over.
		log.Info("the sweep was cancelled before the CSI finalizer could be written off", logger.Err(err))
		return monitoring.OrphanStateTerminating, "", 0
	case err != nil:
		log.Error("unable to remove the CSI finalizer", logger.Err(err))
		g.metrics.ObserveCSIFinalizerRemoval(kind, monitoring.ResultError)
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonRemovalFailed, 0
	case removed:
		g.metrics.ObserveCSIFinalizerRemoval(kind, monitoring.ResultSuccess)
		log.Info("removed the CSI finalizer", slog.String("finalizer", SDSLocalVolumeCSIFinalizer))
	default:
		// The live object had already lost the finalizer: the cache was behind, or
		// DeleteVolume got there first. Counting it as a removal would make the
		// counter report sweeps rather than reclaims.
		g.metrics.ObserveCSIFinalizerRemoval(kind, monitoring.ResultNoop)
		log.Debug("the CSI finalizer was already gone from the live object")
	}

	// Still terminating as far as this sweep is concerned: the resource goes away
	// once sds-node-configurator has removed the logical volume and dropped its
	// own finalizer.
	return monitoring.OrphanStateTerminating, "", 0
}

// reclaimSnapshot is reclaim for an LVMLogicalVolumeSnapshot whose deletion has
// been requested.
//
// What stands in for the PersistentVolume check is the VolumeSnapshotContent: it
// is what the snapshot-controller deletes to have DeleteSnapshot called, so one
// that still exists means the snapshot is live and only the LVMLogicalVolumeSnapshot
// underneath it was deleted by hand.
func (g *llvGarbageCollector) reclaimSnapshot(
	ctx context.Context,
	llvs *snc.LVMLogicalVolumeSnapshot,
	live *liveConfirmation,
	budget *reclaimBudget,
) (state, reason string, requeueAfter time.Duration) {
	log := g.log.Named("reclaim").With("lvmLogicalVolumeSnapshot", llvs.Name)

	if !slices.Contains(llvs.Finalizers, SDSNodeConfiguratorFinalizer) {
		log.Warn("refusing to unblock the deletion: the sds-node-configurator finalizer is absent, so nothing would remove the snapshot",
			slog.String("finalizer", SDSNodeConfiguratorFinalizer),
		)
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonAgentFinalizerAbsent, 0
	}

	if waited := g.now().Sub(llvs.DeletionTimestamp.Time); waited < g.gracePeriod {
		remaining := g.gracePeriod - waited
		log.Debug("the snapshot is still inside its grace period", "remaining", remaining.String())
		return monitoring.OrphanStateTerminating, "", remaining
	}

	// See reclaim: the budget is spent where the uncached reads and the write begin.
	if !budget.spend() {
		log.Debug("the sweep has used up its reclaim budget, deferring this snapshot to the next pass")
		return monitoring.OrphanStateTerminating, "", reclaimBacklogRequeue
	}

	if err := live.load(ctx); err != nil {
		log.Error("unable to confirm against the API server that the snapshot can be reclaimed, keeping the finalizer", logger.Err(err))
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonAPIError, 0
	}

	if _, inUse := live.snapshotContents[llvs.Name]; inUse {
		log.Warn("a VolumeSnapshotContent still refers to the snapshot, keeping the finalizer")
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonVolumeSnapshotContentExists, 0
	}

	log.Info("removing the CSI finalizer so that sds-node-configurator can remove the snapshot",
		slog.String("finalizer", SDSLocalVolumeCSIFinalizer),
		slog.String("lvmLogicalVolume", llvs.Spec.LVMLogicalVolumeName),
	)

	const kind = monitoring.KindLVMLogicalVolumeSnapshot

	removed, err := g.removeCSIFinalizer(ctx, kind, llvs, func() client.Object { return &snc.LVMLogicalVolumeSnapshot{} })
	switch {
	case errors.Is(err, errAgentFinalizerAbsent):
		log.Warn("refusing to unblock the deletion: the live object carries no sds-node-configurator finalizer", logger.Err(err))
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonAgentFinalizerAbsent, 0
	case errors.Is(err, errLiveObjectIsASuccessor):
		log.Info("the live object is a same-named successor, leaving it to the next sweep", logger.Err(err))
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonSuccessorInPlace, 0
	case err != nil && ctx.Err() != nil:
		// See reclaim: a cancelled write is neither a refusal nor a fault.
		log.Info("the sweep was cancelled before the CSI finalizer could be written off", logger.Err(err))
		return monitoring.OrphanStateTerminating, "", 0
	case err != nil:
		log.Error("unable to remove the CSI finalizer", logger.Err(err))
		g.metrics.ObserveCSIFinalizerRemoval(kind, monitoring.ResultError)
		return monitoring.OrphanStateBlocked, monitoring.BlockReasonRemovalFailed, 0
	case removed:
		g.metrics.ObserveCSIFinalizerRemoval(kind, monitoring.ResultSuccess)
		log.Info("removed the CSI finalizer", slog.String("finalizer", SDSLocalVolumeCSIFinalizer))
	default:
		g.metrics.ObserveCSIFinalizerRemoval(kind, monitoring.ResultNoop)
		log.Debug("the CSI finalizer was already gone from the live object")
	}

	return monitoring.OrphanStateTerminating, "", 0
}

// removeCSIFinalizer takes SDSLocalVolumeCSIFinalizer off the object a sweep
// decided on, leaving every other finalizer in place, and reports whether it
// actually wrote anything.
//
// cached is the object the decision was taken on, and newLive returns a fresh empty
// object of the same type to read the current one into. It is a factory rather than
// one object because the read is repeated on a conflict or a transient server error
// — sds-node-configurator writes to the same object — and client-go's JSON
// serializer unmarshals straight into whatever it is handed without zeroing it
// first, so reusing the object would let a field absent from a later response keep
// the value an earlier attempt left behind.
//
// Every invariant readable from the object itself is re-checked here against the
// live object rather than against the one the sweep classified: the UID, the
// deletion timestamp and both finalizers. A whole sweep can pass between listing
// the cache and reaching this write. The invariants that live outside the object —
// a referencing PersistentVolume, a snapshot on the volume, a VolumeSnapshotContent
// on the snapshot — are the caller's, from liveConfirmation, which bounds their
// staleness at liveConfirmationMaxAge instead of re-reading them per write.
//
// The write is a merge patch of metadata.finalizers alone, under an optimistic
// lock. A read-modify-write of the whole object would send back a spec this
// controller neither owns nor reads — the agent and the CSI driver both write to
// it — and would conflict with them for no reason. It is also why the ClusterRole
// grants patch rather than update on both resources.
func (g *llvGarbageCollector) removeCSIFinalizer(
	ctx context.Context,
	kind string,
	cached client.Object,
	newLive func() client.Object,
) (bool, error) {
	var removed bool

	err := retry.OnError(retry.DefaultBackoff, retriableWrite, func() error {
		removed = false

		live := newLive()
		if err := g.liveReader.Get(ctx, client.ObjectKeyFromObject(cached), live); err != nil {
			if apierrors.IsNotFound(err) {
				// Already gone; nothing left to unblock.
				return nil
			}
			return err
		}

		// The name of one of these resources is the CSI volume ID, which
		// external-provisioner reuses when it retries a creation, so a same-named
		// successor of the object the sweep looked at can exist. The UID is what
		// tells the two apart, and reclaiming the successor would destroy a volume
		// nobody asked to delete.
		if live.GetUID() != cached.GetUID() {
			return fmt.Errorf(
				"refusing to touch the %s %s: %w (%s != %s)",
				kind, cached.GetName(), errLiveObjectIsASuccessor, live.GetUID(), cached.GetUID(),
			)
		}

		// Only a deletion somebody asked for is ever unblocked. This is the
		// invariant the whole reclaim rests on, so it is asserted where the write
		// happens and not only where the decision was taken.
		//
		// Unlike the refusals around it this one carries no sentinel, because with a
		// matching UID it cannot happen: a deletion timestamp is set once for the
		// life of an object and never cleared. If it ever does fire, an assumption
		// this controller rests on has broken, and counting that as a fault in the
		// module is the right answer.
		if live.GetDeletionTimestamp() == nil {
			return fmt.Errorf(
				"refusing to touch the %s %s: the live object is not being deleted",
				kind, cached.GetName(),
			)
		}

		if !slices.Contains(live.GetFinalizers(), SDSLocalVolumeCSIFinalizer) {
			return nil
		}

		// reclaim checked this on the cached object; this is the live one, and it is
		// the list that the write below would actually shorten. If the two disagree,
		// the write would remove the last finalizer and delete the only record of a
		// logical volume that may still exist on the node.
		if !slices.Contains(live.GetFinalizers(), SDSNodeConfiguratorFinalizer) {
			return fmt.Errorf(
				"refusing to remove the last finalizer from the %s %s: %w, so nothing would remove the logical volume",
				kind, cached.GetName(), errAgentFinalizerAbsent,
			)
		}

		base := live.DeepCopyObject().(client.Object)
		live.SetFinalizers(slices.DeleteFunc(live.GetFinalizers(), func(f string) bool {
			return f == SDSLocalVolumeCSIFinalizer
		}))

		if err := g.cl.Patch(ctx, live, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}

		removed = true
		return nil
	})

	return removed, err
}

// retriableWrite reports whether a failed finalizer write is worth another attempt.
//
// retry.RetryOnConflict, which this replaces, retries a 409 and nothing else — so an
// API server being rolled, an etcd leader election, or a request the priority and
// fairness queues shed all came out of removeCSIFinalizer as a failed write, next to
// a permanent 403. The counter behind D8SdsLocalVolumeCSIFinalizerRemovalErrors is
// read as "a fault in the module or its permissions" and that alert has no `for:` on
// purpose, so a failure the next attempt fixes must never reach it.
//
// Cancellation is deliberately not here: a sweep that ran out of its deadline, or a
// manager shutting down, wants to stop rather than to retry. The callers tell that
// case apart from a real failure by the context.
func retriableWrite(err error) bool {
	return apierrors.IsConflict(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsTimeout(err)
}

// liveConfirmation is the uncached picture of everything that can block a reclaim,
// read when a volume or a snapshot is about to lose its finalizer and re-read once
// it is older than liveConfirmationMaxAge.
//
// It exists because the cache is not a safe basis for a destructive decision, and
// because a Get by name — the obvious confirmation — would only cover one of the
// two ways a PersistentVolume can refer to a volume, missing exactly the
// hand-written PersistentVolume that the handle matching was added for.
type liveConfirmation struct {
	g *llvGarbageCollector
	// readAt is when the picture below was read; zero means never.
	readAt time.Time

	referenced       map[string]pvReference
	snapshotted      map[string]struct{}
	snapshotContents map[string]struct{}
}

// load reads the picture from the API server, reusing the one it already has
// while that is younger than liveConfirmationMaxAge.
func (c *liveConfirmation) load(ctx context.Context) error {
	if !c.readAt.IsZero() && c.g.now().Sub(c.readAt) < liveConfirmationMaxAge {
		return nil
	}

	referenced, err := c.g.referencingPersistentVolumes(ctx, c.g.liveReader)
	if err != nil {
		return err
	}

	llvsList := &snc.LVMLogicalVolumeSnapshotList{}
	if err := c.g.liveReader.List(ctx, llvsList); err != nil {
		return fmt.Errorf("list the LVMLogicalVolumeSnapshots: %w", err)
	}

	snapshotContents, err := c.g.snapshotContentHandles(ctx, c.g.liveReader)
	if err != nil {
		return err
	}

	c.referenced = referenced
	c.snapshotted = snapshottedVolumeNames(llvsList.Items)
	c.snapshotContents = snapshotContents
	c.readAt = c.g.now()

	return nil
}

// pvReference is a PersistentVolume that keeps an LVMLogicalVolume from being
// reclaimed. The driver is carried so that a log line can tell a real reference
// apart from a name collision with another provisioner's PersistentVolume.
type pvReference struct {
	Name   string
	Driver string
}

// referencingPersistentVolumes returns, keyed by LVMLogicalVolume name, the
// PersistentVolume that still points at it.
//
// Both the PersistentVolume name and the CSI volume handle are collected: the
// driver names the LVMLogicalVolume after the volume ID, which for a dynamically
// provisioned volume is also the PersistentVolume name, but a hand-written
// PersistentVolume may carry any name while still referring to the volume through
// its handle. The handle is only taken from this module's own PersistentVolumes;
// the name is taken from every one of them, because a collision is reason enough
// not to destroy anything.
//
// A PersistentVolume that is itself being deleted still counts as a reference. Its
// kubernetes.io/pv-protection finalizer is removed by kube-controller-manager only
// once nothing uses the volume any more, so an object still sitting in Terminating
// may well be mounted somewhere. The reclaim waits for it to really go, which the
// Delete event then triggers a sweep for.
//
// The reader is a parameter so that the same rule serves both the cached first
// pass and the live confirmation, rather than the confirmation approximating it.
func (g *llvGarbageCollector) referencingPersistentVolumes(ctx context.Context, reader client.Reader) (map[string]pvReference, error) {
	pvList := &corev1.PersistentVolumeList{}
	if err := reader.List(ctx, pvList); err != nil {
		return nil, fmt.Errorf("list the PersistentVolumes: %w", err)
	}

	referenced := make(map[string]pvReference, len(pvList.Items))

	// The two key spaces overlap: a hand-written PersistentVolume's handle can equal
	// another one's name. Whether the volume is referenced is the same either way,
	// but the entry is also what the log line and the runbook's driver check name, so
	// the first referrer found is kept rather than being overwritten by a later one.
	keep := func(key string, ref pvReference) {
		if _, seen := referenced[key]; !seen {
			referenced[key] = ref
		}
	}

	for i := range pvList.Items {
		pv := &pvList.Items[i]

		ref := pvReference{Name: pv.Name}
		if pv.Spec.CSI != nil {
			ref.Driver = pv.Spec.CSI.Driver
		}

		keep(pv.Name, ref)
		if pv.Spec.CSI != nil &&
			pv.Spec.CSI.Driver == LocalStorageClassProvisioner &&
			pv.Spec.CSI.VolumeHandle != "" {
			keep(pv.Spec.CSI.VolumeHandle, ref)
		}
	}

	return referenced, nil
}

// snapshotContentHandles returns the snapshot handles — which are
// LVMLogicalVolumeSnapshot names — that a VolumeSnapshotContent of this module
// still refers to.
//
// A missing CRD is a normal answer rather than an error: snapshot-controller is an
// optional dependency, and on a cluster without it no VolumeSnapshotContent can
// refer to anything. Reading the resource unstructured is what makes that
// possible, since a typed client would need the kind registered up front.
func (g *llvGarbageCollector) snapshotContentHandles(ctx context.Context, reader client.Reader) (map[string]struct{}, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(volumeSnapshotContentListGVK)

	if err := reader.List(ctx, list); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			g.log.Trace("no VolumeSnapshotContent kind is served, treating the snapshots as unreferenced")
			return nil, nil
		}
		return nil, fmt.Errorf("list the VolumeSnapshotContents: %w", err)
	}

	handles := make(map[string]struct{}, len(list.Items))
	for i := range list.Items {
		content := list.Items[i].Object

		if driver, _, _ := unstructured.NestedString(content, "spec", "driver"); driver != LocalStorageClassProvisioner {
			continue
		}

		// A dynamically provisioned VolumeSnapshotContent carries the handle in its
		// status once the driver has reported it; a pre-provisioned one carries it in
		// its spec from the start. Both are references.
		for _, path := range [][]string{
			{"status", "snapshotHandle"},
			{"spec", "source", "snapshotHandle"},
		} {
			if handle, found, _ := unstructured.NestedString(content, path...); found && handle != "" {
				handles[handle] = struct{}{}
			}
		}
	}

	return handles, nil
}

// snapshottedVolumeNames returns the LVMLogicalVolume names that an
// LVMLogicalVolumeSnapshot is taken from.
func snapshottedVolumeNames(snapshots []snc.LVMLogicalVolumeSnapshot) map[string]struct{} {
	snapshotted := make(map[string]struct{}, len(snapshots))
	for i := range snapshots {
		if name := snapshots[i].Spec.LVMLogicalVolumeName; name != "" {
			snapshotted[name] = struct{}{}
		}
	}

	return snapshotted
}

// StripPersistentVolume keeps only what a sweep reads from a PersistentVolume and
// drops the rest before the object is committed to the informer cache. A cluster
// can hold a lot of PersistentVolumes, and this controller needs all of them but
// looks at almost none of their fields.
//
// What has to survive:
//
//   - metadata.name and spec.csi.{driver,volumeHandle}, the two ways a
//     PersistentVolume refers to an LVMLogicalVolume;
//   - metadata.deletionTimestamp and metadata.finalizers, because a
//     PersistentVolume in Terminating still counts as a reference.
//
// The object arrives freshly decoded from the watch stream, so it is edited in
// place, the same way controller-runtime's own TransformStripManagedFields does.
// The function is idempotent, which client-go requires: it may be called again
// when the same object is re-added to the store. Anything that is not a
// PersistentVolume — a tombstone, most of all — is passed through untouched.
func StripPersistentVolume(obj any) (any, error) {
	pv, ok := obj.(*corev1.PersistentVolume)
	if !ok {
		return obj, nil
	}

	stripped := corev1.PersistentVolumeSpec{}
	if pv.Spec.CSI != nil {
		stripped.CSI = &corev1.CSIPersistentVolumeSource{
			Driver:       pv.Spec.CSI.Driver,
			VolumeHandle: pv.Spec.CSI.VolumeHandle,
		}
	}
	pv.Spec = stripped
	pv.Status = corev1.PersistentVolumeStatus{}
	pv.ManagedFields = nil
	pv.Annotations = nil
	pv.Labels = nil

	return pv, nil
}

// retainAcknowledged reports whether an administrator has marked the volume as one
// that is meant to outlive its PersistentVolume.
func retainAcknowledged(llv *snc.LVMLogicalVolume) bool {
	return llv.Annotations[RetainAcknowledgedAnnotation] == "true"
}

// llvPhase returns the phase to report for an LVMLogicalVolume, falling back to
// monitoring.PhaseUnknown while the node agent has written no status.
func llvPhase(llv *snc.LVMLogicalVolume) string {
	if llv.Status == nil || llv.Status.Phase == "" {
		return monitoring.PhaseUnknown
	}
	return llv.Status.Phase
}

// llvType returns the LVM type to report for an LVMLogicalVolume.
//
// The type decides how the size below has to be read, so it is a label on every
// series carrying one, and an explicit fallback keeps it from ever being empty.
func llvType(llv *snc.LVMLogicalVolume) string {
	if llv.Spec.Type == "" {
		return monitoring.TypeUnknown
	}
	return llv.Spec.Type
}

// llvAllocatedBytes returns the size of the logical volume as last reported by the
// node agent. It is 0 while the volume is being created, which is also what should
// be reported: nothing is allocated yet.
//
// For a Thick volume this is the space held in the volume group. For a Thin one it
// is the virtual size, which says nothing about the thin pool's consumption — the
// agent reports LV_SIZE for both. That is why every series built from this number
// carries the LVM type: summing across types would present overprovisioned virtual
// size as allocated space.
func llvAllocatedBytes(llv *snc.LVMLogicalVolume) float64 {
	if llv.Status == nil {
		return 0
	}
	return float64(llv.Status.ActualSize.Value())
}

// llvsUsedBytes returns the space a snapshot actually consumes, as last reported
// by the node agent. A snapshot is always thin, so unlike an LVMLogicalVolume it
// has a meaningful used size and that is what is reported rather than its
// nominal one.
func llvsUsedBytes(llvs *snc.LVMLogicalVolumeSnapshot) float64 {
	if llvs.Status == nil {
		return 0
	}
	return float64(llvs.Status.UsedSize.Value())
}

// soonest returns the earlier of two requeue delays, treating zero as "no request".
func soonest(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	default:
		return min(a, b)
	}
}
