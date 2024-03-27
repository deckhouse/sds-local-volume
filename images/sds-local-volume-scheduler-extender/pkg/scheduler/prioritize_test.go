package scheduler

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"math"
	"testing"
)

func TestPrioritize(t *testing.T) {
	t.Run("getFreeSpaceLeftPercent", func(t *testing.T) {
		size := resource.MustParse("20476Mi")
		allocated := resource.MustParse("15392Mi")
		requested := resource.MustParse("700Mi")
		size.Sub(allocated)
		percent := getFreeSpaceLeftPercent(size.Value(), requested.Value())
		t.Log(percent)

		devisor := 1.0
		converted := int(math.Round(math.Log2(float64(percent) / devisor)))
		t.Log(converted)

		size2 := resource.MustParse("122876Mi")
		allocated2 := resource.MustParse("52004Mi")
		size2.Sub(allocated2)
		percent2 := getFreeSpaceLeftPercent(size2.Value(), requested.Value())
		t.Log(percent2)

		converted = int(math.Round(math.Log2(float64(percent2) / devisor)))
		t.Log(converted)
	})

	t.Run("getNodeScore", func(t *testing.T) {

	})
}
