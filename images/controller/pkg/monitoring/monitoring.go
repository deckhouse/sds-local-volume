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

// Package monitoring declares every metric the controller exposes and provides
// the helpers used to record them.
//
// All metric names, label sets and bucket layouts live in this file so that the
// exposed surface can be reviewed in one place. Register must be called once
// during start-up; the recording helpers are safe to call from any goroutine.
package monitoring

import (
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
	"github.com/deckhouse/deckhouse/pkg/metrics-storage/options"
)

// Metric names. The sds_local_volume_ prefix is spelled out rather than being
// injected at runtime so that the names below are greppable from a dashboard or
// an alerting rule.
const (
	ReconcilesTotal          = "sds_local_volume_reconciles_total"
	ReconcileDurationSeconds = "sds_local_volume_reconcile_duration_seconds"
	LocalStorageClassPhase   = "sds_local_volume_local_storage_class_phase"
	LocalStorageClassReady   = "sds_local_volume_local_storage_class_ready"
)

// Label names.
const (
	LabelController = "controller"
	LabelResult     = "result"
	LabelName       = "name"
	LabelPhase      = "phase"
)

// Values of the LabelResult label.
const (
	ResultSuccess = "success"
	ResultError   = "error"
	ResultRequeue = "requeue"
)

// reconcileDurationBuckets covers a fast cache-only reconcile (single-digit
// milliseconds) up to a reconcile that waits on several API round-trips.
var reconcileDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// Register declares every metric on the given storage.
//
// Declaring metrics up front means a freshly started controller exposes them
// with a zero value instead of having them appear only once the first event is
// recorded, which keeps rate() over a restart from producing gaps.
func Register(st metricsstorage.Storage) error {
	if _, err := st.RegisterCounter(
		ReconcilesTotal,
		[]string{LabelController, LabelResult},
		options.WithHelp("Total number of reconcile invocations, partitioned by outcome."),
	); err != nil {
		return fmt.Errorf("register %s: %w", ReconcilesTotal, err)
	}

	if _, err := st.RegisterHistogram(
		ReconcileDurationSeconds,
		[]string{LabelController},
		reconcileDurationBuckets,
		options.WithHelp("How long a single reconcile invocation took, in seconds."),
	); err != nil {
		return fmt.Errorf("register %s: %w", ReconcileDurationSeconds, err)
	}

	if _, err := st.RegisterGauge(
		LocalStorageClassPhase,
		[]string{LabelName, LabelPhase},
		options.WithHelp("Current phase of a LocalStorageClass, 1 for the active phase."),
	); err != nil {
		return fmt.Errorf("register %s: %w", LocalStorageClassPhase, err)
	}

	if _, err := st.RegisterGauge(
		LocalStorageClassReady,
		[]string{LabelName},
		options.WithHelp("Whether the Ready condition of a LocalStorageClass is True, 1 or 0."),
	); err != nil {
		return fmt.Errorf("register %s: %w", LocalStorageClassReady, err)
	}

	return nil
}

// Recorder records the metrics declared above.
//
// The zero value is usable and records nothing, so tests and any code path that
// has no storage wired up do not need a nil check.
type Recorder struct {
	st metricsstorage.Storage
}

// NewRecorder returns a Recorder writing into st.
func NewRecorder(st metricsstorage.Storage) Recorder {
	return Recorder{st: st}
}

// ObserveReconcile records the duration and the outcome of a single reconcile.
//
// It is meant to be deferred from a reconciler that uses named return values,
// so that every return path — including a panic unwinding through it — is
// counted exactly once:
//
//	func(ctx context.Context, req reconcile.Request) (res reconcile.Result, err error) {
//	    defer metrics.ObserveReconcile(CtrlName, time.Now(), &res, &err)
//
// start is evaluated when the defer statement runs, res and err are read when
// the deferred call fires.
func (r Recorder) ObserveReconcile(controller string, start time.Time, res *reconcile.Result, err *error) {
	if r.st == nil {
		return
	}

	r.st.HistogramObserve(
		ReconcileDurationSeconds,
		time.Since(start).Seconds(),
		map[string]string{LabelController: controller},
		reconcileDurationBuckets,
	)

	r.st.CounterAdd(ReconcilesTotal, 1, map[string]string{
		LabelController: controller,
		LabelResult:     reconcileResult(res, err),
	})
}

// reconcileResult classifies a reconcile outcome. An error wins over a requeue,
// because controller-runtime requeues on error regardless of the returned Result.
func reconcileResult(res *reconcile.Result, err *error) string {
	switch {
	case err != nil && *err != nil:
		return ResultError
	case res != nil && (res.Requeue || res.RequeueAfter > 0):
		return ResultRequeue
	default:
		return ResultSuccess
	}
}

// SetLocalStorageClassPhase publishes the phase of a single LocalStorageClass.
//
// The gauge is grouped per resource name and the group is expired before the new
// value is set, so a resource that moves from Created to Failed leaves exactly
// one series behind instead of two competing ones.
func (r Recorder) SetLocalStorageClassPhase(name, phase string) {
	if r.st == nil {
		return
	}

	grouped := r.st.Grouped()
	grouped.ExpireGroupMetricByName(localStorageClassGroup(name), LocalStorageClassPhase)
	grouped.GaugeSet(localStorageClassGroup(name), LocalStorageClassPhase, 1, map[string]string{
		LabelName:  name,
		LabelPhase: phase,
	})
}

// SetLocalStorageClassReady publishes whether the Ready condition of a single
// LocalStorageClass is True.
//
// It exists because the phase gauge cannot express the state that matters most.
// The phase vocabulary is exactly Created and Failed and has no word for a
// teardown, so a LocalStorageClass wedged in Terminating — held by a finalizer
// that never gets removed — goes on reporting phase=Created indefinitely, which
// is the one thing an operator needs to be told about and the one thing the
// phase cannot say. Ready goes to 0 there.
//
// The label set is just the resource name, so successive calls overwrite the
// same series and no expiry is needed between them; ForgetLocalStorageClass
// drops it along with the phase series when the resource goes away.
func (r Recorder) SetLocalStorageClassReady(name string, ready bool) {
	if r.st == nil {
		return
	}

	value := 0.0
	if ready {
		value = 1
	}

	r.st.Grouped().GaugeSet(localStorageClassGroup(name), LocalStorageClassReady, value, map[string]string{
		LabelName: name,
	})
}

// ForgetLocalStorageClass drops every series published for a LocalStorageClass.
// Without it a deleted resource would keep being exported until the process
// restarts.
func (r Recorder) ForgetLocalStorageClass(name string) {
	if r.st == nil {
		return
	}

	r.st.Grouped().ExpireGroupMetrics(localStorageClassGroup(name))
}

// localStorageClassGroup returns the expiry group holding the series of a single
// LocalStorageClass. The prefix keeps the group namespaced against any other
// grouped metric added later.
func localStorageClassGroup(name string) string {
	return "local-storage-class/" + name
}
