"""Pure configuration helpers for the generic native engine application."""

from __future__ import annotations

import json
from collections.abc import Mapping
from typing import Any

_RUNNER_OWNED_ARGS = {"model", "served_model_name", "host", "port"}


def build_engine_args(
    *,
    runtime: Mapping[str, Any],
    model: Mapping[str, Any],
    engine_args: Mapping[str, Any],
    port: int,
) -> list[str]:
    """Convert API engine arguments to an argv for the raw engine image."""
    command = [str(value) for value in (runtime.get("command") or [])]
    model_path = str(model.get("path", ""))
    serve_name = str(model.get("serve_name", ""))
    if not model_path:
        raise ValueError("native engine model.path is required")
    command.extend(["--model", model_path])
    if serve_name:
        command.extend(["--served-model-name", serve_name])
    command.extend(["--host", "127.0.0.1", "--port", str(port)])
    for key in sorted(engine_args):
        normalized_key = key.replace("-", "_")
        if normalized_key in _RUNNER_OWNED_ARGS:
            continue
        value = engine_args[key]
        flag = "--" + key.replace("_", "-")
        if isinstance(value, str) and value.lower() in {"true", "false"}:
            value = value.lower() == "true"
        if value is True:
            command.append(flag)
        elif value is False or value is None:
            continue
        elif isinstance(value, (dict, list)):
            command.extend([flag, json.dumps(value, separators=(",", ":"))])
        else:
            command.extend([flag, str(value)])
    return command
