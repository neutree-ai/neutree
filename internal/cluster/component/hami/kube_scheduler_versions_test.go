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
)

func TestKubeSchedulerVersionsByMinor(t *testing.T) {
	cases := []struct {
		minor   string
		version string
	}{
		{minor: "1.26", version: "v1.26.15"},
		{minor: "1.27", version: "v1.27.16"},
		{minor: "1.28", version: "v1.28.15"},
		{minor: "1.29", version: "v1.29.14"},
		{minor: "1.30", version: "v1.30.14"},
		{minor: "1.31", version: "v1.31.14"},
		{minor: "1.32", version: "v1.32.13"},
		{minor: "1.33", version: "v1.33.13"},
		{minor: "1.34", version: "v1.34.12"},
		{minor: "1.35", version: "v1.35.9"},
	}

	assert.Len(t, KubeSchedulerVersionsByMinor, len(cases))
	assert.Equal(t, "v1.32.13", DefaultKubeSchedulerVersion())

	for _, tc := range cases {
		t.Run(tc.minor, func(t *testing.T) {
			require.Contains(t, KubeSchedulerVersionsByMinor, tc.minor)
			assert.Equal(t, tc.version, KubeSchedulerVersionsByMinor[tc.minor])
		})
	}
}

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
		assert.Equal(t, "v2.10.0", Version)
		assert.Equal(t, []string{hamiImagePrefix + Version}, hamiImages)
	})

	expectedSchedulerImages := make([]string, 0, len(KubeSchedulerVersionsByMinor))
	for minor, version := range KubeSchedulerVersionsByMinor {
		expectedSchedulerImages = append(expectedSchedulerImages, schedulerImagePrefix+version)
		t.Run(minor, func(t *testing.T) {
			assert.Regexp(t, "^v"+regexp.QuoteMeta(minor)+`\.[0-9]+$`, version)
		})
	}

	t.Run("scheduler image set", func(t *testing.T) {
		assert.ElementsMatch(t, expectedSchedulerImages, schedulerImages, "scheduler pins must match %s exactly", imageListPath)
	})
}
