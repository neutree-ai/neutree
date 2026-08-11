"""Legacy v1.1.1 import entrypoint for the vLLM native image."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from ray.serve import Application

from serve.native_engine.app import app_builder as native_app_builder
from serve.native_engine.vllm_v0_24_0_compat import adapt_vllm_v0_24_0_args


def app_builder(args: Mapping[str, Any]) -> Application:
    return native_app_builder(adapt_vllm_v0_24_0_args(args))
