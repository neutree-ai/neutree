package cron

import (
	"context"
	"time"

	gocron "github.com/go-co-op/gocron/v2"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// StartCrons starts all cron jobs
func StartCrons(ctx context.Context, storage storage.Storage) error {
	s, err := gocron.NewScheduler()
	if err != nil {
		return errors.Wrapf(err, "failed to init cron scheduler")
	}

	// Runs frequently (vs. the other jobs below) to bound how long an API key's
	// token_quota usage can lag actual consumption, since get_api_key_remaining
	// reads from the aggregated api_daily_usage table, not the raw usage records.
	_, err = s.NewJob(gocron.DurationJob(time.Second*30), gocron.NewTask(func() {
		klog.V(4).Infof("Start to aggregate usage records")

		jobErr := storage.CallDatabaseFunction("aggregate_usage_records", map[string]interface{}{
			"p_older_than": time.Now().Format(time.RFC3339Nano),
		}, nil)
		if jobErr != nil {
			klog.Errorf("Failed to aggregate usage records: %v", jobErr)
		}
	}), gocron.WithSingletonMode(gocron.LimitModeWait))
	if err != nil {
		return errors.Wrapf(err, "failed to add aggregate usage records cron job")
	}

	_, err = s.NewJob(gocron.DurationJob(time.Minute*5), gocron.NewTask(func() {
		klog.V(4).Infof("Start to cleanup aggregated records")

		deleted, jobErr := cleanupAggregatedRecords(storage)
		if jobErr != nil {
			klog.Errorf("Failed to cleanup aggregated records after deleting %d: %v", deleted, jobErr)
		}
	}), gocron.WithSingletonMode(gocron.LimitModeWait))
	if err != nil {
		return errors.Wrapf(err, "failed to add cleanup aggregated records cron job")
	}

	_, err = s.NewJob(gocron.DurationJob(time.Minute*5), gocron.NewTask(func() {
		klog.V(4).Infof("Start to sync api key usage")

		jobErr := storage.CallDatabaseFunction("sync_api_key_usage", nil, nil)
		if jobErr != nil {
			klog.Errorf("Failed to sync api key usage: %v", jobErr)
		}
	}), gocron.WithSingletonMode(gocron.LimitModeWait))
	if err != nil {
		return errors.Wrapf(err, "failed to add sync api key usage cron job")
	}

	s.Start()

	go func() {
		<-ctx.Done()

		err = s.Shutdown()
		if err != nil {
			klog.Errorf("Failed to shutdown cron scheduler: %v", err)
		}
	}()

	return nil
}

const (
	aggregatedRecordRetention = "15 minutes"
	cleanupBatchSize          = 1000
	// cleanupMaxBatches bounds one run. At one run every five minutes it still
	// drains far more than a single batch could, so the raw table cannot grow
	// without bound under sustained traffic.
	cleanupMaxBatches = 100
)

// cleanupAggregatedRecords deletes aggregated raw usage records past the
// retention window, one batch per call so each delete is its own short
// transaction, until a batch comes back short or the per-run cap is reached.
// Only records already folded into the daily usage are ever deleted.
func cleanupAggregatedRecords(s storage.Storage) (int, error) {
	total := 0

	for range cleanupMaxBatches {
		var deleted int

		err := s.CallDatabaseFunction("cleanup_aggregated_records", map[string]interface{}{
			"p_older_than": aggregatedRecordRetention,
			"p_batch_size": cleanupBatchSize,
		}, &deleted)
		if err != nil {
			return total, err
		}

		total += deleted

		if deleted < cleanupBatchSize {
			break
		}
	}

	return total, nil
}
