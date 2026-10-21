package hami

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	componentversion "github.com/neutree-ai/neutree/internal/component"
)

func TestHAMiOfflineImages(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	imageListPath := filepath.Join(filepath.Dir(testFile), "..", "..", "..", "..",
		"scripts", "builder", "image-lists", "cluster", "kubernetes", "images.txt")
	imageList, err := os.ReadFile(imageListPath)
	require.NoError(t, err)

	hamiImagePrefix := HAMiImageRegistry + "/" + HAMiImageRepository + ":"
	schedulerImagePrefix := KubeSchedulerImageRegistry + "/" + KubeSchedulerImageRepository + ":"
	var hamiImages []string
	var schedulerImages []string

	for _, line := range strings.Split(string(imageList), "\n") {
		image := strings.TrimSpace(line)

		if strings.HasPrefix(image, hamiImagePrefix) {
			hamiImages = append(hamiImages, image)
		}
		if strings.HasPrefix(image, schedulerImagePrefix) {
			schedulerImages = append(schedulerImages, image)
		}
	}

	t.Run("hami", func(t *testing.T) {
		assert.Equal(t, "v2.10.0", componentversion.HAMi)
		assert.ElementsMatch(t, []string{hamiImagePrefix + componentversion.HAMi}, hamiImages)
	})

	expectedSchedulerImages := make([]string, 0, len(clusterversion.KubeSchedulerVersionsByMinor))
	for minor, version := range clusterversion.KubeSchedulerVersionsByMinor {
		expectedSchedulerImages = append(expectedSchedulerImages, schedulerImagePrefix+version)
		t.Run(minor, func(t *testing.T) {
			assert.Regexp(t, "^v"+regexp.QuoteMeta(minor)+`\.[0-9]+$`, version)
		})
	}

	t.Run("scheduler image set", func(t *testing.T) {
		assert.ElementsMatch(t, expectedSchedulerImages, schedulerImages, "scheduler pins must match %s exactly", imageListPath)
	})
}
