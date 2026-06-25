import importlib.util
import sys
import types
import unittest
from pathlib import Path
from unittest import mock


def load_bridge_module():
    ray_module = types.ModuleType("ray")
    serve_module = types.SimpleNamespace(get_replica_context=lambda: None)
    util_module = types.ModuleType("ray.util")
    metrics_module = types.ModuleType("ray.util.metrics")
    metrics_module.Counter = object
    metrics_module.Gauge = object
    util_module.metrics = metrics_module
    ray_module.serve = serve_module
    ray_module.util = util_module

    with mock.patch.dict(
        sys.modules,
        {
            "ray": ray_module,
            "ray.util": util_module,
            "ray.util.metrics": metrics_module,
        },
    ):
        module_path = Path(__file__).with_name("sglang_ray_bridge.py")
        spec = importlib.util.spec_from_file_location("sglang_ray_bridge_test", module_path)
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module


class SGLangRayBridgeTagsTest(unittest.TestCase):
    def test_replica_tags_include_engine_metadata(self):
        bridge = load_bridge_module()
        ctx = types.SimpleNamespace(
            deployment="Backend",
            replica_tag="Backend#abc",
            app_name="endpoint-a",
        )

        with mock.patch.object(bridge.serve, "get_replica_context", return_value=ctx), \
                mock.patch.dict(
                    "os.environ",
                    {"ENGINE_NAME": "sglang", "ENGINE_VERSION": "v0.5.10"},
                    clear=False,
                ):
            self.assertEqual(
                {
                    "deployment": "Backend",
                    "replica": "Backend#abc",
                    "application": "endpoint-a",
                    "engine": "sglang",
                    "engine_version": "v0.5.10",
                },
                bridge._replica_tags(),
            )

    def test_replica_tags_are_empty_outside_serve_context(self):
        bridge = load_bridge_module()

        with mock.patch.object(
            bridge.serve,
            "get_replica_context",
            side_effect=RuntimeError("outside serve"),
        ):
            self.assertEqual({}, bridge._replica_tags())


if __name__ == "__main__":
    unittest.main()
