import os
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

from prometheus_multiproc import (
    ensure_prometheus_multiproc_dir,
    install_stable_prometheus_multiproc_dir,
)


class PrometheusMultiprocDirTest(unittest.TestCase):
    def test_creates_pid_scoped_dir_and_sets_env(self):
        with tempfile.TemporaryDirectory() as tmpdir, \
                mock.patch.dict(os.environ, {}, clear=True), \
                mock.patch("os.getpid", return_value=1234):
            metrics_dir = ensure_prometheus_multiproc_dir(
                base_dir=tmpdir,
                namespace="sglang",
            )

            self.assertEqual(str(Path(tmpdir) / "sglang" / "1234"), metrics_dir)
            self.assertTrue(Path(metrics_dir).is_dir())
            self.assertEqual(metrics_dir, os.environ["PROMETHEUS_MULTIPROC_DIR"])

    def test_clears_stale_pid_dir(self):
        with tempfile.TemporaryDirectory() as tmpdir, \
                mock.patch.dict(os.environ, {}, clear=True), \
                mock.patch("os.getpid", return_value=1234):
            stale_file = Path(tmpdir) / "sglang" / "1234" / "gauge.db"
            stale_file.parent.mkdir(parents=True)
            stale_file.write_text("stale")

            metrics_dir = ensure_prometheus_multiproc_dir(
                base_dir=tmpdir,
                namespace="sglang",
            )

            self.assertTrue(Path(metrics_dir).is_dir())
            self.assertFalse(stale_file.exists())

    def test_existing_env_is_preserved(self):
        with tempfile.TemporaryDirectory() as tmpdir, \
                mock.patch.dict(
                    os.environ,
                    {"PROMETHEUS_MULTIPROC_DIR": str(Path(tmpdir) / "custom")},
                    clear=True,
                ):
            existing_file = Path(tmpdir) / "custom" / "gauge.db"
            existing_file.parent.mkdir(parents=True)
            existing_file.write_text("keep")

            metrics_dir = ensure_prometheus_multiproc_dir(
                base_dir=tmpdir,
                namespace="sglang",
            )

            self.assertEqual(str(Path(tmpdir) / "custom"), metrics_dir)
            self.assertTrue(existing_file.exists())

    def test_installs_stable_helper_on_sglang_modules(self):
        common_module = types.SimpleNamespace()
        engine_module = types.SimpleNamespace()

        with tempfile.TemporaryDirectory() as tmpdir, \
                mock.patch.dict(os.environ, {}, clear=True), \
                mock.patch("os.getpid", return_value=1234):
            metrics_dir = install_stable_prometheus_multiproc_dir(
                common_module=common_module,
                engine_module=engine_module,
                base_dir=tmpdir,
                namespace="sglang",
            )

            self.assertEqual(str(Path(tmpdir) / "sglang" / "1234"), metrics_dir)
            self.assertEqual(metrics_dir, common_module.set_prometheus_multiproc_dir())
            self.assertEqual(metrics_dir, engine_module.set_prometheus_multiproc_dir())
            self.assertEqual(metrics_dir, os.environ["PROMETHEUS_MULTIPROC_DIR"])


if __name__ == "__main__":
    unittest.main()
