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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/deckhouse/sds-common-lib/conditions"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
)

type eventRecorderKey struct{}

// withEventRecorder returns ctx carrying rec, the recorder that
// publishLocalStorageClassStatus records Ready transitions with.
//
// The recorder rides on the context rather than in a parameter because the
// status writer is reached from some twenty call sites through every reconcile
// function of the LocalStorageClass controller, and only that writer uses it.
// The reconciler sets it once per pass, the way controller-runtime hands down
// its logger.
func withEventRecorder(ctx context.Context, rec conditions.EventRecorder) context.Context {
	return context.WithValue(ctx, eventRecorderKey{}, rec)
}

// eventRecorderFrom returns the recorder withEventRecorder put on ctx, or nil,
// which conditions.WithEvents takes as "record nothing".
func eventRecorderFrom(ctx context.Context) conditions.EventRecorder {
	rec, _ := ctx.Value(eventRecorderKey{}).(conditions.EventRecorder)
	return rec
}

// localStorageClassConditions is how conditions.WithEvents reads the conditions
// of a LocalStorageClass. It runs on the object as read from the API server,
// whose status may still be nil.
func localStorageClassConditions(lsc *slv.LocalStorageClass) []metav1.Condition {
	if lsc.Status == nil {
		return nil
	}
	return lsc.Status.Conditions
}

func asNormal(rec conditions.EventRecorder) conditions.EventRecorder {
	if rec == nil {
		return nil
	}
	return normalRecorder{rec}
}

type normalRecorder struct{ rec conditions.EventRecorder }

func (n normalRecorder) Eventf(regarding, related runtime.Object, _, reason, action, note string, args ...any) {
	n.rec.Eventf(regarding, related, corev1.EventTypeNormal, reason, action, note, args...)
}
