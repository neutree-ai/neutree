package launch

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/command/mocks"
)

// expectNoRetiredContainers answers the post-up lookup of retired services.
func expectNoRetiredContainers(m *mocks.MockExecutor) {
	m.On("Execute", mock.Anything, "docker", mock.MatchedBy(func(args []string) bool {
		return len(args) > 1 && args[0] == "ps" && args[1] == "-aq"
	})).Return([]byte(""), nil).Maybe()
}

func TestRemoveRetiredServices(t *testing.T) {
	f := (&fakeDocker{}).
		on("ps -aq --filter label=com.docker.compose.project=neutree-core --filter label=com.docker.compose.service=postgres-meta", "abc123\n", nil).
		on("rm -f abc123", "", nil)

	var out bytes.Buffer
	require.NoError(t, removeRetiredCoreServices(f, &out))
	assert.Contains(t, f.calls, "rm -f abc123")
	assert.Contains(t, out.String(), "postgres-meta")
}

func TestRemoveRetiredServicesNothingToRemove(t *testing.T) {
	f := (&fakeDocker{}).on("ps -aq", "", nil)

	require.NoError(t, removeRetiredCoreServices(f, &bytes.Buffer{}))

	for _, c := range f.calls {
		assert.NotContains(t, c, "rm -f", "nothing should be removed")
	}
}

func TestRemoveRetiredServicesListFails(t *testing.T) {
	f := (&fakeDocker{}).on("ps -aq", "daemon down", fmt.Errorf("exit status 1"))

	err := removeRetiredCoreServices(f, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres-meta")
}
