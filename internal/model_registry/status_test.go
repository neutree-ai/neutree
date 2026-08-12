package model_registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNextStatus(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-72 * time.Hour).Format(time.RFC3339Nano)

	tests := []struct {
		name               string
		previous           *v1.ModelRegistryStatus
		phase              v1.ModelRegistryPhase
		errMessage         string
		wantTransitionTime string
		wantStats          *v1.ModelRegistryStats
	}{
		{
			// The acceptance case: a registry that has been up for days reports the
			// moment it came up, not the moment it was last looked at.
			name: "unchanged phase keeps the transition time",
			previous: &v1.ModelRegistryStatus{
				Phase:              v1.ModelRegistryPhaseCONNECTED,
				LastTransitionTime: earlier,
			},
			phase:              v1.ModelRegistryPhaseCONNECTED,
			wantTransitionTime: earlier,
		},
		{
			name: "a real transition moves the transition time",
			previous: &v1.ModelRegistryStatus{
				Phase:              v1.ModelRegistryPhaseCONNECTED,
				LastTransitionTime: earlier,
			},
			phase:              v1.ModelRegistryPhaseFAILED,
			errMessage:         "dial tcp: connection refused",
			wantTransitionTime: now.Format(time.RFC3339Nano),
		},
		{
			// Same phase, different reason. The reason has to be replaced; the
			// transition has not happened.
			name: "a changed reason is not a transition",
			previous: &v1.ModelRegistryStatus{
				Phase:              v1.ModelRegistryPhaseFAILED,
				LastTransitionTime: earlier,
				ErrorMessage:       "invalid token",
			},
			phase:              v1.ModelRegistryPhaseFAILED,
			errMessage:         "dial tcp: connection refused",
			wantTransitionTime: earlier,
		},
		{
			name:               "no previous status",
			phase:              v1.ModelRegistryPhaseCONNECTED,
			wantTransitionTime: now.Format(time.RFC3339Nano),
		},
		{
			// PostgREST replaces the whole composite, so anything not carried
			// forward here is erased on the next write.
			name: "statistics are carried forward",
			previous: &v1.ModelRegistryStatus{
				Phase:              v1.ModelRegistryPhaseCONNECTED,
				LastTransitionTime: earlier,
				Stats:              &v1.ModelRegistryStats{ModelCount: 5, StorageBytes: 1024},
			},
			phase:              v1.ModelRegistryPhaseFAILED,
			errMessage:         "gone",
			wantTransitionTime: now.Format(time.RFC3339Nano),
			wantStats:          &v1.ModelRegistryStats{ModelCount: 5, StorageBytes: 1024},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextStatus(tt.previous, tt.phase, tt.errMessage, now)

			assert.Equal(t, tt.phase, got.Phase)
			assert.Equal(t, tt.errMessage, got.ErrorMessage)
			assert.Equal(t, tt.wantTransitionTime, got.LastTransitionTime)
			assert.Equal(t, tt.wantStats, got.Stats)
			// Whatever else happened, the check itself was just made.
			assert.Equal(t, now.Format(time.RFC3339Nano), got.LastCheckedAt)
		})
	}
}

func TestNeedsStatusWrite(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	interval := time.Minute

	connected := func(checkedAgo time.Duration, errMessage string) *v1.ModelRegistryStatus {
		phase := v1.ModelRegistryPhaseCONNECTED
		if errMessage != "" {
			phase = v1.ModelRegistryPhaseFAILED
		}

		return &v1.ModelRegistryStatus{
			Phase:         phase,
			ErrorMessage:  errMessage,
			LastCheckedAt: now.Add(-checkedAgo).Format(time.RFC3339Nano),
		}
	}

	tests := []struct {
		name           string
		previous       *v1.ModelRegistryStatus
		phase          v1.ModelRegistryPhase
		errMessage     string
		statsRefreshed bool
		want           bool
	}{
		{
			// The reconcile runs every ten seconds. Writing each one back would be
			// a stream of updates that say nothing.
			name:     "nothing changed and the check time is fresh",
			previous: connected(5*time.Second, ""),
			phase:    v1.ModelRegistryPhaseCONNECTED,
			want:     false,
		},
		{
			name:     "nothing changed but the check time has gone stale",
			previous: connected(2*time.Minute, ""),
			phase:    v1.ModelRegistryPhaseCONNECTED,
			want:     true,
		},
		{
			name:     "the phase changed",
			previous: connected(5*time.Second, ""),
			phase:    v1.ModelRegistryPhaseFAILED,
			want:     true,
		},
		{
			// Both are non-empty, so a check for "is there an error" would say
			// nothing changed and leave a reason on display that no longer applies.
			name:       "still failing, for a different reason",
			previous:   connected(5*time.Second, "invalid token"),
			phase:      v1.ModelRegistryPhaseFAILED,
			errMessage: "dial tcp: connection refused",
			want:       true,
		},
		{
			name:       "still failing for the same reason",
			previous:   connected(5*time.Second, "invalid token"),
			phase:      v1.ModelRegistryPhaseFAILED,
			errMessage: "invalid token",
			want:       false,
		},
		{
			name:           "fresh statistics must be recorded",
			previous:       connected(5*time.Second, ""),
			phase:          v1.ModelRegistryPhaseCONNECTED,
			statsRefreshed: true,
			want:           true,
		},
		{
			name:  "no status at all",
			phase: v1.ModelRegistryPhaseCONNECTED,
			want:  true,
		},
		{
			// A row written before this field existed, or one whose timestamp is
			// unreadable, has to start moving again.
			name:     "check time missing",
			previous: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
			phase:    v1.ModelRegistryPhaseCONNECTED,
			want:     true,
		},
		{
			name: "check time unparseable",
			previous: &v1.ModelRegistryStatus{
				Phase:         v1.ModelRegistryPhaseCONNECTED,
				LastCheckedAt: "not a timestamp",
			},
			phase: v1.ModelRegistryPhaseCONNECTED,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsStatusWrite(tt.previous, tt.phase, tt.errMessage, tt.statsRefreshed, now, interval)
			assert.Equal(t, tt.want, got)
		})
	}
}
