package packageimport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportCmdNoCleanupImageFlagIsInheritedByImageImportCommands(t *testing.T) {
	importCmd := NewImportCmd()

	flag := importCmd.PersistentFlags().Lookup("no-cleanup-image")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)

	for _, commandName := range []string{"engine", "cluster", "controlplane"} {
		t.Run(commandName, func(t *testing.T) {
			command, _, err := importCmd.Find([]string{commandName})
			require.NoError(t, err)
			require.NotNil(t, command)
			assert.NotNil(t, command.InheritedFlags().Lookup("no-cleanup-image"))
		})
	}
}

func TestImportCmdParsesNoCleanupImageFlagForChildCommand(t *testing.T) {
	previousValue := noCleanupImage
	noCleanupImage = false
	t.Cleanup(func() { noCleanupImage = previousValue })

	importCmd := NewImportCmd()
	importCmd.SetArgs([]string{"cluster", "--no-cleanup-image", "--help"})

	require.NoError(t, importCmd.Execute())
	assert.True(t, noCleanupImage)
}
