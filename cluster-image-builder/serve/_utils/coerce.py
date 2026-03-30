"""Shared utilities for Neutree Ray Serve applications."""

import dataclasses
import json
import logging
import typing
from typing import Any, Dict, Union

logger = logging.getLogger("ray.serve")


def _get_field_annotations(model_class: type) -> Dict[str, Any]:
    """Extract field name → annotation mapping from Pydantic models or standard dataclasses."""
    # Pydantic model / pydantic dataclass
    pydantic_fields = getattr(model_class, '__pydantic_fields__', None)
    if pydantic_fields is not None:
        return {name: info.annotation for name, info in pydantic_fields.items()}

    # Standard Python dataclass
    if dataclasses.is_dataclass(model_class):
        try:
            return typing.get_type_hints(model_class)
        except Exception:
            return {}

    return {}


def _wants_dict_or_list(annotation: Any) -> bool:
    """Return True if the annotation expects a dict or list type."""
    origin = getattr(annotation, '__origin__', None)

    # Unwrap Optional[X] (Union[X, None]) → X
    if origin is Union:
        type_args = [a for a in annotation.__args__ if a is not type(None)]
        if len(type_args) == 1:
            annotation = type_args[0]
            origin = getattr(annotation, '__origin__', None)

    return origin in (dict, list) or annotation in (dict, list)


def coerce_args(args: Dict[str, Any], model_class: type) -> None:
    """Coerce JSON string values to native types based on field annotations.

    On the SSH/Ray path, values arrive via JSON from the Go control plane. Users
    may provide complex values as JSON strings (e.g. '{"temperature": 0.5}')
    rather than native objects. Unlike the K8s CLI path (where argparse type
    converters handle json.loads on specific fields), the Ray Dashboard API passes
    values through as-is.

    This function inspects each field's type annotation and converts JSON strings
    only for fields that expect dict or list types, leaving all other values
    untouched.

    Supports both Pydantic models/dataclasses and standard Python dataclasses.

    Args:
        args: Mutable dict of keyword arguments to coerce in place.
        model_class: A Pydantic model, Pydantic dataclass, or standard dataclass
            whose field annotations drive the conversion decisions.
    """
    annotations = _get_field_annotations(model_class)
    if not annotations:
        return

    for field_name, annotation in annotations.items():
        if field_name not in args or not isinstance(args[field_name], str):
            continue

        if _wants_dict_or_list(annotation):
            try:
                parsed = json.loads(args[field_name])
            except (json.JSONDecodeError, TypeError):
                continue
            if isinstance(parsed, (dict, list)):
                args[field_name] = parsed


def filter_engine_args(args: Dict[str, Any], engine_args_class: type) -> None:
    """Remove keys from *args* that are not recognised by *engine_args_class*.

    This prevents ``AsyncEngineArgs(**args)`` from crashing with a TypeError
    when the dict contains serving-only or otherwise unknown parameters.

    Mutates *args* in place.  Unknown keys are logged as warnings.
    """
    known_fields = set(_get_field_annotations(engine_args_class).keys())
    if not known_fields:
        logger.warning(
            "filter_engine_args: could not introspect %r — skipping unknown-key filter",
            engine_args_class,
        )
        return
    unknown = [k for k in args if k not in known_fields]
    for key in unknown:
        value = args.pop(key)
        logger.warning("Unknown engine parameter %r=%r ignored", key, value)
