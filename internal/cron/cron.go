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

	_, err = s.NewJob(gocron.DurationJob(time.Minute*5), gocron.NewTask(func() {
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

		jobErr := storage.CallDatabaseFunction("cleanup_aggregated_records", map[string]interface{}{
			"p_older_than": "15 minutes",
			"p_batch_size": 1000,
		}, nil)
		if jobErr != nil {
			klog.Errorf("Failed to cleanup aggregated records: %v", jobErr)
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
