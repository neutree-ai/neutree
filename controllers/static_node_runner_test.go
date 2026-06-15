package controllers

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
)

func TestStaticNodeSSHRunnerFactoryRequiresDirectAuth(t *testing.T) {
	factory := &StaticNodeSSHRunnerFactory{}

	_, err := factory.NewStaticNodeRunner(context.Background(), &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			IP:         "10.0.0.10",
			SSHAuthRef: "ssh-ref",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh_auth_ref")
	assert.Contains(t, err.Error(), "set spec.ssh_auth")
}

func TestStaticNodeSSHRunnerFactoryWrapsSSHRunner(t *testing.T) {
	var calls []processCall
	factory := &StaticNodeSSHRunnerFactory{
		ProcessExecute: func(_ context.Context, name string, args []string) ([]byte, error) {
			calls = append(calls, processCall{name: name, args: args})
			if len(calls) == 1 {
				return []byte("up\n"), nil
			}

			return []byte("ok\n"), nil
		},
	}

	runner, err := factory.NewStaticNodeRunner(context.Background(), &v1.StaticNode{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "head-0",
		},
		Spec: &v1.StaticNodeSpec{
			IP: "10.0.0.10",
			SSHAuth: &v1.Auth{
				SSHUser:       "ray",
				SSHPrivateKey: "cmF5LXBlbQo=",
			},
		},
	})
	require.NoError(t, err)
	_, supportsFiles := runner.(interface {
		Files() commandrunner.FileClient
	})
	assert.True(t, supportsFiles)

	output, err := runner.Run(context.Background(), "docker ps")

	require.NoError(t, err)
	assert.Equal(t, "ok\n", output)
	require.Len(t, calls, 2)
	assert.Equal(t, "ssh", calls[0].name)
	keyPath := sshArgValue(calls[0].args, "-i")
	require.NotEmpty(t, keyPath)
	keyData, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, "ray-pem\n", string(keyData))
	assert.Equal(t, "uptime", calls[0].args[len(calls[0].args)-1])
	assert.Equal(t, "ray@10.0.0.10", calls[0].args[len(calls[0].args)-2])
	assert.Equal(t, "docker ps", calls[1].args[len(calls[1].args)-1])
}

type processCall struct {
	name string
	args []string
}

func sshArgValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}

	return ""
}
