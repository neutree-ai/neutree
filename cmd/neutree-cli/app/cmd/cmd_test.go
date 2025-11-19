package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewNeutreeCliCommandIncludesCluster(t *testing.T) {
	cmd := NewNeutreeCliCommand()

	found := false
	for _, c := range cmd.Commands() {
		if c.Use == "cluster" {
			found = true
			break
		}
	}
	assert.True(t, found, "cluster command should be registered in the root CLI")
}
