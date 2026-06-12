import importlib.util
import json
from pathlib import Path
import unittest
from unittest import mock


def load_start_module():
    module_path = Path(__file__).with_name("start.py")
    spec = importlib.util.spec_from_file_location("cluster_image_builder_start", module_path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TestRayStartCommand(unittest.TestCase):
    def test_main_disables_prestarted_runtime_env_workers(self):
        start = load_start_module()
        calls = []

        def fake_run(cmd, check):
            calls.append((cmd, check))

        with mock.patch.dict(start.os.environ, {}, clear=True):
            with mock.patch.object(start.sys, "argv", ["start.py", "--head"]):
                with mock.patch.object(start.subprocess, "run", fake_run):
                    start.main()

        self.assertEqual(len(calls), 1)
        cmd, check = calls[0]
        self.assertTrue(check)

        system_config_index = cmd.index("--system-config") + 1
        self.assertEqual(
            json.loads(cmd[system_config_index]),
            {
                "prestart_worker_first_driver": False,
                "num_workers_soft_limit": 0,
            },
        )
        self.assertIn("--head", cmd)


if __name__ == "__main__":
    unittest.main()
