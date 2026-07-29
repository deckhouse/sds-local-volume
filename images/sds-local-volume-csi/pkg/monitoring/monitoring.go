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

// Package monitoring declares every metric the CSI driver exposes and provides
// the helpers used to record them.
//
// All metric names, label sets and bucket layouts live in this file so that the
// exposed surface can be reviewed in one place. Register must be called once
// during start-up; the recording helpers are safe to call from any goroutine.
package monitoring

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/status"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
	"github.com/deckhouse/deckhouse/pkg/metrics-storage/options"
)

// Metric names. The sds_local_volume_csi_ prefix is spelled out rather than
// being injected at runtime so that the names below are greppable from a
// dashboard or an alerting rule.
const (
	OperationsTotal          = "sds_local_volume_csi_operations_total"
	OperationDurationSeconds = "sds_local_volume_csi_operation_duration_seconds"
)

// Label names.
const (
	LabelMethod   = "method"
	LabelGRPCCode = "grpc_code"
)

// operationDurationBuckets is deliberately wider than a plain HTTP-latency
// layout: NodeStageVolume formats a filesystem and CreateVolume waits for
// sds-node-configurator to act on an LVMLogicalVolume, both of which routinely
// take seconds and can take minutes.
var operationDurationBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Register declares every metric on the given storage.
//
// Declaring metrics up front means a freshly started driver exposes them with a
// zero value instead of having them appear only once the first RPC is served,
// which keeps rate() over a restart from producing gaps.
func Register(st metricsstorage.Storage) error {
	if _, err := st.RegisterCounter(
		OperationsTotal,
		[]string{LabelMethod, LabelGRPCCode},
		options.WithHelp("Total number of served CSI operations, partitioned by gRPC status code."),
	); err != nil {
		return fmt.Errorf("register %s: %w", OperationsTotal, err)
	}

	if _, err := st.RegisterHistogram(
		OperationDurationSeconds,
		[]string{LabelMethod},
		operationDurationBuckets,
		options.WithHelp("How long a single CSI operation took, in seconds."),
	); err != nil {
		return fmt.Errorf("register %s: %w", OperationDurationSeconds, err)
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

// ObserveOperation records the duration and the gRPC status of one CSI call.
//
// fullMethod is the gRPC full method name as handed to a server interceptor,
// e.g. /csi.v1.Controller/CreateVolume.
func (r Recorder) ObserveOperation(fullMethod string, start time.Time, err error) {
	if r.st == nil {
		return
	}

	method := ShortMethodName(fullMethod)

	r.st.HistogramObserve(
		OperationDurationSeconds,
		time.Since(start).Seconds(),
		map[string]string{LabelMethod: method},
		operationDurationBuckets,
	)

	r.st.CounterAdd(OperationsTotal, 1, map[string]string{
		LabelMethod:   method,
		LabelGRPCCode: status.Code(err).String(),
	})
}

// ShortMethodName trims the gRPC service prefix from a full method name, so that
// /csi.v1.Controller/CreateVolume becomes CreateVolume.
//
// Keeping only the method keeps label cardinality at the number of CSI calls the
// driver implements, and makes the series readable in a dashboard. An input
// without a slash is returned unchanged.
func ShortMethodName(fullMethod string) string {
	if i := strings.LastIndex(fullMethod, "/"); i >= 0 {
		return fullMethod[i+1:]
	}
	return fullMethod
}
