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
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatherLSCLVG registers a collector over entries (or listErr) on a pedantic
// registry, scrapes it once and returns the series as
// storage_class/lvg_name/thin_pool together with the errors passed to onError.
func gatherLSCLVG(t *testing.T, elected <-chan struct{}, entries []LocalStorageClassLVG, listErr error) ([]string, []error) {
	t.Helper()

	var reported []error
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(NewLocalStorageClassLVGCollector(
		func(context.Context) ([]LocalStorageClassLVG, error) { return entries, listErr },
		elected,
		func(err error) { reported = append(reported, err) },
	)))

	families, err := reg.Gather()
	require.NoError(t, err)

	var series []string
	for _, f := range families {
		for _, m := range f.GetMetric() {
			assert.Equal(t, LocalStorageClassLVGInfo, f.GetName())
			assert.Equal(t, 1.0, m.GetGauge().GetValue())
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			series = append(series, labels[LabelStorageClass]+"/"+labels[LabelLVGName]+"/"+labels[LabelThinPool])
		}
	}
	return series, reported
}

func closedChan() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestLocalStorageClassLVGCollectorExportsEveryEntry(t *testing.T) {
	series, reported := gatherLSCLVG(t, closedChan(), []LocalStorageClassLVG{
		{StorageClass: "thick", LVMVolumeGroup: "vg-1"},
		{StorageClass: "thin", LVMVolumeGroup: "vg-1", ThinPool: "pool"},
	}, nil)

	assert.Empty(t, reported)
	assert.ElementsMatch(t, []string{"thick/vg-1/", "thin/vg-1/pool"}, series)
}

func TestLocalStorageClassLVGCollectorIsSilentUntilElected(t *testing.T) {
	series, reported := gatherLSCLVG(t, make(chan struct{}), []LocalStorageClassLVG{
		{StorageClass: "thick", LVMVolumeGroup: "vg-1"},
	}, nil)

	assert.Empty(t, reported)
	assert.Empty(t, series)
}

// A repeated entry must not reach the registry: a duplicate series fails the
// whole scrape.
func TestLocalStorageClassLVGCollectorDropsRepeatedEntries(t *testing.T) {
	series, _ := gatherLSCLVG(t, closedChan(), []LocalStorageClassLVG{
		{StorageClass: "thick", LVMVolumeGroup: "vg-1"},
		{StorageClass: "thick", LVMVolumeGroup: "vg-1"},
	}, nil)

	assert.Equal(t, []string{"thick/vg-1/"}, series)
}

// A failed listing is reported and leaves the scrape itself successful.
func TestLocalStorageClassLVGCollectorReportsAListingError(t *testing.T) {
	listErr := errors.New("cache not synced")
	series, reported := gatherLSCLVG(t, closedChan(), nil, listErr)

	assert.Empty(t, series)
	require.Len(t, reported, 1)
	assert.ErrorIs(t, reported[0], listErr)
}
