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

// Package logger wraps github.com/deckhouse/deckhouse/pkg/log in the small API
// this module uses.
//
// The method set (Error/Warning/Info/Debug/Trace) and the Verbosity type match
// the controller and CSI copies of this package, so that all three components of
// the module log in one format and take the same numeric 0..4 level.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	"github.com/deckhouse/deckhouse/pkg/log"
)

// Verbosity is the numeric LOG_LEVEL value taken from the environment.
const (
	ErrorLevel   Verbosity = "0"
	WarningLevel Verbosity = "1"
	InfoLevel    Verbosity = "2"
	DebugLevel   Verbosity = "3"
	TraceLevel   Verbosity = "4"
)

type (
	Verbosity string
)

// Level maps a numeric verbosity onto a log level.
//
// The mapping reproduces the thresholds of the previous klog-based logger, where
// Warning was V(1), Info V(2), Debug V(3) and Trace V(4): a given LOG_LEVEL
// keeps emitting exactly the same set of messages as before.
func (v Verbosity) Level() (log.Level, error) {
	n, err := strconv.Atoi(string(v))
	if err != nil {
		return log.LevelInfo, fmt.Errorf("parse verbosity %q: %w", string(v), err)
	}

	switch n {
	case 0:
		return log.LevelError, nil
	case 1:
		return log.LevelWarn, nil
	case 2:
		return log.LevelInfo, nil
	case 3:
		return log.LevelDebug, nil
	default:
		// Anything above TRACE stays at TRACE rather than being rejected; the
		// previous implementation also accepted higher numbers.
		if n < 0 {
			return log.LevelInfo, fmt.Errorf("verbosity %d is negative", n)
		}
		return log.LevelTrace, nil
	}
}

type Logger struct {
	log *log.Logger
}

// nop backs the zero Logger. The previous logr-based implementation held a
// logr.Logger by value, whose zero value discards silently, and callers rely on
// that: a zero Logger must stay a no-op rather than panic.
var nop = log.NewNop()

// unwrap returns the underlying logger, substituting the discard logger for a
// zero Logger.
func (l Logger) unwrap() *log.Logger {
	if l.log == nil {
		return nop
	}
	return l.log
}

// NewLogger returns a logger emitting at the given verbosity.
func NewLogger(level Verbosity) (*Logger, error) {
	lvl, err := level.Level()
	if err != nil {
		return nil, err
	}

	return &Logger{log: log.NewLogger(log.WithLevel(lvl.Level()))}, nil
}

// NewLoggerToWriter returns a logger writing to w, for tests that want the
// output attached to their own reporter.
func NewLoggerToWriter(w io.Writer, level Verbosity) (*Logger, error) {
	lvl, err := level.Level()
	if err != nil {
		return nil, err
	}

	return &Logger{log: log.NewLogger(log.WithLevel(lvl.Level()), log.WithOutput(w))}, nil
}

// NewNop returns a logger that discards everything, for tests that do not care
// about log output.
func NewNop() *Logger {
	return &Logger{log: log.NewNop()}
}

// NewLoggerFromSlog adapts an existing pkg/log logger, so that a caller which
// already built one does not have to reconfigure it.
func NewLoggerFromSlog(l *log.Logger) Logger {
	return Logger{log: l}
}

// Named appends name to the logger's dotted trace path, e.g. a logger named
// "local-storage-class-controller" then named "reconcile" reports
// "local-storage-class-controller.reconcile". Use it instead of hand-writing a
// "[Component]" prefix into the message.
func (l Logger) Named(name string) Logger {
	return Logger{log: l.unwrap().Named(name)}
}

// With returns a logger that attaches the given key/value pairs to every
// record. Use it for values that are constant for a unit of work, such as a
// traceID or a volumeID, instead of interpolating them into each message.
func (l Logger) With(args ...any) Logger {
	return Logger{log: l.unwrap().With(args...)}
}

// SetLevel changes the emitted level in place, on every logger derived from this
// one, without recreating it.
func (l Logger) SetLevel(level Verbosity) error {
	lvl, err := level.Level()
	if err != nil {
		return err
	}

	l.unwrap().SetLevel(lvl)
	return nil
}

// GetSlogLogger returns the underlying logger, for the few APIs that take one
// directly.
func (l Logger) GetSlogLogger() *log.Logger {
	return l.unwrap()
}

// GetLogger returns a logr view of this logger, for libraries that accept only
// logr — controller-runtime's manager.Options.Logger in particular.
func (l Logger) GetLogger() logr.Logger {
	return logr.FromSlogHandler(l.unwrap().Handler())
}

// Error logs at error level. The error is attached as a structured field via
// log.Err, and the underlying logger appends a full stack trace.
//
// This one delegates to the library instead of going through emit, because the
// stack trace is injected by log.Logger.Error itself. The trace supersedes the
// "source" field, which the handler drops when a trace is present, so nothing is
// lost by not fixing up the caller frame here.
func (l Logger) Error(err error, message string, keysAndValues ...interface{}) {
	l.unwrap().Error(message, append([]any{log.Err(err)}, keysAndValues...)...)
}

func (l Logger) Warning(message string, keysAndValues ...interface{}) {
	l.emit(log.LevelWarn, message, keysAndValues)
}

func (l Logger) Info(message string, keysAndValues ...interface{}) {
	l.emit(log.LevelInfo, message, keysAndValues)
}

func (l Logger) Debug(message string, keysAndValues ...interface{}) {
	l.emit(log.LevelDebug, message, keysAndValues)
}

// Trace logs at the trace level, which plain slog does not provide.
func (l Logger) Trace(message string, keysAndValues ...interface{}) {
	l.emit(log.LevelTrace, message, keysAndValues)
}

// emit builds the record by hand so that the "source" field names the call site
// rather than this file.
//
// Calling log.Logger.Info and friends directly would make slog capture the
// program counter of the wrapper, so every line would report
// logger/logger.go. The previous klog-based logger avoided that with
// WithCallDepth(1); this is the slog equivalent.
func (l Logger) emit(level log.Level, message string, keysAndValues []any) {
	lg := l.unwrap()
	ctx := context.Background()
	slevel := slog.Level(level)

	if !lg.Enabled(ctx, slevel) {
		return
	}

	// Skip runtime.Callers, emit itself, and the exported method that called it.
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])

	record := slog.NewRecord(time.Now(), slevel, message, pcs[0])
	record.Add(keysAndValues...)

	// The error is not actionable: the handler only fails if the record cannot be
	// serialised, and there is nowhere left to report that.
	_ = lg.Handler().Handle(ctx, record)
}
