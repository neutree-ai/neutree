package staticnode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
)

func TestSSHRunnerFactoryRequiresDirectAuth(t *testing.T) {
	factory := &SSHRunnerFactory{}

	_, err := factory.NewStaticNodeRunner(context.Background(), &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			IP: "10.0.0.10",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.ssh_auth is required")
}

func TestSSHRunnerFactoryWrapsSSHRunner(t *testing.T) {
	var calls []processCall
	factory := &SSHRunnerFactory{
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

	require.NoError(t, runner.Close())
	_, err = os.Stat(keyPath)
	assert.True(t, os.IsNotExist(err))
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

func sshControlPath(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) && strings.HasPrefix(args[i+1], "ControlPath=") {
			return strings.TrimPrefix(args[i+1], "ControlPath=")
		}
	}

	return ""
}

func TestStaticNodeRunnerControlPathScopedToRunnerKeyDir(t *testing.T) {
	var calls []processCall
	factory := &SSHRunnerFactory{
		ProcessExecute: func(_ context.Context, name string, args []string) ([]byte, error) {
			calls = append(calls, processCall{name: name, args: args})
			return []byte("up\n"), nil
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

	_, err = runner.Run(context.Background(), "docker ps")
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.Equal(t, "ssh", calls[0].name)

	// ControlPath must be set (ControlMaster reuse active) and scoped inside the
	// per-runner temp key dir, not a shared global path.
	controlPath := sshControlPath(calls[0].args)
	require.NotEmpty(t, controlPath, "ControlPath option must be present when reuse is enabled")
	assert.True(t, strings.HasSuffix(controlPath, "/%C"),
		"ControlPath %q should use the OpenSSH %%C hashed per-target filename", controlPath)

	keyDir := filepath.Dir(sshArgValue(calls[0].args, "-i"))
	assert.True(t, strings.HasPrefix(controlPath, keyDir),
		"ControlPath %q should be scoped to the per-runner key dir %q", controlPath, keyDir)

	// Master options must be present so commands reuse the connection.
	assert.Contains(t, calls[0].args, "ControlMaster=auto")
	assert.Contains(t, calls[0].args, "ControlPersist="+commandrunner.SSHControlPersist)

	// The real command must multiplex onto the same control path as the precheck,
	// so the precheck becomes the master and the command rides it.
	require.Len(t, calls, 2)
	realControlPath := sshControlPath(calls[1].args)
	assert.Equal(t, controlPath, realControlPath,
		"real command should reuse the precheck's ControlPath")

	// Close() must terminate the lingering multiplexed master (not just unlink
	// the socket) so it cannot hold an authenticated connection past the cycle.
	require.NoError(t, runner.Close())
	require.Len(t, calls, 3)
	require.Equal(t, "ssh", calls[2].name)
	assert.Contains(t, calls[2].args, "-O")
	assert.Contains(t, calls[2].args, "exit")
}

func TestSSHRunnerFactoryControlPathOverride(t *testing.T) {
	var calls []processCall
	override := "/var/run/neutree-ssh-mux"
	factory := &SSHRunnerFactory{
		ProcessExecute: func(_ context.Context, name string, args []string) ([]byte, error) {
			calls = append(calls, processCall{name: name, args: args})
			return []byte("up\n"), nil
		},
		SSHControlPath: override,
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

	_, err = runner.Run(context.Background(), "docker ps")
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.Equal(t, "ssh", calls[0].name)

	controlPath := sshControlPath(calls[0].args)
	require.NotEmpty(t, controlPath)
	assert.Equal(t, override+"/%C", controlPath,
		"explicit SSHControlPath %q should take precedence; got %q", override, controlPath)

	require.NoError(t, runner.Close())
}
