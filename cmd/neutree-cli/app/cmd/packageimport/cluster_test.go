package packageimport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	internalpackageimport "github.com/neutree-ai/neutree/internal/cli/packageimport"
	"github.com/neutree-ai/neutree/pkg/client"
)

func TestClusterImportForceUpdateUsesControlPlaneClient(t *testing.T) {
	oldNewClient := clusterImportNewAPIClient
	oldNewImporter := clusterImportNewImporter
	t.Cleanup(func() {
		clusterImportNewAPIClient = oldNewClient
		clusterImportNewImporter = oldNewImporter
	})

	createdClient := false
	clusterImportNewAPIClient = func() (*client.Client, error) {
		createdClient = true
		return client.NewClient("http://example.invalid"), nil
	}
	importer := &recordingClusterPackageImporter{}
	clusterImportNewImporter = func(apiClient *client.Client) clusterPackageImporter {
		require.NotNil(t, apiClient)
		return importer
	}

	err := runClusterImport(&ClusterImportOptions{
		packagePath: "cluster-package.tar.gz",
		importLocal: true,
		forceUpdate: true,
	})

	require.NoError(t, err)
	assert.True(t, createdClient)
	require.NotNil(t, importer.options)
	assert.True(t, importer.options.SkipImagePush)
	assert.True(t, importer.options.ForceUpdate)
	assert.Equal(t, workspace, importer.options.Workspace)
}

func TestClusterImportCommandHasForceUpdateFlag(t *testing.T) {
	assert.NotNil(t, NewClusterImportCmd().Flags().Lookup("force-update"))
}

type recordingClusterPackageImporter struct {
	options *internalpackageimport.ImportOptions
}

func (importer *recordingClusterPackageImporter) Import(_ context.Context, options *internalpackageimport.ImportOptions) (*internalpackageimport.ImportResult, error) {
	importer.options = options
	return &internalpackageimport.ImportResult{}, nil
}
