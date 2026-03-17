"""Shared utilities for Neutree Ray Serve applications."""

import json
from typing import Any, Dict, Union


def coerce_pydantic_args(args: Dict[str, Any], model_class: type) -> None:
    """Coerce JSON string values to native types based on Pydantic field annotations.

    On the SSH/Ray path, values arrive via JSON from the Go control plane. Users
    may provide complex values as JSON strings (e.g. '{"temperature": 0.5}')
    rather than native objects. Unlike the K8s CLI path (where argparse type
    converters handle json.loads on specific fields), the Ray Dashboard API passes
    values through as-is.

    This function inspects each field's type annotation and converts JSON strings
    only for fields that expect dict or list types, leaving all other values
    untouched.

    Args:
        args: Mutable dict of keyword arguments to coerce in place.
        model_class: A Pydantic model or dataclass whose field annotations drive
            the conversion decisions.
    """
    pydantic_fields = getattr(model_class, '__pydantic_fields__', None)
    if pydantic_fields is None:
        return

    for field_name, field_info in pydantic_fields.items():
        if field_name not in args or not isinstance(args[field_name], str):
            continue

        annotation = field_info.annotation
        # Unwrap Optional[X] (Union[X, None]) → X
        origin = getattr(annotation, '__origin__', None)
        if origin is Union:
            type_args = [a for a in annotation.__args__ if a is not type(None)]
            if len(type_args) == 1:
                annotation = type_args[0]
                origin = getattr(annotation, '__origin__', None)

        if origin in (dict, list) or annotation in (dict, list):
            try:
                parsed = json.loads(args[field_name])
            except (json.JSONDecodeError, TypeError):
                continue
            if isinstance(parsed, (dict, list)):
                args[field_name] = parsed
