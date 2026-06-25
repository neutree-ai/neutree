package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPushCmd_InvalidModelName(t *testing.T) {
	modelDir := t.TempDir()
	cmd := NewPushCmd()
	cmd.SetArgs([]string{
		modelDir,
		"--name", "Invalid_Name",
		"--version", "v1.0",
	})

	err := cmd.Execute()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid model name")
	assert.Contains(t, err.Error(), "model name must be lowercase")
}
