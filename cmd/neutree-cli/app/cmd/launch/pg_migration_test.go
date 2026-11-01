package launch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/pkg/command/mocks"
)

// expectNoPostgres makes the migration step see a host without a database.
func expectNoPostgres(m *mocks.MockExecutor) {
	m.On("Execute", mock.Anything, "docker", mock.MatchedBy(func(args []string) bool {
		return args[0] == "inspect" || (args[0] == "volume" && args[1] == "inspect")
	})).Return([]byte(""), fmt.Errorf("no such object"))
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

// fakeDocker answers docker commands by the first matching prefix and
// records every call.
type fakeDocker struct {
	calls []string
	rules []fakeRule
}

type fakeRule struct {
	prefix string
	out    string
	err    error
}

func (f *fakeDocker) on(prefix, out string, err error) *fakeDocker {
	f.rules = append(f.rules, fakeRule{prefix, out, err})
	return f
}

func (f *fakeDocker) Execute(_ context.Context, name string, args []string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)

	for _, r := range f.rules {
		if strings.HasPrefix(call, r.prefix) {
			return []byte(r.out), r.err
		}
	}

	return nil, nil
}

func (f *fakeDocker) ExecuteWithTimeout(ctx context.Context, _ time.Duration, name string, args []string) ([]byte, error) {
	return f.Execute(ctx, name, args)
}

func (f *fakeDocker) index(t *testing.T, prefix string) int {
	t.Helper()

	for i, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}

	t.Fatalf("no call %q in\n%s", prefix, strings.Join(f.calls, "\n"))

	return -1
}

func (f *fakeDocker) has(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}

	return false
}

const mountInspect = `inspect -f {{range .Mounts}}`

func writeMigrationCompose(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	file := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(file, []byte(`services:
  postgres:
    image: mirror.example.com/postgres:17
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: secret-pw
      POSTGRES_DB: neutree
`), 0600))

	return file, dir
}

// legacyHost is a host running an earlier release on PostgreSQL 13.
func legacyHost() *fakeDocker {
	return (&fakeDocker{}).
		on(mountInspect, pgLegacyVolume, nil).
		on("inspect -f {{range $k", "neutree-core_neutree", nil).
		on("inspect -f {{.State.Running}} neutree-core", "true", nil).
		on("inspect -f {{.State.Running}} neutree-api", "true", nil).
		on("inspect -f {{.State.Running}} postgrest", "true", nil).
		on("inspect -f {{.State.Running}} auth", "false", nil)
}

func TestMigrateLegacyPostgresSkips(t *testing.T) {
	file, dir := writeMigrationCompose(t)

	t.Run("fresh host", func(t *testing.T) {
		f := (&fakeDocker{}).on("inspect", "", fmt.Errorf("no such object")).on("volume inspect", "", fmt.Errorf("no such volume"))
		require.NoError(t, migrateLegacyPostgres(f, &bytes.Buffer{}, file, dir, "pw"))
		assert.False(t, f.has("run"))
	})

	t.Run("already on PostgreSQL 17", func(t *testing.T) {
		f := (&fakeDocker{}).on(mountInspect, pgVolume, nil)
		require.NoError(t, migrateLegacyPostgres(f, &bytes.Buffer{}, file, dir, "pw"))
		assert.Equal(t, 1, len(f.calls))
	})

	t.Run("earlier release taken down", func(t *testing.T) {
		f := (&fakeDocker{}).
			on("inspect", "", fmt.Errorf("no such object")).
			on("volume inspect "+pgVolume, "", fmt.Errorf("no such volume")).
			on("volume inspect "+pgLegacyVolume, "[]", nil)
		err := migrateLegacyPostgres(f, &bytes.Buffer{}, file, dir, "pw")
		require.ErrorContains(t, err, "start the earlier release again")
	})
}

func TestMigrateLegacyPostgresCopies(t *testing.T) {
	file, dir := writeMigrationCompose(t)
	f := legacyHost().on("volume inspect "+pgVolume, "", fmt.Errorf("no such volume"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".pg-migration-1.env"), []byte("PGPASSWORD=old"), 0600))

	require.NoError(t, migrateLegacyPostgres(f, &bytes.Buffer{}, file, dir, "secret-pw"))

	// Slow steps come before writers stop; the target runs the rendered image
	// on the new volume.
	stop := f.index(t, "stop neutree-core")
	assert.Less(t, f.index(t, "compose -p neutree-core -f "+file+" pull --policy missing"), stop)
	assert.Less(t, f.index(t, "compose -p neutree-core -f "+file+" run --rm --no-deps copy-db-scripts"), stop)

	target := f.calls[f.index(t, "run -d --name "+pgMigrationTarget)]
	assert.Less(t, f.index(t, "run -d --name "+pgMigrationTarget), stop)
	assert.Contains(t, target, pgVolume+":"+pgDataDir)
	assert.Less(t, f.index(t, "volume create --label com.docker.compose.project=neutree-core --label com.docker.compose.volume=pg17_data "+pgVolume),
		f.index(t, "run -d --name "+pgMigrationTarget))
	assert.True(t, strings.HasSuffix(target, " mirror.example.com/postgres:17"))

	assert.Less(t, stop, f.index(t, "stop neutree-api"))
	assert.Less(t, f.index(t, "stop neutree-api"), f.index(t, "stop postgrest"))
	assert.False(t, f.has("stop auth"), "a stopped writer is left alone")

	runner := f.calls[f.index(t, "run --rm --name "+pgMigrationRunner)]
	assert.Less(t, stop, f.index(t, "run --rm --name "+pgMigrationRunner))
	assert.True(t, strings.HasSuffix(runner, "bash /scripts/pg-copy.sh copy"))
	assert.Contains(t, runner, "SOURCE_ID="+pgLegacyVolume)

	for _, c := range f.calls {
		assert.NotContains(t, c, "secret-pw", "passwords stay off command lines")
	}

	assert.False(t, f.has("start neutree"), "writers come back with the new release")
	assert.True(t, f.has("rm -f "+pgMigrationTarget))

	envFiles, _ := filepath.Glob(filepath.Join(dir, ".pg-migration-*.env"))
	assert.Empty(t, envFiles)
}

func TestMigrateLegacyPostgresCopyFailureRestartsWriters(t *testing.T) {
	file, dir := writeMigrationCompose(t)
	f := legacyHost().
		on("volume inspect "+pgVolume, "", fmt.Errorf("no such volume")).
		on("run --rm --name "+pgMigrationRunner, "the copy differs", exitErr(1))

	err := migrateLegacyPostgres(f, &bytes.Buffer{}, file, dir, "pw")
	require.ErrorContains(t, err, "keeps running on the earlier database")

	// Also auth, which was not running: an interrupted earlier run may have
	// stopped it.
	assert.Less(t, f.index(t, "start auth"), f.index(t, "start postgrest"))
	assert.Less(t, f.index(t, "start postgrest"), f.index(t, "start neutree-api"))
	assert.Less(t, f.index(t, "start neutree-api"), f.index(t, "start neutree-core"))
	assert.False(t, f.has("volume rm"))
}

func TestMigrateLegacyPostgresRerun(t *testing.T) {
	file, dir := writeMigrationCompose(t)

	t.Run("unfinished copy is discarded", func(t *testing.T) {
		f := legacyHost().on("volume inspect "+pgVolume, "[]", nil)
		// The first runner call is the check.
		calls := 0
		checker := &countingDocker{fakeDocker: f, onRunner: func() error {
			calls++
			if calls == 1 {
				return exitErr(pgCopyNotDone)
			}
			return nil
		}}

		require.NoError(t, migrateLegacyPostgres(checker, &bytes.Buffer{}, file, dir, "pw"))
		assert.Less(t, f.index(t, "volume rm "+pgVolume), f.index(t, "stop neutree-core"))
		assert.Equal(t, 2, calls)
		assert.True(t, strings.HasSuffix(f.calls[len(f.calls)-2], "pg-copy.sh copy"))
	})

	t.Run("finished copy is reused", func(t *testing.T) {
		f := legacyHost().on("volume inspect "+pgVolume, "[]", nil)
		require.NoError(t, migrateLegacyPostgres(f, &bytes.Buffer{}, file, dir, "pw"))

		var actions []string
		for _, c := range f.calls {
			if strings.HasPrefix(c, "run --rm --name "+pgMigrationRunner) {
				actions = append(actions, c[strings.LastIndex(c, " ")+1:])
			}
		}

		assert.Equal(t, []string{"check", "check"}, actions)
		assert.False(t, f.has("volume rm"))
	})

	t.Run("rolled back source is refused", func(t *testing.T) {
		f := legacyHost().
			on("volume inspect "+pgVolume, "[]", nil).
			on("run --rm --name "+pgMigrationRunner, "", exitErr(pgCopySourceChanged))

		err := migrateLegacyPostgres(f, &bytes.Buffer{}, file, dir, "pw")
		require.ErrorContains(t, err, "docker volume rm "+pgVolume)
		assert.False(t, f.has("stop "), "nothing stops before the refusal")
		assert.Equal(t, "rm -f "+pgMigrationTarget, f.calls[len(f.calls)-1])
	})
}

type countingDocker struct {
	*fakeDocker
	onRunner func() error
}

func (c *countingDocker) Execute(ctx context.Context, name string, args []string) ([]byte, error) {
	out, err := c.fakeDocker.Execute(ctx, name, args)
	if strings.HasPrefix(strings.Join(args, " "), "run --rm --name "+pgMigrationRunner) {
		return out, c.onRunner()
	}

	return out, err
}

func TestPrepareNeutreeCoreDeployConfigRendersPostgres17(t *testing.T) {
	workDir := t.TempDir()
	options := neutreeCoreInstallOptions{
		commonOptions: &commonOptions{
			workDir:        workDir,
			nodeIP:         "192.168.1.1",
			deployType:     constants.DeployTypeLocal,
			deployMode:     constants.DeployModeSingle,
			mirrorRegistry: "mirror.example.com",
		},
		jwtSecret:  "test-secret",
		dbPassword: "secret-pw",
		version:    "v1.0.0",
	}

	require.NoError(t, prepareNeutreeCoreDeployConfig(options))

	coreWorkDir := filepath.Join(workDir, "neutree-core")
	m := &pgMigration{composeFile: filepath.Join(coreWorkDir, "docker-compose.yml")}
	require.NoError(t, m.loadComposeService())
	assert.Equal(t, "mirror.example.com/library/postgres:17", m.image)
	assert.Equal(t, "secret-pw", m.env["POSTGRES_PASSWORD"])

	project, err := cli.ProjectFromOptions(&cli.ProjectOptions{ConfigPaths: []string{m.composeFile}})
	require.NoError(t, err)

	service, err := project.GetService(pgContainer)
	require.NoError(t, err)
	require.Equal(t, pgDataDir, service.Volumes[0].Target)
	// The migration addresses the volumes by their compose-project names.
	assert.Equal(t, strings.TrimPrefix(pgVolume, neutreeCoreComposeProject+"_"), service.Volumes[0].Source)
	assert.Contains(t, project.Volumes, "pg17_data")
	assert.NotContains(t, project.Volumes, "pg_data", "the old volume must survive compose down -v")

	for _, f := range []string{"pg-copy.sh", "inventory.sql"} {
		assert.FileExists(t, filepath.Join(coreWorkDir, "db-migration", f))
	}
}
