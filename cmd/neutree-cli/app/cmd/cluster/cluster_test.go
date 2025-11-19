package cluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewClusterCmd(t *testing.T) {
	cmd := NewClusterCmd()
	assert.Equal(t, "cluster", cmd.Use)
	// should contain import subcommand
	found := false
	for _, c := range cmd.Commands() {
		if c.Use == "import" {
			found = true
			break
		}
	}
	assert.True(t, found, "import subcommand should be registered")
}
