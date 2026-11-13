package launch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/cli"
	"github.com/compose-spec/compose-go/loader"
	"github.com/pkg/errors"

	"github.com/neutree-ai/neutree/pkg/command"
)

// LegacyPostgresVolume holds the PostgreSQL 13 data of releases before
// PostgreSQL 17. An upgrade copies it and leaves it in place for a rollback.
const LegacyPostgresVolume = neutreeCoreComposeProject + "_pg_data"

const (
	neutreeCoreComposeProject = "neutree-core"

	pgContainer    = "postgres"
	pgDataDir      = "/var/lib/postgresql/data"
	pgLegacyVolume = LegacyPostgresVolume
	pgVolume       = neutreeCoreComposeProject + "_pg17_data"

	pgMigrationTarget = "neutree-pg-migration-target"
	pgMigrationRunner = "neutree-pg-migration"

	// pg-copy.sh check exit codes.
	pgCopyNotDone       = 3
	pgCopySourceChanged = 4
)

// pgMigrationWriters are stopped while the database is copied, in this order.
// Kong keeps serving from its cache and only reads the database.
var pgMigrationWriters = []string{"neutree-core", "neutree-api", "postgrest", "auth"}

type pgMigration struct {
	exec        command.Executor
	out         io.Writer
	composeFile string
	coreWorkDir string
	dbPassword  string

	image   string
	env     map[string]string
	network string
	envFile string
}

// migrateLegacyPostgres copies the PostgreSQL 13 database of an earlier
// release into the PostgreSQL 17 volume of this release before compose
// switches to it. The old volume is left untouched for a rollback.
func migrateLegacyPostgres(exec command.Executor, out io.Writer, composeFile, coreWorkDir, dbPassword string) error {
	m := &pgMigration{exec: exec, out: out, composeFile: composeFile, coreWorkDir: coreWorkDir, dbPassword: dbPassword}

	needed, err := m.needed()
	if err != nil || !needed {
		return err
	}

	if err := m.loadComposeService(); err != nil {
		return err
	}

	m.envFile, err = m.writeEnvFile()
	if err != nil {
		return err
	}

	defer os.Remove(m.envFile)

	return m.run()
}

func (m *pgMigration) docker(args ...string) (string, error) {
	output, err := m.exec.Execute(context.Background(), "docker", args)
	if err != nil {
		return string(output), errors.Wrapf(err, "docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}

	return strings.TrimSpace(string(output)), nil
}

func (m *pgMigration) compose(args ...string) (string, error) {
	return m.docker(append([]string{"compose", "-p", neutreeCoreComposeProject, "-f", m.composeFile}, args...)...)
}

func (m *pgMigration) logf(format string, args ...interface{}) {
	fmt.Fprintf(m.out, "%s "+format+"\n", append([]interface{}{time.Now().UTC().Format(time.RFC3339)}, args...)...)
}

// needed reports whether the running deployment still uses the volume of an
// earlier release.
func (m *pgMigration) needed() (bool, error) {
	volume, err := m.docker("inspect", "-f",
		`{{range .Mounts}}{{if eq .Destination "`+pgDataDir+`"}}{{.Name}}{{end}}{{end}}`, pgContainer)
	if err != nil {
		// No database container. A volume of an earlier release without a
		// copy next to it means the deployment was taken down before upgrading.
		_, legacyErr := m.docker("volume", "inspect", pgLegacyVolume)
		_, currentErr := m.docker("volume", "inspect", pgVolume)

		if legacyErr == nil && currentErr != nil {
			return false, fmt.Errorf("found volume %s of an earlier release but no %s container to copy it from; "+
				"start the earlier release again (its neutree-cli launch) and then upgrade, "+
				"or, if that data is not needed, run `docker volume rm %s`", pgLegacyVolume, pgContainer, pgLegacyVolume)
		}

		return false, nil
	}

	return volume == pgLegacyVolume, nil
}

func (m *pgMigration) loadComposeService() error {
	options, err := cli.NewProjectOptions([]string{m.composeFile}, cli.WithLoadOptions(func(o *loader.Options) {
		o.SkipInterpolation = true
	}))
	if err != nil {
		return errors.Wrapf(err, "load docker compose file %s", m.composeFile)
	}

	project, err := cli.ProjectFromOptions(options)
	if err != nil {
		return errors.Wrapf(err, "analyze docker compose file %s", m.composeFile)
	}

	service, err := project.GetService(pgContainer)
	if err != nil {
		return errors.Wrapf(err, "find service %s in %s", pgContainer, m.composeFile)
	}

	m.image = service.Image
	m.env = map[string]string{}

	for k, v := range service.Environment {
		if v != nil {
			m.env[k] = *v
		}
	}

	return nil
}

// writeEnvFile keeps passwords off docker command lines.
func (m *pgMigration) writeEnvFile() (string, error) {
	env := map[string]string{
		"PGUSER":              m.env["POSTGRES_USER"],
		"PGPASSWORD":          m.dbPassword,
		"DB_NAME":             m.env["POSTGRES_DB"],
		"AUTH_ADMIN_PASSWORD": "auth_admin_password",
		"KONG_ADMIN_PASSWORD": "kong_admin_password",
	}
	for k, v := range m.env {
		env[k] = v
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}

	// A killed earlier run cannot have removed its file.
	stale, _ := filepath.Glob(filepath.Join(m.coreWorkDir, ".pg-migration-*.env"))
	for _, f := range stale {
		_ = os.Remove(f)
	}

	f, err := os.CreateTemp(m.coreWorkDir, ".pg-migration-*.env")
	if err != nil {
		return "", errors.Wrap(err, "create migration env file")
	}
	defer f.Close()

	if _, err := f.WriteString(b.String()); err != nil {
		return "", errors.Wrap(err, "write migration env file")
	}

	return f.Name(), nil
}

func (m *pgMigration) run() error {
	m.logf("the database runs on volume %s (PostgreSQL 13); copying it to %s (PostgreSQL 17)", pgLegacyVolume, pgVolume)

	network, err := m.docker("inspect", "-f", `{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}`, pgContainer)
	if err != nil {
		return err
	}

	m.network = network

	if _, err := m.docker("start", pgContainer); err != nil {
		return err
	}

	// Everything slow happens before writes stop: images, the new scripts and
	// a running target.
	m.logf("pulling images")

	// Only missing images, as up would: refreshing a moved tag would make up
	// recreate containers the upgrade does not otherwise change.
	if out, err := m.compose("pull", "--policy", "missing", "--ignore-pull-failures", "--quiet"); err != nil {
		m.logf("pull failed, continuing with local images: %s", out)
	}

	if _, err := m.compose("run", "--rm", "--no-deps", "copy-db-scripts"); err != nil {
		return err
	}

	defer func() {
		_, _ = m.docker("rm", "-f", pgMigrationTarget)
	}()

	copied, err := m.startTarget()
	if err != nil {
		return err
	}

	if err := m.freeze(); err != nil {
		m.unfreeze()
		return err
	}

	frozenAt := time.Now()

	if copied {
		// A previous run finished the copy but did not switch; make sure
		// nothing was written since.
		err = m.runner(frozenAt, "check")
	} else {
		m.logf("copying the database")
		err = m.runner(frozenAt, "copy")
	}

	if err != nil {
		m.unfreeze()
		return errors.Wrap(err, "database migration failed; the deployment keeps running on the earlier database")
	}

	m.logf("database copied in %s; switching to PostgreSQL 17", time.Since(frozenAt).Round(time.Second))

	return nil
}

// startTarget starts PostgreSQL 17 on the new volume next to the running
// database and reports whether it already holds a finished copy.
func (m *pgMigration) startTarget() (bool, error) {
	_, _ = m.docker("rm", "-f", pgMigrationTarget)

	if _, err := m.docker("volume", "inspect", pgVolume); err == nil {
		if err := m.runTarget(); err != nil {
			return false, err
		}

		err := m.runner(time.Now(), "check")
		switch exitCode(err) {
		case 0:
			return true, nil
		case pgCopyNotDone:
			// Only a failed attempt; it never served.
			m.logf("discarding the unfinished copy in %s", pgVolume)

			if _, err := m.docker("rm", "-f", pgMigrationTarget); err != nil {
				return false, err
			}

			if _, err := m.docker("volume", "rm", pgVolume); err != nil {
				return false, err
			}
		case pgCopySourceChanged:
			return false, fmt.Errorf("the earlier database took writes after it was copied to %s (was the release rolled back?); "+
				"to copy it again, run `docker volume rm %s` (its data is discarded) and launch again", pgVolume, pgVolume)
		default:
			return false, err
		}
	}

	// Labelled as compose would label it, so compose adopts it.
	if _, err := m.docker("volume", "create",
		"--label", "com.docker.compose.project="+neutreeCoreComposeProject,
		"--label", "com.docker.compose.volume="+strings.TrimPrefix(pgVolume, neutreeCoreComposeProject+"_"),
		pgVolume); err != nil {
		return false, err
	}

	return false, m.runTarget()
}

func (m *pgMigration) runTarget() error {
	_, err := m.docker("run", "-d", "--name", pgMigrationTarget, "--network", m.network,
		"--env-file", m.envFile,
		"-v", pgVolume+":"+pgDataDir,
		"-v", filepath.Join(m.coreWorkDir, "db", "init-scripts")+":/docker-entrypoint-initdb.d",
		m.image)

	return err
}

func (m *pgMigration) runner(frozenAt time.Time, action string) error {
	_, _ = m.docker("rm", "-f", pgMigrationRunner)

	args := []string{"run", "--rm", "--name", pgMigrationRunner, "--network", m.network,
		"--env-file", m.envFile,
		"-e", "SOURCE_ID=" + pgLegacyVolume,
		"-e", "SOURCE_HOST=" + pgContainer,
		"-e", "TARGET_HOST=" + pgMigrationTarget,
		"-e", "FROZEN_AT=" + strconv.FormatInt(frozenAt.Unix(), 10),
		"-e", "WORK=/tmp/pg-migration",
		"-v", filepath.Join(m.coreWorkDir, "db-migration") + ":/scripts:ro",
		m.image, "bash", "/scripts/pg-copy.sh", action}

	// The script logs progress on stdout; its errors are on stderr, in err.
	out, err := m.exec.Execute(context.Background(), "docker", args)
	if len(out) > 0 {
		fmt.Fprint(m.out, string(out))
	}

	return errors.Wrapf(err, "pg-copy.sh %s", action)
}

// freeze stops the containers that write to the database.
func (m *pgMigration) freeze() error {
	m.logf("stopping writers: %s", strings.Join(pgMigrationWriters, ", "))

	for _, c := range pgMigrationWriters {
		state, err := m.docker("inspect", "-f", "{{.State.Running}}", c)
		if err != nil || state != "true" {
			continue
		}

		if _, err := m.docker("stop", c); err != nil {
			return err
		}
	}

	return nil
}

// unfreeze starts every writer, including ones an interrupted earlier run
// left stopped.
func (m *pgMigration) unfreeze() {
	for i := len(pgMigrationWriters) - 1; i >= 0; i-- {
		c := pgMigrationWriters[i]
		if _, err := m.docker("inspect", c); err != nil {
			continue
		}

		if _, err := m.docker("start", c); err != nil {
			m.logf("restart %s: %v", c, err)
		}
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}
