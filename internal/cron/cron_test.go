package cron

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func mockCleanupBatches(s *storagemocks.MockStorage, batches ...int) {
	for _, n := range batches {
		s.On("CallDatabaseFunction", "cleanup_aggregated_records", mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				*args.Get(2).(*int) = n
			}).
			Return(nil).Once()
	}
}

func TestCleanupAggregatedRecordsDrainsUntilShortBatch(t *testing.T) {
	s := &storagemocks.MockStorage{}
	mockCleanupBatches(s, cleanupBatchSize, cleanupBatchSize, 7)

	deleted, err := cleanupAggregatedRecords(s)

	require.NoError(t, err)
	assert.Equal(t, 2*cleanupBatchSize+7, deleted)
	s.AssertNumberOfCalls(t, "CallDatabaseFunction", 3)
}

func TestCleanupAggregatedRecordsStopsAtCap(t *testing.T) {
	s := &storagemocks.MockStorage{}
	s.On("CallDatabaseFunction", "cleanup_aggregated_records", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			*args.Get(2).(*int) = cleanupBatchSize
		}).
		Return(nil)

	deleted, err := cleanupAggregatedRecords(s)

	require.NoError(t, err)
	assert.Equal(t, cleanupMaxBatches*cleanupBatchSize, deleted)
	s.AssertNumberOfCalls(t, "CallDatabaseFunction", cleanupMaxBatches)
}

func TestCleanupAggregatedRecordsStopsOnError(t *testing.T) {
	s := &storagemocks.MockStorage{}
	mockCleanupBatches(s, cleanupBatchSize)
	s.On("CallDatabaseFunction", "cleanup_aggregated_records", mock.Anything, mock.Anything).
		Return(errors.New("rpc failed")).Once()

	deleted, err := cleanupAggregatedRecords(s)

	require.Error(t, err)
	assert.Equal(t, cleanupBatchSize, deleted)
	s.AssertNumberOfCalls(t, "CallDatabaseFunction", 2)
}

func TestCleanupRateLimitCountersDrainsUntilShortBatch(t *testing.T) {
	s := &storagemocks.MockStorage{}
	for _, n := range []int{rateLimitCleanupBatchSize, 3} {
		s.On("CallDatabaseFunction", "cleanup_gateway_rate_limit_counters", mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				*args.Get(2).(*int) = n
			}).
			Return(nil).Once()
	}

	deleted, err := cleanupRateLimitCounters(s)

	require.NoError(t, err)
	assert.Equal(t, rateLimitCleanupBatchSize+3, deleted)
	s.AssertNumberOfCalls(t, "CallDatabaseFunction", 2)
}

func TestCleanupRateLimitCountersStopsOnError(t *testing.T) {
	s := &storagemocks.MockStorage{}
	s.On("CallDatabaseFunction", "cleanup_gateway_rate_limit_counters", mock.Anything, mock.Anything).
		Return(errors.New("rpc failed")).Once()

	deleted, err := cleanupRateLimitCounters(s)

	require.Error(t, err)
	assert.Equal(t, 0, deleted)
}
