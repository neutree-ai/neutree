"""Adapt the v1.1.1 vLLM app-builder contract to the native runner."""

from __future__ import annotations

import os
from collections.abc import Mapping
from typing import Any

_PORT_STATE_MOUNT = "-v /tmp/neutree/ports:/var/run/neutree/ports"
_VLLM_RUN_OPTIONS = ("--ipc=host", _PORT_STATE_MOUNT)
_VLLM_RUNTIME = {
    "command": ["vllm", "serve"],
    "health_path": "/health",
    "metrics_path": "/metrics",
}
_VLLM_IDENTITY = {"name": "vllm", "version": "v0.24.0"}


def adapt_vllm_v0_24_0_args(args: Mapping[str, Any]) -> dict[str, Any]:
    """Return native-runner arguments from v1.1.1's existing app contract."""
    backend_container = dict(args.get("backend_container", {}))
    if not backend_container:
        raise ValueError("vLLM native compatibility requires backend_container")

    run_options = list(backend_container.get("run_options", []))
    for option in _VLLM_RUN_OPTIONS:
        if option not in run_options:
            run_options.append(option)
    backend_container["run_options"] = run_options

    result = dict(args)
    result["backend_container"] = backend_container
    result["native_runtime"] = dict(_VLLM_RUNTIME)
    result["engine_identity"] = {
        "name": os.getenv("ENGINE_NAME", _VLLM_IDENTITY["name"]),
        "version": os.getenv("ENGINE_VERSION", _VLLM_IDENTITY["version"]),
    }
    return result
