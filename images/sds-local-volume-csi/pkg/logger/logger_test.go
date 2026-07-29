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
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deckhouse/deckhouse/pkg/log"
)

func newBufLogger(t *testing.T, level Verbosity) (*Logger, *bytes.Buffer) {
	t.Helper()

	buf := &bytes.Buffer{}
	l, err := NewLoggerToWriter(buf, level)
	require.NoError(t, err)
	return l, buf
}

func TestVerbosityLevelMapping(t *testing.T) {
	// The numeric LOG_LEVEL contract is shared with lib-helm and the CSI
	// templates; these thresholds must keep matching the previous klog-based
	// logger, where Warning was V(1), Info V(2), Debug V(3) and Trace V(4).
	tests := []struct {
		verbosity Verbosity
		want      log.Level
	}{
		{verbosity: ErrorLevel, want: log.LevelError},
		{verbosity: WarningLevel, want: log.LevelWarn},
		{verbosity: InfoLevel, want: log.LevelInfo},
		{verbosity: DebugLevel, want: log.LevelDebug},
		{verbosity: TraceLevel, want: log.LevelTrace},
		// Anything past TRACE stays at TRACE, as before.
		{verbosity: Verbosity("9"), want: log.LevelTrace},
	}

	for _, tt := range tests {
		t.Run(string(tt.verbosity), func(t *testing.T) {
			got, err := tt.verbosity.Level()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVerbosityRejectsInvalidValues(t *testing.T) {
	for _, v := range []Verbosity{"", "abc", "-1", "2.5"} {
		t.Run(string(v), func(t *testing.T) {
			_, err := v.Level()
			assert.Error(t, err)

			_, err = NewLogger(v)
			assert.Error(t, err)
		})
	}
}

// TestLevelThresholds pins which methods emit at each configured verbosity.
func TestLevelThresholds(t *testing.T) {
	tests := []struct {
		level   Verbosity
		emitted []string
		dropped []string
	}{
		{
			level:   ErrorLevel,
			emitted: []string{"an-error"},
			dropped: []string{"a-warning", "an-info", "a-debug", "a-trace"},
		},
		{
			level:   WarningLevel,
			emitted: []string{"an-error", "a-warning"},
			dropped: []string{"an-info", "a-debug", "a-trace"},
		},
		{
			level:   InfoLevel,
			emitted: []string{"an-error", "a-warning", "an-info"},
			dropped: []string{"a-debug", "a-trace"},
		},
		{
			level:   DebugLevel,
			emitted: []string{"an-error", "a-warning", "an-info", "a-debug"},
			dropped: []string{"a-trace"},
		},
		{
			level:   TraceLevel,
			emitted: []string{"an-error", "a-warning", "an-info", "a-debug", "a-trace"},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			l, buf := newBufLogger(t, tt.level)

			l.Error(errors.New("boom"), "an-error")
			l.Warning("a-warning")
			l.Info("an-info")
			l.Debug("a-debug")
			l.Trace("a-trace")

			out := buf.String()
			for _, msg := range tt.emitted {
				assert.Contains(t, out, msg, "%s should be emitted at %s", msg, tt.level)
			}
			for _, msg := range tt.dropped {
				assert.NotContains(t, out, msg, "%s should be dropped at %s", msg, tt.level)
			}
		})
	}
}

func TestErrorAttachesErrorAndStackTrace(t *testing.T) {
	l, buf := newBufLogger(t, TraceLevel)

	l.Error(errors.New("disk on fire"), "unable to create volume", "volumeID", "pvc-123")

	out := buf.String()
	assert.Contains(t, out, "unable to create volume")
	// The error travels as a structured field rather than being interpolated
	// into the message.
	assert.Contains(t, out, "disk on fire")
	assert.Contains(t, out, "pvc-123")
	// A stack trace is what the previous logger did not provide.
	assert.Contains(t, out, "logger.TestErrorAttachesErrorAndStackTrace")
}

func TestErrorToleratesNilError(t *testing.T) {
	l, buf := newBufLogger(t, TraceLevel)

	// Call sites pass whatever they hold; a nil error must not panic.
	assert.NotPanics(t, func() {
		l.Error(nil, "something went wrong")
	})
	assert.Contains(t, buf.String(), "something went wrong")
}

func TestNamedBuildsDottedPath(t *testing.T) {
	l, buf := newBufLogger(t, TraceLevel)

	l.Named("local-storage-class-controller").Named("reconcile").Info("starting")

	// This is the replacement for hand-written "[Component]" message prefixes.
	assert.Contains(t, buf.String(), "local-storage-class-controller.reconcile")
}

func TestNamedDoesNotAffectTheParent(t *testing.T) {
	l, buf := newBufLogger(t, TraceLevel)

	child := l.Named("child")
	child.Info("from-child")
	l.Info("from-parent")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], "child")
	assert.NotContains(t, lines[1], "child")
}

func TestWithAttachesFieldsToEveryRecord(t *testing.T) {
	l, buf := newBufLogger(t, TraceLevel)

	scoped := l.With("traceID", "abc-123", "volumeID", "pvc-1")
	scoped.Info("first")
	scoped.Trace("second")

	out := buf.String()
	assert.Equal(t, 2, strings.Count(out, "abc-123"))
	assert.Equal(t, 2, strings.Count(out, "pvc-1"))
}

func TestSetLevelChangesLevelInPlace(t *testing.T) {
	l, buf := newBufLogger(t, ErrorLevel)

	l.Debug("before")
	require.NotContains(t, buf.String(), "before")

	// Changing the level must affect the live logger, which is what lets the
	// level be raised without restarting the pod.
	require.NoError(t, l.SetLevel(DebugLevel))
	l.Debug("after")
	assert.Contains(t, buf.String(), "after")
}

func TestSetLevelPropagatesToDerivedLoggers(t *testing.T) {
	l, buf := newBufLogger(t, ErrorLevel)
	child := l.Named("child").With("k", "v")

	child.Debug("before")
	require.NotContains(t, buf.String(), "before")

	require.NoError(t, l.SetLevel(DebugLevel))

	child.Debug("after")
	assert.Contains(t, buf.String(), "after")
}

func TestSetLevelRejectsInvalidValue(t *testing.T) {
	l, _ := newBufLogger(t, InfoLevel)
	assert.Error(t, l.SetLevel(Verbosity("nonsense")))
}

func TestNopDiscardsEverything(t *testing.T) {
	l := NewNop()

	assert.NotPanics(t, func() {
		l.Error(errors.New("boom"), "an-error")
		l.Info("an-info")
		l.Trace("a-trace")
		l.Named("x").With("k", "v").Debug("a-debug")
	})
}

// TestZeroLoggerIsANoOp guards the behaviour the previous logr-backed
// implementation had: tests construct logger.Logger{} directly and must not
// crash on it.
func TestZeroLoggerIsANoOp(t *testing.T) {
	var l Logger

	assert.NotPanics(t, func() {
		l.Error(errors.New("boom"), "an-error")
		l.Warning("a-warning")
		l.Info("an-info")
		l.Debug("a-debug")
		l.Trace("a-trace")
		l.Named("x").With("k", "v").Info("nested")
		_ = l.GetLogger()
		_ = l.GetSlogLogger()
		_ = l.SetLevel(InfoLevel)
	})
}

// TestSourceNamesTheCallSite guards against the wrapper attributing every record
// to itself. Without the manual record construction in emit, "source" would read
// logger/logger.go for all 300-odd call sites in the module.
func TestSourceNamesTheCallSite(t *testing.T) {
	tests := []struct {
		name string
		emit func(l *Logger)
	}{
		{name: "Warning", emit: func(l *Logger) { l.Warning("m") }},
		{name: "Info", emit: func(l *Logger) { l.Info("m") }},
		{name: "Debug", emit: func(l *Logger) { l.Debug("m") }},
		{name: "Trace", emit: func(l *Logger) { l.Trace("m") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, buf := newBufLogger(t, TraceLevel)

			tt.emit(l)

			out := buf.String()
			assert.Contains(t, out, "logger_test.go")
			assert.NotContains(t, out, "logger/logger.go")
		})
	}
}

// TestErrorReportsStackTraceInsteadOfSource documents the one exception: the
// handler drops "source" when a stack trace is attached, so Error does not need
// caller fix-up.
func TestErrorReportsStackTraceInsteadOfSource(t *testing.T) {
	l, buf := newBufLogger(t, TraceLevel)

	l.Error(errors.New("boom"), "failed")

	out := buf.String()
	assert.NotContains(t, out, `"source"`)
	assert.Contains(t, out, "stacktrace")
}

func TestGetLoggerBridgesToLogr(t *testing.T) {
	l, buf := newBufLogger(t, TraceLevel)

	// controller-runtime takes a logr.Logger; records written through it must
	// land in the same output with the same level handling.
	l.GetLogger().Info("from-logr", "k", "v")

	out := buf.String()
	assert.Contains(t, out, "from-logr")
	assert.Contains(t, out, "\"k\":\"v\"")
}
