package scheduler

import (
	"math"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestPrioritize(t *testing.T) {
	t.Run("getFreeSpaceLeftPercent", func(t *testing.T) {
		requested := resource.MustParse("1Gi")
		devisor := 1.0

		totalSizeString := "327676Mi"
		totalSize := resource.MustParse(totalSizeString)
		allocated := resource.MustParse("211Gi")
		freeSize := resource.MustParse(totalSizeString)
		freeSize.Sub(allocated)
		// t.Logf("freeSize=%s, requested=%s, totalSize=%s", freeSize.String(), requested.String(), totalSize.String())
		// t.Logf("freeSize=%d, requested=%d, totalSize=%d", freeSize.Value(), requested.Value(), totalSize.Value())

		percent := getFreeSpaceLeftPercent(freeSize.Value(), requested.Value(), totalSize.Value())
		t.Logf("First freeSpacePercent %d", percent)

		rawScore := int(math.Round(math.Log2(float64(percent) / devisor)))
		t.Logf("rawScore1=%d", rawScore)

		totalSizeString2 := "327676Mi"
		totalSize2 := resource.MustParse(totalSizeString2)
		allocated2 := resource.MustParse("301Gi")
		freeSize2 := resource.MustParse(totalSizeString2)
		freeSize2.Sub(allocated2)

		percent2 := getFreeSpaceLeftPercent(freeSize2.Value(), requested.Value(), totalSize2.Value())
		t.Logf("Second freeSpacePercent2 %d", percent2)

		rawScore2 := int(math.Round(math.Log2(float64(percent2) / devisor)))
		t.Logf("rawScore2=%d", rawScore2)
	})

	t.Run("getNodeScore", func(t *testing.T) {

	})
}
