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

package monitoring

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metricsstorage "github.com/deckhouse/deckhouse/pkg/metrics-storage"
)

// newTestRecorder returns a Recorder backed by an isolated registry plus a
// scrape function returning the current exposition text.
func newTestRecorder(t *testing.T) (Recorder, func() string) {
	t.Helper()

	st := metricsstorage.NewMetricStorage(metricsstorage.WithNewRegistry())
	require.NoError(t, Register(st))

	scrape := func() string {
		families, err := st.Gather()
		require.NoError(t, err)

		var sb strings.Builder
		for _, f := range families {
			for _, m := range f.GetMetric() {
				sb.WriteString(f.GetName())
				sb.WriteString("{")
				for _, l := range m.GetLabel() {
					sb.WriteString(l.GetName() + "=" + l.GetValue() + ",")
				}
				sb.WriteString("}")
				switch {
				case m.GetCounter() != nil:
					sb.WriteString(strconv.FormatFloat(m.GetCounter().GetValue(), 'f', -1, 64))
				case m.GetHistogram() != nil:
					sb.WriteString(strconv.FormatUint(m.GetHistogram().GetSampleCount(), 10))
				}
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}

	return NewRecorder(st), scrape
}

func TestRegisterIsIdempotent(t *testing.T) {
	st := metricsstorage.NewMetricStorage(metricsstorage.WithNewRegistry())

	// Registering the same metric set twice must not fail. This is the property
	// that makes a hand-written MustRegister init() unnecessary, and the reason a
	// duplicate declaration cannot take the process down.
	require.NoError(t, Register(st))
	require.NoError(t, Register(st))
}

func TestShortMethodName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "/csi.v1.Controller/CreateVolume", want: "CreateVolume"},
		{in: "/csi.v1.Node/NodeStageVolume", want: "NodeStageVolume"},
		{in: "/csi.v1.Identity/Probe", want: "Probe"},
		// Defensive: an interceptor is not contractually required to hand us a
		// slash-separated name, and a panic here would take down every RPC.
		{in: "CreateVolume", want: "CreateVolume"},
		{in: "", want: ""},
		{in: "/", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, ShortMethodName(tt.in))
		})
	}
}

func TestObserveOperationRecordsGRPCCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "a nil error is recorded as OK",
			wantCode: codes.OK.String(),
		},
		{
			name:     "a status error keeps its code",
			err:      status.Error(codes.InvalidArgument, "bad request"),
			wantCode: codes.InvalidArgument.String(),
		},
		{
			name:     "a NotFound status keeps its code",
			err:      status.Error(codes.NotFound, "gone"),
			wantCode: codes.NotFound.String(),
		},
		{
			// The driver returns plain errors in places; those must land in a
			// bucket rather than in an empty label.
			name:     "a plain error is recorded as Unknown",
			err:      errors.New("boom"),
			wantCode: codes.Unknown.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, scrape := newTestRecorder(t)

			rec.ObserveOperation("/csi.v1.Controller/CreateVolume", time.Now(), tt.err)

			out := scrape()
			assert.Contains(t, out, "grpc_code="+tt.wantCode+",method=CreateVolume,")
			// The duration histogram must be observed for failures too, otherwise
			// a method that only ever fails would look like it is never called.
			assert.Contains(t, out, OperationDurationSeconds+"{method=CreateVolume,}1")
		})
	}
}

func TestObserveOperationSeparatesMethods(t *testing.T) {
	rec, scrape := newTestRecorder(t)

	rec.ObserveOperation("/csi.v1.Controller/CreateVolume", time.Now(), nil)
	rec.ObserveOperation("/csi.v1.Controller/CreateVolume", time.Now(), nil)
	rec.ObserveOperation("/csi.v1.Node/NodeStageVolume", time.Now(), nil)

	out := scrape()
	assert.Contains(t, out, OperationsTotal+"{grpc_code=OK,method=CreateVolume,}2")
	assert.Contains(t, out, OperationsTotal+"{grpc_code=OK,method=NodeStageVolume,}1")
}

func TestZeroRecorderRecordsNothing(t *testing.T) {
	var rec Recorder

	// The zero value is what a test or an un-wired code path gets; it must not
	// panic.
	assert.NotPanics(t, func() {
		rec.ObserveOperation("/csi.v1.Controller/CreateVolume", time.Now(), nil)
	})
}
