"""Tests for serve._utils.build_backend_runtime_env."""

import sys
from unittest.mock import MagicMock

import pytest

# Provide a mock 'ray' module before importing the function under test.
mock_ray = MagicMock()
sys.modules.setdefault("ray", mock_ray)

from serve._utils.runtime_env import build_backend_runtime_env  # noqa: E402


@pytest.fixture(autouse=True)
def _reset_ray_mock():
    mock_ray.reset_mock()


class TestBuildBackendRuntimeEnv:
    def test_includes_container(self):
        mock_ray.get_runtime_context.return_value.runtime_env.get.return_value = None
        container = {"image": "my-image:latest"}
        result = build_backend_runtime_env(container)
        assert result["container"] == container

    def test_propagates_app_env_vars(self):
        env_vars = {"HF_TOKEN": "tok-123", "ENGINE_ID": "e1"}
        mock_ray.get_runtime_context.return_value.runtime_env.get.return_value = env_vars

        container = {"image": "my-image:latest"}
        result = build_backend_runtime_env(container)
        assert result["container"] == container
        assert result["env_vars"] == env_vars

    def test_no_env_vars_in_app_runtime(self):
        mock_ray.get_runtime_context.return_value.runtime_env.get.return_value = None

        result = build_backend_runtime_env({"image": "img"})
        assert "env_vars" not in result

    def test_handles_attribute_error(self):
        mock_ray.get_runtime_context.side_effect = AttributeError("no ctx")

        result = build_backend_runtime_env({"image": "img"})
        assert result == {"container": {"image": "img"}}

    def test_handles_key_error(self):
        mock_ray.get_runtime_context.side_effect = KeyError("missing")

        result = build_backend_runtime_env({"image": "img"})
        assert result == {"container": {"image": "img"}}
