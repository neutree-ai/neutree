#!/usr/bin/env python3
"""Exercise the migration guard against real Git index/commit/merge states."""
import pathlib
import subprocess
import tempfile
import unittest

GUARD = pathlib.Path(__file__).with_name('check-migrations.sh').resolve()


class MigrationGuardTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.git('init', '-q', '-b', 'main')
        self.git('config', 'user.email', 'test@example.com')
        self.git('config', 'user.name', 'Migration Test')
        self.git('config', 'core.hooksPath', '/dev/null')
        (self.root / 'db/migrations').mkdir(parents=True)
        self.pair('009_base')
        self.update()
        self.commit()

    def run_command(self, *args):
        return subprocess.run(args, cwd=self.root, text=True, capture_output=True)

    def git(self, *args):
        result = self.run_command('git', *args)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def pair(self, name, stage=True):
        for direction in ('up', 'down'):
            path = f'db/migrations/{name}.{direction}.sql'
            (self.root / path).write_text('-- migration\n')
            if stage:
                self.git('add', path)

    def guard(self, *args, success=True):
        result = self.run_command('bash', str(GUARD), *args)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result.stdout

    def update(self):
        self.guard('--update-latest')

    def commit(self):
        self.git('commit', '-qm', 'fixture')

    def test_index_generation_and_commit_validation(self):
        self.pair('010_staged')
        self.pair('999_untracked', stage=False)
        self.update()
        self.assertEqual(self.git('show', ':db/migrations/LATEST'), '010_staged')
        self.guard()
        self.commit()
        # CI must read HEAD, even when the worktree marker is wrong.
        (self.root / 'db/migrations/LATEST').write_text('wrong\n')
        self.guard('main')

    def test_missing_or_stale_marker_rejected(self):
        self.pair('010_new')
        self.assertIn('LATEST must contain: 010_new', self.guard(success=False))
        self.commit()
        self.guard('HEAD~1', success=False)
        self.update()
        self.git('rm', '-f', 'db/migrations/LATEST')
        self.guard(success=False)

    def test_numeric_order_and_exact_single_line(self):
        self.pair('10_new')
        self.update()
        self.assertEqual(self.git('show', ':db/migrations/LATEST'), '10_new')
        (self.root / 'db/migrations/LATEST').write_text('10_new\n\n')
        self.git('add', 'db/migrations/LATEST')
        self.guard(success=False)

    def test_concurrent_additions_conflict(self):
        self.git('checkout', '-qb', 'a')
        self.pair('010_a')
        self.update()
        self.commit()
        self.guard('main')
        self.git('checkout', '-qb', 'b', 'main')
        self.pair('010_b')
        self.update()
        self.guard()
        self.commit()
        result = self.run_command('git', 'merge', 'a')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.git('diff', '--name-only', '--diff-filter=U'), 'db/migrations/LATEST')
        # Resolving only the marker must not hide duplicate versions.
        self.update()
        self.assertIn('same version number', self.guard(success=False))

    def test_ablation_without_marker_updates_merges_duplicate_versions(self):
        # Removing marker updates loses the Git-level collision signal.
        self.git('checkout', '-qb', 'a')
        self.pair('010_a')
        self.commit()
        self.git('checkout', '-qb', 'b', 'main')
        self.pair('010_b')
        self.commit()
        self.git('merge', '--no-edit', 'a')
        self.assertEqual(self.git('diff', '--name-only', '--diff-filter=U'), '')
        self.assertIn('same version number', self.guard('main', success=False))

    def test_ci_marker_uses_head_not_newer_base(self):
        self.git('checkout', '-qb', 'feature')
        self.git('checkout', 'main')
        self.pair('010_base')
        self.update()
        self.commit()
        self.git('checkout', 'feature')
        self.guard('main')


if __name__ == '__main__':
    unittest.main()
