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

package utils

import (
	"math"
	"math/big"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func FuzzAlignSizeToExtent(f *testing.F) {
	for _, seed := range [][2]int64{
		{35 << 20, 4 << 20},
		{52 << 20, 4 << 20},
		{0, 4 << 20},
		{1, 4 << 20},
		{1 << 30, 32 << 20},
		{10 << 30, 1},
		{4 << 20, 0},
		{4 << 20, -(4 << 20)},
		{math.MaxInt64, 1},
		{math.MaxInt64 - 3, 4},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, size, extent int64) {
		if size < 0 {
			return
		}
		aligned, err := AlignSizeToExtent(*resource.NewQuantity(size, resource.BinarySI), *resource.NewQuantity(extent, resource.BinarySI))
		if extent <= 0 {
			if err == nil {
				t.Fatalf("AlignSizeToExtent(%d, %d) = %s, want an error for an extent that is not positive", size, extent, aligned.String())
			}
			return
		}

		e := big.NewInt(extent)
		want := new(big.Int).Add(big.NewInt(size), new(big.Int).Sub(e, big.NewInt(1)))
		want.Div(want, e).Mul(want, e)
		if !want.IsInt64() {
			if err == nil {
				t.Fatalf("AlignSizeToExtent(%d, %d) = %s, but %s bytes do not fit into an int64", size, extent, aligned.String(), want)
			}
			return
		}
		if err != nil {
			t.Fatalf("AlignSizeToExtent(%d, %d): %v", size, extent, err)
		}
		if aligned.Value() != want.Int64() {
			t.Fatalf("AlignSizeToExtent(%d, %d) = %d, want the next multiple of the extent %d", size, extent, aligned.Value(), want.Int64())
		}

		again, err := AlignSizeToExtent(aligned, *resource.NewQuantity(extent, resource.BinarySI))
		if err != nil || again.Value() != aligned.Value() {
			t.Fatalf("aligning %d again to %d gives %s, %v", aligned.Value(), extent, again.String(), err)
		}
	})
}
