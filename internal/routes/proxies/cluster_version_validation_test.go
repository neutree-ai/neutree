package proxies

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClusterAcceleratorVirtualizationDisableRequestedBoundaryInputs(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
		wantErr string
	}{
		{name: "invalid payload", payload: `{`, wantErr: "unexpected end of JSON input"},
		{name: "missing spec", payload: `{}`, want: false},
		{name: "invalid accelerator virtualization", payload: `{"spec":{"accelerator_virtualization":"invalid"}}`, wantErr: "cannot unmarshal"},
		{name: "explicit enabled", payload: `{"spec":{"accelerator_virtualization":{"enabled":true}}}`, want: false},
		{name: "invalid enabled", payload: `{"spec":{"accelerator_virtualization":{"enabled":"false"}}}`, wantErr: "cannot unmarshal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disabled, err := clusterAcceleratorVirtualizationDisableRequested([]byte(tt.payload))

			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.want, disabled)
		})
	}
}
