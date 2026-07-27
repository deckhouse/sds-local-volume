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

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestAlignSizeToExtent(t *testing.T) {
	extent := resource.MustParse("4Mi")

	t.Run("rounds_up_unaligned_size_to_the_next_extent", func(t *testing.T) {
		// 35Mi is a whole number of MiB but not a multiple of the 4Mi extent,
		// so it must round up to 36Mi — this is exactly the drift the plain
		// create path used to leave in Spec.Size and the reported capacity.
		aligned, err := AlignSizeToExtent(resource.MustParse("35Mi"), extent)
		assert.NoError(t, err)
		expected := resource.MustParse("36Mi")
		assert.Equal(t, expected.Value(), aligned.Value())
	})

	t.Run("leaves_already_aligned_size_unchanged", func(t *testing.T) {
		aligned, err := AlignSizeToExtent(resource.MustParse("52Mi"), extent)
		assert.NoError(t, err)
		expected := resource.MustParse("52Mi")
		assert.Equal(t, expected.Value(), aligned.Value())
	})

	t.Run("zero_size_stays_zero", func(t *testing.T) {
		aligned, err := AlignSizeToExtent(resource.MustParse("0"), extent)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), aligned.Value())
	})

	t.Run("returns_error_for_non_positive_extent", func(t *testing.T) {
		_, err := AlignSizeToExtent(resource.MustParse("35Mi"), resource.MustParse("0"))
		assert.Error(t, err)
	})
}

func TestSafeExtentSize(t *testing.T) {
	t.Run("returns_extent_when_positive", func(t *testing.T) {
		expected := resource.MustParse("8Mi")
		actual := SafeExtentSize(resource.MustParse("8Mi"))
		assert.Equal(t, expected.Value(), actual.Value())
	})

	t.Run("falls_back_to_4Mi_when_zero", func(t *testing.T) {
		expected := resource.MustParse("4Mi")
		actual := SafeExtentSize(resource.MustParse("0"))
		assert.Equal(t, expected.Value(), actual.Value())
	})
}
