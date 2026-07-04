package staticnode

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDockerRuntimeCreatesDockerConfigDirWithoutAuth(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
		},
	}

	_, err := NewDockerRuntime(context.Background(), runner, nil)

	require.NoError(t, err)
	assert.Equal(t, 1, runner.calls)
}

func TestNewDockerRuntimeSkipsLoginWhenRegistryConfigMatches(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				command: "cat '/etc/neutree/docker/config.json'",
				output:  registryAuthConfigJSON("registry.example.com", "user", "token"),
			},
		},
	}

	_, err := NewDockerRuntime(context.Background(), runner, &RegistryAuth{
		Server:   "registry.example.com",
		Username: "user",
		Password: "token",
	})

	require.NoError(t, err)
	assert.Equal(t, 2, runner.calls)
}

func TestNewDockerRuntimeLoginsWhenRegistryConfigDoesNotMatch(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				command: "cat '/etc/neutree/docker/config.json'",
				output:  registryAuthConfigJSON("registry.example.com", "user", "old-token"),
			},
			{
				command: "docker --config '/etc/neutree/docker' login 'registry.example.com' -u 'user' -p 'token'",
			},
		},
	}

	_, err := NewDockerRuntime(context.Background(), runner, &RegistryAuth{
		Server:   "registry.example.com",
		Username: "user",
		Password: "token",
	})

	require.NoError(t, err)
	assert.Equal(t, 3, runner.calls)
}

func TestNewDockerRuntimeRedactsRegistryLoginPasswordFromError(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				command: "cat '/etc/neutree/docker/config.json'",
				err:     errors.New("no config"),
			},
			{
				command: "docker --config '/etc/neutree/docker' login 'registry.example.com' -u 'user' -p 'token'",
				err:     errors.New("command failed with password token"),
			},
		},
	}

	_, err := NewDockerRuntime(context.Background(), runner, &RegistryAuth{
		Server:   "registry.example.com",
		Username: "user",
		Password: "token",
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "token")
	assert.Contains(t, err.Error(), "[REDACTED]")
}

func TestDockerRuntimePullImageReturnsDockerReason(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker pull 'registry.example.com/neutree/serve:v1.2.0'",
				err:     errors.New("pull denied"),
			},
		},
	}

	runtime := newTestDockerRuntime(t, runner, nil)
	err := runtime.PullImage(context.Background(), "registry.example.com/neutree/serve:v1.2.0")

	require.Error(t, err)
	assert.Equal(t, componentReasonImagePullFailed, dockerReason(err, ""))
	assert.Contains(t, err.Error(), "failed to pull image registry.example.com/neutree/serve:v1.2.0")
}

func TestDockerRuntimeEnsureImageSkipsPullWhenLocalImageExists(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker image inspect 'registry.example.com/neutree/serve:v1.2.0' >/dev/null",
			},
		},
	}

	runtime := newTestDockerRuntime(t, runner, nil)
	err := runtime.EnsureImage(context.Background(), "registry.example.com/neutree/serve:v1.2.0")

	require.NoError(t, err)
	assert.Equal(t, 2, runner.calls)
}

func TestDockerRuntimeEnsureImagePullsWhenLocalImageMissing(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker image inspect 'registry.example.com/neutree/serve:v1.2.0' >/dev/null",
				err:     errors.New("no such image"),
			},
			{
				command: "docker pull 'registry.example.com/neutree/serve:v1.2.0'",
				err:     errors.New("pull denied"),
			},
		},
	}

	runtime := newTestDockerRuntime(t, runner, nil)
	err := runtime.EnsureImage(context.Background(), "registry.example.com/neutree/serve:v1.2.0")

	require.Error(t, err)
	assert.Equal(t, componentReasonImagePullFailed, dockerReason(err, ""))
	assert.Equal(t, 3, runner.calls)
}

func TestDockerRuntimePullImageUsesRegistryAuth(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				command: "cat '/etc/neutree/docker/config.json'",
				err:     errors.New("no config"),
			},
			{
				command: "docker --config '/etc/neutree/docker' login 'registry.example.com' -u 'user' -p 'token'",
			},
			{
				command: "docker --config '/etc/neutree/docker' pull 'registry.example.com/neutree/serve:v1.2.0'",
			},
		},
	}

	runtime := newTestDockerRuntime(t, runner, &RegistryAuth{
		Server:   "registry.example.com",
		Username: "user",
		Password: "token",
	})
	err := runtime.PullImage(context.Background(), "registry.example.com/neutree/serve:v1.2.0")

	require.NoError(t, err)
	assert.Equal(t, 4, runner.calls)
}

func TestDockerRuntimeRestartComponentPreparesDockerConfigMount(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker image inspect 'registry.example.com/neutree/serve:v1.2.0' >/dev/null",
			},
			{
				command: "docker rm -f 'neutree-static-a-ray-head' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-ray-head'",
					"'--volume' '/etc/neutree/docker:/root/.docker:ro'",
				},
			},
		},
	}

	runtime := newTestDockerRuntime(t, runner, nil)
	err := runtime.RestartComponentContainer(
		context.Background(),
		&v1.StaticNode{Spec: &v1.StaticNodeSpec{Cluster: "static-a"}},
		v1.NodeComponentSpec{
			Name:  "ray-head",
			Image: "registry.example.com/neutree/serve:v1.2.0",
			DockerRunOptions: []string{
				"--volume /etc/neutree/docker:/root/.docker:ro",
			},
		},
		"hash-ray-head",
	)

	require.NoError(t, err)
	assert.Equal(t, 4, runner.calls)
}

func newTestDockerRuntime(t *testing.T, runner CommandRunner, registryAuth *RegistryAuth) DockerRuntime {
	t.Helper()

	if registryAuth == nil {
		if fakeRunner, ok := runner.(*fakeStaticNodeRunner); ok {
			fakeRunner.responses = append([]fakeStaticNodeResponse{
				{command: "mkdir -p '/etc/neutree/docker'"},
			}, fakeRunner.responses...)
		}
	}

	runtime, err := NewDockerRuntime(context.Background(), runner, registryAuth)
	require.NoError(t, err)

	return runtime
}

func registryAuthConfigJSON(server, username, password string) string {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))

	return `{"auths":{"` + server + `":{"auth":"` + auth + `"}}}`
}
