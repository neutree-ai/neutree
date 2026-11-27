package source

import (
	"context"
	"encoding/json"
	"os"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type StaticConfigSource struct {
	File   string
	Config state.ConfigSnapshot
}

func (StaticConfigSource) Name() string { return "static" }
func (s StaticConfigSource) Run(ctx context.Context, emit event.Emit) error {
	cfg := s.Config

	if s.File != "" {
		b, err := os.ReadFile(s.File)
		if err != nil {
			return err
		}

		if err = json.Unmarshal(b, &cfg); err != nil {
			return err
		}
	}

	return emit(ctx, event.Event{Lane: event.LaneConfig, Kind: state.ConfigReplace, Value: cfg})
}
