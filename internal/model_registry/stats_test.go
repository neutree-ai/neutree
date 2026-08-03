package model_registry_test

import (
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/internal/model_registry/mocks"
)

func fixedClock(at string) func() time.Time {
	return func() time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			panic(err)
		}

		return parsed
	}
}

func TestStatsAggregator_NeedsRefresh(t *testing.T) {
	aggregator := model_registry.StatsAggregator{
		StaleAfter: time.Hour,
		Now:        fixedClock("2026-01-01T12:00:00Z"),
	}

	tests := []struct {
		name     string
		previous *v1.ModelRegistryStats
		want     bool
	}{
		{name: "never measured", previous: nil, want: true},
		{
			name:     "measured but undated",
			previous: &v1.ModelRegistryStats{ModelCount: 1},
			want:     true,
		},
		{
			name:     "timestamp unparseable",
			previous: &v1.ModelRegistryStats{StatsUpdatedAt: "yesterday"},
			want:     true,
		},
		{
			name:     "fresh",
			previous: &v1.ModelRegistryStats{StatsUpdatedAt: "2026-01-01T11:30:00Z"},
			want:     false,
		},
		{
			name:     "exactly at the threshold",
			previous: &v1.ModelRegistryStats{StatsUpdatedAt: "2026-01-01T11:00:00Z"},
			want:     true,
		},
		{
			name:     "stale",
			previous: &v1.ModelRegistryStats{StatsUpdatedAt: "2026-01-01T09:00:00Z"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, aggregator.NeedsRefresh(tt.previous))
		})
	}
}

// The whole point of the staleness check: the reconcile loop runs every ten
// seconds and walking a networked model tree cannot.
func TestStatsAggregator_ThrottlesTheWalk(t *testing.T) {
	registry := &mocks.MockModelRegistry{}
	registry.On("CollectUsage").Return(&model_registry.RegistryUsage{ModelCount: 2, StorageBytes: 4096}, nil)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	aggregator := model_registry.StatsAggregator{
		StaleAfter: time.Hour,
		Now:        func() time.Time { return now },
	}

	var (
		stats     *v1.ModelRegistryStats
		refreshed bool
		writes    int
	)

	// Twenty reconciles a minute apart: one measurement, at the first.
	for i := 0; i < 20; i++ {
		stats, refreshed = aggregator.Refresh(registry, stats, "default/models")
		if refreshed {
			writes++
		}

		now = now.Add(time.Minute)
	}

	registry.AssertNumberOfCalls(t, "CollectUsage", 1)
	assert.Equal(t, 1, writes)
	require.NotNil(t, stats)
	assert.Equal(t, 2, stats.ModelCount)
	assert.Equal(t, int64(4096), stats.StorageBytes)

	// Once the counters age past the threshold it measures again.
	now = now.Add(2 * time.Hour)
	_, refreshed = aggregator.Refresh(registry, stats, "default/models")
	assert.True(t, refreshed)
	registry.AssertNumberOfCalls(t, "CollectUsage", 2)
}

// A registry that cannot be measured — unreachable, or public and not ours to
// measure — must keep the counters it last reported. Zeroing them would present
// a mount failure as an empty registry.
func TestStatsAggregator_KeepsPreviousWhenMeasurementFails(t *testing.T) {
	previous := &v1.ModelRegistryStats{
		ModelCount:     7,
		StorageBytes:   123456,
		StatsUpdatedAt: "2026-01-01T00:00:00Z",
	}

	tests := []struct {
		name string
		err  error
	}{
		{name: "storage unreachable", err: errors.New("mount gone")},
		{name: "registry does not support it", err: errors.Wrap(model_registry.ErrNotSupported, "public registry")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := &mocks.MockModelRegistry{}
			registry.On("CollectUsage").Return(nil, tt.err)

			aggregator := model_registry.StatsAggregator{Now: fixedClock("2026-06-01T00:00:00Z")}

			stats, refreshed := aggregator.Refresh(registry, previous, "default/models")

			assert.False(t, refreshed)
			assert.Equal(t, previous, stats)
		})
	}
}

func TestStatsAggregator_Aggregate(t *testing.T) {
	aggregator := model_registry.StatsAggregator{Now: fixedClock("2026-01-01T12:00:00Z")}

	stats := aggregator.Aggregate(&model_registry.RegistryUsage{ModelCount: 3, StorageBytes: 900})
	require.NotNil(t, stats)
	assert.Equal(t, 3, stats.ModelCount)
	assert.Equal(t, int64(900), stats.StorageBytes)
	assert.Equal(t, "2026-01-01T12:00:00Z", stats.StatsUpdatedAt)

	assert.Nil(t, aggregator.Aggregate(nil))
}

func TestStatsAggregator_DefaultStaleAfter(t *testing.T) {
	aggregator := model_registry.StatsAggregator{Now: fixedClock("2026-01-01T12:00:00Z")}

	justInside := &v1.ModelRegistryStats{
		StatsUpdatedAt: fixedClock("2026-01-01T12:00:00Z")().
			Add(-model_registry.DefaultStatsStaleAfter + time.Second).Format(time.RFC3339Nano),
	}
	assert.False(t, aggregator.NeedsRefresh(justInside))

	justOutside := &v1.ModelRegistryStats{
		StatsUpdatedAt: fixedClock("2026-01-01T12:00:00Z")().
			Add(-model_registry.DefaultStatsStaleAfter - time.Second).Format(time.RFC3339Nano),
	}
	assert.True(t, aggregator.NeedsRefresh(justOutside))
}
