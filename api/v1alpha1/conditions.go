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

package v1alpha1

// Condition types published in status.conditions.
//
// These follow the Kubernetes API conventions for typical status properties:
// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties
const (
	// ConditionTypeReady reports whether the controller has fully reconciled
	// the resource:
	//
	//   - True  the StorageClass is in place and its LVMVolumeGroups resolve;
	//   - False a reconcile pass failed (reason ReconcileFailed) or the
	//           resource is being torn down (reason Deleting); the detail is in
	//           the condition message.
	//
	// Until the controller's first pass over a resource the condition is
	// absent, not Unknown. Those are different answers and the controller never
	// writes the second one, so a consumer waiting on Ready has to treat a
	// missing condition as "no verdict yet" — which is what conditions.IsTrue
	// and conditions.IsStale already do.
	//
	// It matches the aggregate Ready condition used across the storage
	// modules; the shared helpers live in
	// github.com/deckhouse/sds-common-lib/conditions.
	ConditionTypeReady = "Ready"
)

// LocalStorageClassConditionTypes is every condition type a LocalStorageClass
// publishes.
//
// A condition type that is declared but never written is worse than one that
// does not exist: an absent condition is indistinguishable from "not yet
// evaluated", so an operator waits for a verdict that never comes and an alert
// on it never fires. Keeping the set in one list is what lets a test hold the
// controller to it.
var LocalStorageClassConditionTypes = []string{
	ConditionTypeReady,
}
