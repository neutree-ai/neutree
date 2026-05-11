"""Shared utilities for Neutree Ray Serve applications."""

import dataclasses
import functools
import json
import logging
import re
import types
import typing
from typing import Any, Dict, Union

from pydantic import BaseModel, TypeAdapter
from pydantic import ValidationError as PydanticValidationError

logger = logging.getLogger("ray.serve")

# CPython NameError message format: "name 'X' is not defined".
# Format has been stable since at least 3.5 and is shared by PyPy.
_NAMEERROR_NAME_RE = re.compile(r"name '([^']+)' is not defined")

# Bound on the iterative recovery loop. Real engine arg classes leak at most
# a handful of TYPE_CHECKING-only aliases; 64 is an order of magnitude past
# anything realistic and still finite.
_RECOVERY_MAX_ITERS = 64


def _recover_type_hints(model_class: type) -> Dict[str, Any]:
    """Resolve a dataclass's field annotations even when ``typing.get_type_hints``
    raises ``NameError`` due to TYPE_CHECKING-only aliases that lack a runtime
    ``= Any`` fallback (e.g. vLLM v0.20.0 ``OnlineQuantizationConfigArgs``).

    Strategy: keep retrying ``get_type_hints`` with ``localns`` populated by the
    names ``NameError`` reports as missing, each bound to ``Any``. Bounded loop;
    if a non-NameError exception interrupts, or the regex fails to extract a
    name, fall back to ``dataclasses.fields()`` which preserves the keyset
    (filter_engine_args still works) but loses real type objects (coerce_args
    degrades to pass-through).
    """
    extras: Dict[str, Any] = {}
    last_err: BaseException | None = None
    for _ in range(_RECOVERY_MAX_ITERS):
        try:
            return typing.get_type_hints(model_class, localns=extras)
        except NameError as exc:
            last_err = exc
            match = _NAMEERROR_NAME_RE.search(str(exc))
            if match is None:
                break
            missing = match.group(1)
            if missing in extras:
                # Same name keeps failing — defensive break to avoid spinning
                # if get_type_hints surfaces a NameError it can't recover from
                # even with the alias supplied.
                break
            extras[missing] = Any
        except Exception as exc:
            last_err = exc
            break

    logger.warning(
        "_get_field_annotations: typing.get_type_hints(%r) recovery failed (%s); "
        "falling back to __dataclass_fields__ (coerce_args will degrade)",
        model_class,
        last_err,
    )
    try:
        return {f.name: f.type for f in dataclasses.fields(model_class)}
    except Exception:
        return {}


@functools.lru_cache(maxsize=None)
def _get_field_annotations(model_class: type) -> Dict[str, Any]:
    """Extract field name → annotation mapping from Pydantic models or standard dataclasses.

    Cached per-class: ``coerce_args`` and ``filter_engine_args`` both call this
    once per replica startup, so caching avoids redundant introspection work and
    keeps the recovery warning at most once per (class, replica). Returned dict
    is treated as read-only by callers.
    """
    # Pydantic model / pydantic dataclass
    pydantic_fields = getattr(model_class, '__pydantic_fields__', None)
    if pydantic_fields is not None:
        return {name: info.annotation for name, info in pydantic_fields.items()}

    # Standard Python dataclass
    if dataclasses.is_dataclass(model_class):
        try:
            return typing.get_type_hints(model_class)
        except NameError:
            # ``get_type_hints`` is all-or-nothing: one bad field invalidates
            # the whole result. Try to recover by feeding ``Any`` as a stub
            # for each missing name reported by NameError, then re-running.
            # When this succeeds, every other field returns a real type
            # object so coerce_args keeps full JSON-string coercion.
            return _recover_type_hints(model_class)
        except Exception as exc:
            logger.warning(
                "_get_field_annotations: typing.get_type_hints(%r) failed (%s); "
                "falling back to __dataclass_fields__",
                model_class,
                exc,
            )
            try:
                return {f.name: f.type for f in dataclasses.fields(model_class)}
            except Exception:
                return {}

    return {}


def _unwrap_optional(annotation: Any) -> Any:
    """Strip Optional[X] / X | None — handles both typing.Union and PEP 604 types.UnionType."""
    if typing.get_origin(annotation) in (Union, types.UnionType):
        non_none = [a for a in typing.get_args(annotation) if a is not type(None)]
        if len(non_none) == 1:
            return non_none[0]
    return annotation


def _wants_dict_or_list(annotation: Any) -> bool:
    """Return True if the annotation expects a dict or list type."""
    target = _unwrap_optional(annotation)
    origin = typing.get_origin(target)
    return origin in (dict, list) or target in (dict, list)


def _is_dataclass_like(tp: Any) -> bool:
    """True if *tp* is a class hydratable from JSON (Pydantic model or dataclass)."""
    if not isinstance(tp, type):
        return False
    if issubclass(tp, BaseModel):
        return True
    return dataclasses.is_dataclass(tp)


def coerce_args(args: Dict[str, Any], model_class: type) -> None:
    """Coerce JSON string values to native types based on field annotations.

    On the SSH/Ray path, values arrive via JSON from the Go control plane. Users
    may provide complex values as JSON strings (e.g. '{"temperature": 0.5}')
    rather than native objects. Unlike the K8s CLI path (where argparse type
    converters handle json.loads on specific fields), the Ray Dashboard API passes
    values through as-is.

    This function inspects each field's type annotation and converts JSON strings
    for dict/list fields and for custom dataclass/Pydantic-model fields (via
    TypeAdapter), leaving all other values untouched.

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
        raw = args[field_name]

        if _wants_dict_or_list(annotation):
            try:
                parsed = json.loads(raw)
            except (json.JSONDecodeError, TypeError):
                continue
            if isinstance(parsed, (dict, list)):
                args[field_name] = parsed
            continue

        target = _unwrap_optional(annotation)
        if _is_dataclass_like(target):
            try:
                args[field_name] = TypeAdapter(target).validate_json(raw)
            except PydanticValidationError as e:
                logger.warning(
                    "coerce_args: TypeAdapter failed for field %r (target=%s): %s",
                    field_name, target.__name__, e,
                )


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
    unknown = [k for k in list(args) if k not in known_fields]
    for key in unknown:
        args.pop(key, None)
    if unknown:
        logger.warning(
            "filter_engine_args: %d unknown engine parameter(s) ignored: %s",
            len(unknown),
            ", ".join(sorted(unknown)),
        )
