"""Pure configuration helpers for the generic native engine application."""

from __future__ import annotations

import json
from collections.abc import Mapping
from typing import Any

_RUNNER_OWNED_ARGS = {"model", "host", "port"}
_DEFAULT_STARTUP_TIMEOUT_S = 1200.0


def startup_timeout_s(runtime: Mapping[str, Any]) -> float:
    """Use the Kubernetes-equivalent startup window unless CP overrides it."""
    value = runtime.get("startup_timeout_seconds", _DEFAULT_STARTUP_TIMEOUT_S)
    try:
        timeout_s = float(value)
    except (TypeError, ValueError) as exc:
        raise ValueError("native engine startup_timeout_seconds must be a number") from exc
    if timeout_s <= 0:
        raise ValueError("native engine startup_timeout_seconds must be positive")
    return timeout_s


def build_engine_args(
    *,
    runtime: Mapping[str, Any],
    model: Mapping[str, Any],
    engine_args: Mapping[str, Any],
    port: int,
) -> list[str]:
    """Convert API engine arguments to an argv for a direct engine process."""
    command = [str(value) for value in (runtime.get("command") or [])]
    if not command:
        raise ValueError("native engine runtime.command is required for a direct server")
    model_path = str(model.get("path", ""))
    serve_name = str(model.get("serve_name", ""))
    if not model_path:
        raise ValueError("native engine model.path is required")
    command.extend(["--model", model_path])
    served_model_names = _served_model_names(serve_name, engine_args)
    if served_model_names:
        command.extend(["--served-model-name", *served_model_names])
    command.extend(["--host", "127.0.0.1", "--port", str(port)])
    for key in sorted(engine_args):
        normalized_key = key.replace("-", "_")
        if normalized_key in _RUNNER_OWNED_ARGS or normalized_key == "served_model_name":
            continue
        value = engine_args[key]
        flag = "--" + key.replace("_", "-")
        if isinstance(value, str) and value.lower() in {"true", "false"}:
            value = value.lower() == "true"
        if value is True:
            command.append(flag)
        elif value is False or value is None:
            continue
        elif isinstance(value, list):
            if value:
                command.extend([flag, *(_cli_value(item) for item in value)])
        elif isinstance(value, dict):
            command.extend([flag, json.dumps(value, separators=(",", ":"))])
        else:
            command.extend([flag, _cli_value(value)])
    return command


def _served_model_names(serve_name: str, engine_args: Mapping[str, Any]) -> list[str]:
    """Return the endpoint model name followed by unique user aliases."""
    names = [serve_name] if serve_name else []
    for key, value in engine_args.items():
        if key.replace("-", "_") != "served_model_name":
            continue
        values = value if isinstance(value, list) else [value]
        for name in values:
            if name is not None and str(name) not in names:
                names.append(str(name))
    return names


def _cli_value(value: Any) -> str:
    if isinstance(value, bool):
        return str(value).lower()
    if isinstance(value, (dict, list)):
        return json.dumps(value, separators=(",", ":"))
    return str(value)
