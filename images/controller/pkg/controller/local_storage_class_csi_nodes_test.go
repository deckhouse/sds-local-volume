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
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/deckhouse/sds-common-lib/conditions"
	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

func lvgOnNode(name, node string) snc.LVMVolumeGroup {
	return snc.LVMVolumeGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: snc.LVMVolumeGroupSpec{
			Type:                  "Local",
			ActualVGNameOnTheNode: "vg",
			Local:                 snc.LVMVolumeGroupLocalSpec{NodeName: node},
		},
	}
}

func nodeWithLabels(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestLVGsOnNodesWithoutCSI(t *testing.T) {
	lvgList := &snc.LVMVolumeGroupList{Items: []snc.LVMVolumeGroup{
		lvgOnNode("lvg-b", "node-2"),
		lvgOnNode("lvg-a", "node-1"),
		lvgOnNode("lvg-c", "node-3"),
		lvgOnNode("lvg-unused", "node-9"),
	}}
	effective := []slv.LocalStorageClassLVG{{Name: "lvg-a"}, {Name: "lvg-b"}, {Name: "lvg-c"}}
	csiNodes := map[string]struct{}{"node-2": {}}

	got := lvgsOnNodesWithoutCSI(lvgList, effective, csiNodes)
	want := []string{"lvg-a (node-1)", "lvg-c (node-3)"}
	if !slices.Equal(got, want) {
		t.Fatalf("lvgsOnNodesWithoutCSI = %v, want %v", got, want)
	}

	if msg := slv.LVMVolumeGroupsOutsideCSINodesMessage(nil); msg != "" {
		t.Errorf("LVMVolumeGroupsOutsideCSINodesMessage(nil) = %q, want empty", msg)
	}
	if msg := slv.LVMVolumeGroupsOutsideCSINodesMessage(got); !strings.Contains(msg, "lvg-a (node-1), lvg-c (node-3)") {
		t.Errorf("LVMVolumeGroupsOutsideCSINodesMessage = %q, want it to name the LVMVolumeGroups", msg)
	}
}

// The node watch must fire when the CSI node starts or stops running on a node,
// as the DaemonSet selector sees it, and stay quiet otherwise.
func TestCSINodeChanged(t *testing.T) {
	csi := map[string]string{slv.CSINodeLabel: ""}
	otherValue := map[string]string{slv.CSINodeLabel: "true"}
	none := map[string]string{}

	p := csiNodeChanged()
	createDelete := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"with the label", csi, true},
		{"without the label", none, false},
		{"with the key under another value", otherValue, false},
	}
	for _, tc := range createDelete {
		t.Run("create/delete "+tc.name, func(t *testing.T) {
			node := nodeWithLabels("n", tc.labels)
			if got := p.Create(event.TypedCreateEvent[*corev1.Node]{Object: node}); got != tc.want {
				t.Errorf("Create = %v, want %v", got, tc.want)
			}
			if got := p.Delete(event.TypedDeleteEvent[*corev1.Node]{Object: node}); got != tc.want {
				t.Errorf("Delete = %v, want %v", got, tc.want)
			}
		})
	}

	statusOnly := nodeWithLabels("n", csi)
	statusOnly.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	updates := []struct {
		name     string
		old, new *corev1.Node
		want     bool
	}{
		{"label added", nodeWithLabels("n", none), nodeWithLabels("n", csi), true},
		{"label removed", nodeWithLabels("n", csi), nodeWithLabels("n", none), true},
		{"value changed away from empty", nodeWithLabels("n", csi), nodeWithLabels("n", otherValue), true},
		{"label unchanged", nodeWithLabels("n", csi), nodeWithLabels("n", csi), false},
		{"status only", nodeWithLabels("n", csi), statusOnly, false},
		{"other label changed", nodeWithLabels("n", none), nodeWithLabels("n", map[string]string{"x": "y"}), false},
	}
	for _, tc := range updates {
		t.Run("update "+tc.name, func(t *testing.T) {
			if got := p.Update(event.TypedUpdateEvent[*corev1.Node]{ObjectOld: tc.old, ObjectNew: tc.new}); got != tc.want {
				t.Errorf("Update = %v, want %v", got, tc.want)
			}
		})
	}
	if p.Generic(event.TypedGenericEvent[*corev1.Node]{Object: nodeWithLabels("n", csi)}) {
		t.Error("Generic = true, want false")
	}
}

type csiNodesFixture struct {
	t  *testing.T
	cl client.Client
}

func newCSINodesFixture(t *testing.T, hooks interceptor.Funcs, objs ...client.Object) *csiNodesFixture {
	t.Helper()
	scheme := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{slv.AddToScheme, snc.AddToScheme, storagev1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("adding scheme: %v", err)
		}
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&slv.LocalStorageClass{}).
		WithObjects(objs...).
		WithInterceptorFuncs(hooks).
		Build()
	return &csiNodesFixture{t: t, cl: cl}
}

func (f *csiNodesFixture) reconcile() (*slv.LocalStorageClass, error) {
	f.t.Helper()
	ctx := context.Background()
	current := readLSC(f.t, f.cl)
	scList := &storagev1.StorageClassList{}
	if err := f.cl.List(ctx, scList); err != nil {
		f.t.Fatalf("listing StorageClasses: %v", err)
	}
	_, err := RunEventReconcile(ctx, f.cl, testLogger(f.t), scList, current, nil)
	return readLSC(f.t, f.cl), err
}

func thickLSC(lvgs ...string) *slv.LocalStorageClass {
	entries := make([]slv.LocalStorageClassLVG, 0, len(lvgs))
	for _, name := range lvgs {
		entries = append(entries, slv.LocalStorageClassLVG{Name: name})
	}
	return &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: testLSCName},
		Spec: slv.LocalStorageClassSpec{
			ReclaimPolicy:     "Delete",
			VolumeBindingMode: "WaitForFirstConsumer",
			LVM:               &slv.LocalStorageClassLVMSpec{Type: LVMThickType, LVMVolumeGroups: entries},
		},
	}
}

func assertCreatedWithWarning(t *testing.T, got *slv.LocalStorageClass, wantInMessage string) {
	t.Helper()
	if got.Status == nil || got.Status.Phase != CreatedStatusPhase {
		t.Fatalf("status = %+v, want phase %s", got.Status, CreatedStatusPhase)
	}
	if !strings.Contains(got.Status.Reason, wantInMessage) {
		t.Errorf("status.reason = %q, want it to contain %q", got.Status.Reason, wantInMessage)
	}
	ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionTrue ||
		ready.Reason != slv.ConditionReasonLVMVolumeGroupsOutsideCSINodes || !strings.Contains(ready.Message, wantInMessage) {
		t.Errorf("Ready = %+v, want True, reason %s, the warning in the message", ready, slv.ConditionReasonLVMVolumeGroupsOutsideCSINodes)
	}
}

// A class with an LVMVolumeGroup on a node outside dataNodes.nodeSelector is
// created, says so in its status, and stops saying so once the node gets the
// CSI node, without any change to the class itself.
func TestRunEventReconcileWarnsAboutLVGsOnNodesWithoutCSI(t *testing.T) {
	node := nodeWithLabels("node-1", map[string]string{})
	lvg := lvgOnNode("lvg-a", "node-1")
	f := newCSINodesFixture(t, interceptor.Funcs{}, node, &lvg, thickLSC("lvg-a"))

	got, err := f.reconcile()
	if err != nil {
		t.Fatalf("RunEventReconcile: %v", err)
	}
	assertCreatedWithWarning(t, got, "lvg-a (node-1)")

	node.Labels[slv.CSINodeLabel] = ""
	if err := f.cl.Update(context.Background(), node); err != nil {
		t.Fatalf("labelling the node: %v", err)
	}

	got, err = f.reconcile()
	if err != nil {
		t.Fatalf("RunEventReconcile: %v", err)
	}
	if got.Status.Phase != CreatedStatusPhase || got.Status.Reason != "" {
		t.Errorf("status = phase %s reason %q, want %s with no reason", got.Status.Phase, got.Status.Reason, CreatedStatusPhase)
	}
	if ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady); ready == nil || ready.Reason != conditions.ReasonReconciled {
		t.Errorf("Ready = %+v, want reason %s", ready, conditions.ReasonReconciled)
	}
}

// The update path carries the warning too: a class extended with a group on a
// node without the CSI node reports it.
func TestRunEventReconcileUpdateWarnsAboutLVGsOnNodesWithoutCSI(t *testing.T) {
	csiNode := nodeWithLabels("node-1", map[string]string{slv.CSINodeLabel: ""})
	plainNode := nodeWithLabels("node-2", map[string]string{slv.CSINodeLabel: "true"})
	lvgA, lvgB := lvgOnNode("lvg-a", "node-1"), lvgOnNode("lvg-b", "node-2")
	f := newCSINodesFixture(t, interceptor.Funcs{}, csiNode, plainNode, &lvgA, &lvgB, thickLSC("lvg-a"))

	got, err := f.reconcile()
	if err != nil {
		t.Fatalf("RunEventReconcile: %v", err)
	}
	if got.Status.Reason != "" {
		t.Fatalf("status.reason = %q after create on a CSI node, want empty", got.Status.Reason)
	}

	got.Spec.LVM.LVMVolumeGroups = append(got.Spec.LVM.LVMVolumeGroups, slv.LocalStorageClassLVG{Name: "lvg-b"})
	if err := f.cl.Update(context.Background(), got); err != nil {
		t.Fatalf("adding lvg-b to the class: %v", err)
	}

	got, err = f.reconcile()
	if err != nil {
		t.Fatalf("RunEventReconcile: %v", err)
	}
	assertCreatedWithWarning(t, got, "lvg-b (node-2)")
}

// Failing to list the nodes only delays the warning: the pass is retried and the
// class keeps the phase it had.
func TestRunEventReconcileKeepsThePhaseWhenNodesCannotBeListed(t *testing.T) {
	failNodes := false
	hooks := interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.NodeList); ok && failNodes {
				return errors.New("nodes are unavailable")
			}
			return c.List(ctx, list, opts...)
		},
	}
	node := nodeWithLabels("node-1", map[string]string{slv.CSINodeLabel: ""})
	lvg := lvgOnNode("lvg-a", "node-1")
	f := newCSINodesFixture(t, hooks, node, &lvg, thickLSC("lvg-a"))

	if _, err := f.reconcile(); err != nil {
		t.Fatalf("RunEventReconcile: %v", err)
	}

	failNodes = true
	got, err := f.reconcile()
	if err == nil {
		t.Fatal("RunEventReconcile succeeded with the node list failing, want an error to retry on")
	}
	if got.Status.Phase != CreatedStatusPhase {
		t.Errorf("status.phase = %s, want it left at %s", got.Status.Phase, CreatedStatusPhase)
	}
	if ready := conditions.Get(got.Status.Conditions, slv.ConditionTypeReady); ready == nil || ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v, want it left True", ready)
	}
}
