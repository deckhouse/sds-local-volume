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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
	"github.com/deckhouse/sds-local-volume/images/controller/pkg/monitoring"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
)

// lscLVGSeries scrapes the LocalStorageClassLVGInfo collector reading cl, as an
// elected replica, and returns its series as storage_class/lvg_name/thin_pool.
func lscLVGSeries(t *testing.T, cl client.Reader) []string {
	t.Helper()

	elected := make(chan struct{})
	close(elected)

	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(monitoring.NewLocalStorageClassLVGCollector(
		LocalStorageClassLVGs(cl),
		elected,
		func(err error) { t.Errorf("unexpected listing error: %v", err) },
	)))

	families, err := reg.Gather()
	require.NoError(t, err)

	var series []string
	for _, f := range families {
		require.Equal(t, monitoring.LocalStorageClassLVGInfo, f.GetName())
		for _, m := range f.GetMetric() {
			require.Equal(t, 1.0, m.GetGauge().GetValue())
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			series = append(series, labels[monitoring.LabelStorageClass]+"/"+labels[monitoring.LabelLVGName]+"/"+labels[monitoring.LabelThinPool])
		}
	}
	return series
}

func newLSCLVGClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, slv.AddToScheme(s))
	require.NoError(t, snc.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func newTestLSC(name, lvmType string, lvgs ...slv.LocalStorageClassLVG) *slv.LocalStorageClass {
	return &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: slv.LocalStorageClassSpec{
			LVM: &slv.LocalStorageClassLVMSpec{Type: lvmType, LVMVolumeGroups: lvgs},
		},
	}
}

func thinEntry(lvg, pool string) slv.LocalStorageClassLVG {
	return slv.LocalStorageClassLVG{Name: lvg, Thin: &slv.LocalStorageClassLVMThinPoolSpec{PoolName: pool}}
}

func newLabeledLVG(name string, labels map[string]string) *snc.LVMVolumeGroup {
	return &snc.LVMVolumeGroup{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestLocalStorageClassLVGInfoThick(t *testing.T) {
	cl := newLSCLVGClient(t, newTestLSC("thick", LVMThickType,
		slv.LocalStorageClassLVG{Name: "vg-on-node-1"},
		slv.LocalStorageClassLVG{Name: "vg-on-node-2"},
	))

	assert.ElementsMatch(t, []string{
		"thick/vg-on-node-1/",
		"thick/vg-on-node-2/",
	}, lscLVGSeries(t, cl))
}

func TestLocalStorageClassLVGInfoThin(t *testing.T) {
	cl := newLSCLVGClient(t, newTestLSC("thin", LVMThinType,
		thinEntry("vg-on-node-1", "pool-a"),
		thinEntry("vg-on-node-2", "pool-b"),
	))

	assert.ElementsMatch(t, []string{
		"thin/vg-on-node-1/pool-a",
		"thin/vg-on-node-2/pool-b",
	}, lscLVGSeries(t, cl))
}

// The same LVMVolumeGroup used by two classes is two series, told apart by the
// class: a query has to pick the class before it joins.
func TestLocalStorageClassLVGInfoSharedLVG(t *testing.T) {
	cl := newLSCLVGClient(t,
		newTestLSC("thick", LVMThickType, slv.LocalStorageClassLVG{Name: "vg"}),
		newTestLSC("thin", LVMThinType, thinEntry("vg", "pool")),
	)

	assert.ElementsMatch(t, []string{
		"thick/vg/",
		"thin/vg/pool",
	}, lscLVGSeries(t, cl))
}

func TestLocalStorageClassLVGInfoFollowsLabelSelector(t *testing.T) {
	selected := map[string]string{"pool": "local"}
	cl := newLSCLVGClient(t,
		newLabeledLVG("vg-a", selected),
		newLabeledLVG("vg-b", selected),
		newLabeledLVG("vg-other", nil),
		newTestLSC("selected", LVMThinType, slv.LocalStorageClassLVG{
			LabelSelector: &metav1.LabelSelector{MatchLabels: selected},
			Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "pool"},
		}),
	)

	assert.ElementsMatch(t, []string{
		"selected/vg-a/pool",
		"selected/vg-b/pool",
	}, lscLVGSeries(t, cl))

	lvg := &snc.LVMVolumeGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "vg-b"}, lvg))
	lvg.Labels = nil
	require.NoError(t, cl.Update(context.Background(), lvg))

	assert.ElementsMatch(t, []string{"selected/vg-a/pool"}, lscLVGSeries(t, cl))
}

func TestLocalStorageClassLVGInfoDropsARemovedLVG(t *testing.T) {
	lsc := newTestLSC("thick", LVMThickType,
		slv.LocalStorageClassLVG{Name: "vg-on-node-1"},
		slv.LocalStorageClassLVG{Name: "vg-on-node-2"},
	)
	cl := newLSCLVGClient(t, lsc)
	require.Len(t, lscLVGSeries(t, cl), 2)

	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(lsc), lsc))
	lsc.Spec.LVM.LVMVolumeGroups = lsc.Spec.LVM.LVMVolumeGroups[:1]
	require.NoError(t, cl.Update(context.Background(), lsc))

	assert.ElementsMatch(t, []string{"thick/vg-on-node-1/"}, lscLVGSeries(t, cl))
}

func TestLocalStorageClassLVGInfoDropsADeletedClass(t *testing.T) {
	gone := newTestLSC("gone", LVMThickType, slv.LocalStorageClassLVG{Name: "vg-1"})
	cl := newLSCLVGClient(t, gone, newTestLSC("kept", LVMThinType, thinEntry("vg-2", "pool")))
	require.Len(t, lscLVGSeries(t, cl), 2)

	require.NoError(t, cl.Delete(context.Background(), gone))

	assert.ElementsMatch(t, []string{"kept/vg-2/pool"}, lscLVGSeries(t, cl))
}

// A class whose selectors disagree on the thin pool resolves to nothing; the
// others are still exported.
func TestLocalStorageClassLVGInfoSkipsAnUnresolvableClass(t *testing.T) {
	cl := newLSCLVGClient(t,
		newLabeledLVG("vg", map[string]string{"a": "1", "b": "1"}),
		newTestLSC("ambiguous", LVMThinType,
			slv.LocalStorageClassLVG{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"a": "1"}},
				Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "pool-a"},
			},
			slv.LocalStorageClassLVG{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"b": "1"}},
				Thin:          &slv.LocalStorageClassLVMThinPoolSpec{PoolName: "pool-b"},
			},
		),
		newTestLSC("thick", LVMThickType, slv.LocalStorageClassLVG{Name: "vg"}),
	)

	assert.ElementsMatch(t, []string{"thick/vg/"}, lscLVGSeries(t, cl))
}
