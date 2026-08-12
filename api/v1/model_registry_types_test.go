package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The kinds a client must not be offered write controls or storage figures for.
func TestVisibilityForModelRegistryType(t *testing.T) {
	tests := []struct {
		kind ModelRegistryType
		want string
	}{
		{BentoMLModelRegistryType, ModelRegistryVisibilityPrivate},
		{HuggingFaceModelRegistryType, ModelRegistryVisibilityPublic},
		{ModelScopeModelRegistryType, ModelRegistryVisibilityPublic},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			assert.Equal(t, tt.want, VisibilityForModelRegistryType(tt.kind))
		})
	}
}

// A kind this build does not know is private, which is the safe direction:
// calling it public would present somebody's own store as a read-only hub
// operated by a third party, and would switch off the write controls they need.
func TestVisibilityForModelRegistryTypeFailsSafeForUnknownKinds(t *testing.T) {
	assert.Equal(t, ModelRegistryVisibilityPrivate,
		VisibilityForModelRegistryType(ModelRegistryType("some-future-hub")))
}
