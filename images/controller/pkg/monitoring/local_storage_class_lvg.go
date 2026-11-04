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
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// LocalStorageClassLVG is one series of LocalStorageClassLVGInfo: an
// LVMVolumeGroup a LocalStorageClass provisions on and, for a Thin class, the
// thin pool in it.
type LocalStorageClassLVG struct {
	StorageClass   string
	LVMVolumeGroup string
	ThinPool       string
}

// listTimeout bounds a single listing. The listing is expected to read an
// informer cache that is already in sync, so it only matters if it is not.
const listTimeout = 5 * time.Second

var localStorageClassLVGInfoDesc = prometheus.NewDesc(
	LocalStorageClassLVGInfo,
	"1 for every LVMVolumeGroup, and for a Thin class every thin pool, that a LocalStorageClass provisions on. Join it on lvg_name (and thin_pool) with the sds_node_configurator_lvg_* series to read the capacity of a class.",
	[]string{LabelStorageClass, LabelLVGName, LabelThinPool},
	nil,
)

type localStorageClassLVGCollector struct {
	list    func(context.Context) ([]LocalStorageClassLVG, error)
	elected <-chan struct{}
	onError func(error)
}

// NewLocalStorageClassLVGCollector returns the collector of
// LocalStorageClassLVGInfo.
//
// The series are built from list on every scrape rather than recorded by the
// reconciler, so they follow the classes as they are now: an LVMVolumeGroup
// taken out of a class, or matched by its selector no more, and a class that has
// been deleted leave no series behind, without anything having to expire them.
//
// Nothing is exported until elected is closed. Every replica of the controller
// is scraped, and a duplicate from a standby replica would turn every join with
// the series into a many-to-many one.
//
// A failed listing is passed to onError and exports nothing for that scrape. An
// invalid metric would instead fail the whole scrape, taking every other metric
// of the controller with it: the metrics endpoint answers an error to any
// collector error.
func NewLocalStorageClassLVGCollector(
	list func(context.Context) ([]LocalStorageClassLVG, error),
	elected <-chan struct{},
	onError func(error),
) prometheus.Collector {
	return &localStorageClassLVGCollector{list: list, elected: elected, onError: onError}
}

func (c *localStorageClassLVGCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- localStorageClassLVGInfoDesc
}

func (c *localStorageClassLVGCollector) Collect(ch chan<- prometheus.Metric) {
	select {
	case <-c.elected:
	default:
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()

	entries, err := c.list(ctx)
	if err != nil {
		if c.onError != nil {
			c.onError(err)
		}
		return
	}

	// A repeated series fails the whole scrape just as an invalid one would.
	seen := make(map[LocalStorageClassLVG]struct{}, len(entries))
	for _, e := range entries {
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		ch <- prometheus.MustNewConstMetric(localStorageClassLVGInfoDesc, prometheus.GaugeValue, 1, e.StorageClass, e.LVMVolumeGroup, e.ThinPool)
	}
}
