package notify

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lib/pq"
	"k8s.io/klog/v2"
)

type Payload struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type DispatchFunc func(kind, id string)

func Start(ctx context.Context, dsn, channel string, dispatch DispatchFunc) {
	if dsn == "" || channel == "" {
		return
	}

	listener := pq.NewListener(dsn, 2*time.Second, time.Minute, nil)
	if err := listener.Listen(channel); err != nil {
		klog.Errorf("failed to listen channel %s: %v", channel, err)
		listener.Close()

		return
	}

	klog.Infof("db notify listener started on channel %s", channel)

	go func() {
		defer listener.Close()

		for {
			select {
			case <-ctx.Done():
				klog.Infof("db notify listener stopped for channel %s", channel)
				return
			case notification := <-listener.Notify:
				if notification == nil {
					continue
				}

				var payload Payload
				if err := json.Unmarshal([]byte(notification.Extra), &payload); err != nil {
					klog.Warningf("invalid notify payload on %s: %s, err: %v", channel, notification.Extra, err)
					continue
				}

				if payload.Kind == "" || payload.ID == "" {
					klog.Warningf("invalid notify payload on %s: kind/id empty", channel)
					continue
				}

				dispatch(payload.Kind, payload.ID)
			case <-time.After(30 * time.Second):
				if err := listener.Ping(); err != nil {
					klog.Warningf("db notify ping error on channel %s: %v", channel, err)
				}
			}
		}
	}()
}
