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
	"fmt"

	kwhlog "github.com/slok/kubewebhook/v2/pkg/log"
)

// kubewebhookAdapter satisfies kubewebhook's logger interface on top of Logger.
//
// kubewebhook ships an adapter for logrus only, which is the sole reason logrus
// used to be a dependency of this component. Its interface is printf-style, so
// messages coming from the library arrive pre-formatted; the structured values it
// passes through WithValues are preserved as fields.
type kubewebhookAdapter struct {
	log    Logger
	values map[string]interface{}
}

var _ kwhlog.Logger = kubewebhookAdapter{}

// NewKubewebhookLogger adapts log for use as kubewebhook's logger.
func NewKubewebhookLogger(log Logger) kwhlog.Logger {
	return kubewebhookAdapter{log: log.Named("kubewebhook")}
}

func (a kubewebhookAdapter) Infof(format string, args ...interface{}) {
	a.log.Info(fmt.Sprintf(format, args...), a.fields()...)
}

func (a kubewebhookAdapter) Warningf(format string, args ...interface{}) {
	a.log.Warning(fmt.Sprintf(format, args...), a.fields()...)
}

// Errorf logs at error level. kubewebhook hands over a formatted string rather
// than an error value, so there is nothing to pass to Logger.Error's error
// parameter.
func (a kubewebhookAdapter) Errorf(format string, args ...interface{}) {
	a.log.Error(nil, fmt.Sprintf(format, args...), a.fields()...)
}

func (a kubewebhookAdapter) Debugf(format string, args ...interface{}) {
	a.log.Debug(fmt.Sprintf(format, args...), a.fields()...)
}

func (a kubewebhookAdapter) WithValues(values map[string]interface{}) kwhlog.Logger {
	if len(values) == 0 {
		return a
	}

	merged := make(map[string]interface{}, len(a.values)+len(values))
	for k, v := range a.values {
		merged[k] = v
	}
	for k, v := range values {
		merged[k] = v
	}

	return kubewebhookAdapter{log: a.log, values: merged}
}

func (a kubewebhookAdapter) WithCtxValues(ctx context.Context) kwhlog.Logger {
	return a.WithValues(kwhlog.ValuesFromCtx(ctx))
}

func (a kubewebhookAdapter) SetValuesOnCtx(parent context.Context, values map[string]interface{}) context.Context {
	return kwhlog.CtxWithValues(parent, values)
}

// fields flattens the accumulated values into the key/value form the module
// logger takes.
func (a kubewebhookAdapter) fields() []interface{} {
	if len(a.values) == 0 {
		return nil
	}

	out := make([]interface{}, 0, len(a.values)*2)
	for k, v := range a.values {
		out = append(out, k, v)
	}
	return out
}
