import importlib.util
import sys
import types
import unittest
from pathlib import Path


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "serve"
    / "_metrics"
    / "ray_stat_logger.py"
)


class _MetricWrapper:
    pass


class _RayPrometheusStatLogger:
    def __init__(self, *args, **kwargs):
        pass


def _install_fake_modules(kv_connector_cls_name):
    ray_module = types.ModuleType("ray")
    ray_module.serve = types.SimpleNamespace(
        get_replica_context=lambda: (_ for _ in ()).throw(RuntimeError())
    )
    sys.modules["ray"] = ray_module

    for name in [
        "vllm",
        "vllm.v1",
        "vllm.v1.metrics",
    ]:
        sys.modules[name] = types.ModuleType(name)

    ray_wrappers = types.ModuleType("vllm.v1.metrics.ray_wrappers")
    ray_wrappers.RayPrometheusStatLogger = _RayPrometheusStatLogger
    ray_wrappers.RaySpecDecodingProm = type("RaySpecDecodingProm", (), {})
    ray_wrappers.RayGaugeWrapper = _MetricWrapper
    ray_wrappers.RayCounterWrapper = _MetricWrapper
    ray_wrappers.RayHistogramWrapper = _MetricWrapper
    setattr(ray_wrappers, kv_connector_cls_name, type(kv_connector_cls_name, (), {}))
    sys.modules["vllm.v1.metrics.ray_wrappers"] = ray_wrappers


def _clear_fake_modules():
    for name in list(sys.modules):
        if name == "ray" or name.startswith("vllm"):
            sys.modules.pop(name, None)


def _load_ray_stat_logger(kv_connector_cls_name):
    _clear_fake_modules()
    _install_fake_modules(kv_connector_cls_name)
    module_name = f"ray_stat_logger_under_test_{kv_connector_cls_name}"
    sys.modules.pop(module_name, None)
    spec = importlib.util.spec_from_file_location(module_name, MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


class RayStatLoggerImportTest(unittest.TestCase):
    def tearDown(self):
        _clear_fake_modules()

    def test_imports_with_vllm_0_22_kv_connector_class_name(self):
        module = _load_ray_stat_logger("RayKVConnectorProm")

        self.assertTrue(hasattr(module, "NeutreeRayStatLogger"))

    def test_imports_with_older_kv_connector_class_name(self):
        module = _load_ray_stat_logger("RayKVConnectorPrometheus")

        self.assertTrue(hasattr(module, "NeutreeRayStatLogger"))


if __name__ == "__main__":
    unittest.main()
