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

package logger

import (
	"context"
	"strings"
	"testing"

	kwhlog "github.com/slok/kubewebhook/v2/pkg/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestKubewebhookLogger(t *testing.T, level Verbosity) (kwhlog.Logger, *strings.Builder) {
	t.Helper()

	buf := &strings.Builder{}
	l, err := NewLoggerToWriter(buf, level)
	require.NoError(t, err)
	return NewKubewebhookLogger(*l), buf
}

func TestKubewebhookAdapterFormatsMessages(t *testing.T) {
	log, buf := newTestKubewebhookLogger(t, TraceLevel)

	log.Infof("webhook %s received %d reviews", "lsc-validation", 3)

	out := buf.String()
	assert.Contains(t, out, "webhook lsc-validation received 3 reviews")
	// The library's own records are namespaced, so they can be told apart from
	// the module's.
	assert.Contains(t, out, "kubewebhook")
}

func TestKubewebhookAdapterMapsLevels(t *testing.T) {
	tests := []struct {
		name  string
		emit  func(l kwhlog.Logger)
		level string
	}{
		{name: "Debugf", emit: func(l kwhlog.Logger) { l.Debugf("m") }, level: `"level":"debug"`},
		{name: "Infof", emit: func(l kwhlog.Logger) { l.Infof("m") }, level: `"level":"info"`},
		{name: "Warningf", emit: func(l kwhlog.Logger) { l.Warningf("m") }, level: `"level":"warn"`},
		{name: "Errorf", emit: func(l kwhlog.Logger) { l.Errorf("m") }, level: `"level":"error"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newTestKubewebhookLogger(t, TraceLevel)
			tt.emit(log)
			assert.Contains(t, buf.String(), tt.level)
		})
	}
}

func TestKubewebhookAdapterRespectsLevel(t *testing.T) {
	log, buf := newTestKubewebhookLogger(t, ErrorLevel)

	// The level used to be hard-coded to Debug in main, so the webhook logged
	// at debug regardless of the module's logLevel setting.
	log.Debugf("a-debug")
	log.Infof("an-info")
	log.Errorf("an-error")

	out := buf.String()
	assert.NotContains(t, out, "a-debug")
	assert.NotContains(t, out, "an-info")
	assert.Contains(t, out, "an-error")
}

func TestKubewebhookAdapterWithValues(t *testing.T) {
	log, buf := newTestKubewebhookLogger(t, TraceLevel)

	log.WithValues(map[string]interface{}{"webhook": "lsc-validation"}).Infof("handled")

	out := buf.String()
	assert.Contains(t, out, "handled")
	assert.Contains(t, out, "lsc-validation")
}

func TestKubewebhookAdapterWithValuesAccumulates(t *testing.T) {
	log, buf := newTestKubewebhookLogger(t, TraceLevel)

	log.WithValues(map[string]interface{}{"a": "1"}).
		WithValues(map[string]interface{}{"b": "2"}).
		Infof("handled")

	out := buf.String()
	assert.Contains(t, out, `"a":"1"`)
	assert.Contains(t, out, `"b":"2"`)
}

func TestKubewebhookAdapterWithValuesDoesNotMutateTheParent(t *testing.T) {
	log, buf := newTestKubewebhookLogger(t, TraceLevel)

	child := log.WithValues(map[string]interface{}{"only": "child"})
	child.Infof("from-child")
	log.Infof("from-parent")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], "child")
	assert.NotContains(t, lines[1], `"only"`)
}

func TestKubewebhookAdapterCtxValuesRoundTrip(t *testing.T) {
	log, buf := newTestKubewebhookLogger(t, TraceLevel)

	ctx := log.SetValuesOnCtx(context.Background(), map[string]interface{}{"reviewID": "abc"})
	log.WithCtxValues(ctx).Infof("handled")

	assert.Contains(t, buf.String(), "abc")
}
