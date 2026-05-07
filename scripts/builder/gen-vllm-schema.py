#!/usr/bin/env python3
"""Generate JSON Schema for a vLLM engine version from upstream dataclasses.

Walks ``vllm.engine.arg_utils.AsyncEngineArgs`` plus
``vllm.entrypoints.openai.cli_args.FrontendArgs`` (when present) and emits
the JSON Schema that lands at
``internal/engine/vllm/<version>/schema.json``.

This script must run inside an environment where the target vLLM version is
already installed (typically the ``cluster-image-builder`` vLLM image),
since it relies on Python type annotations rather than CLI help text.

The script intentionally carries **zero engine-specific filtering** — no
curated field list, no HTTP-server-only blocklist, no enum stripping. Every
dataclass field is emitted as-is, with types translated by the rules below.
Pruning, if any, is the caller's responsibility (e.g. a manual pass after
generation).

**Type rules** (Python annotation → JSON Schema):

  int | None              -> "integer"          (NOT ["integer", "string"])
  float | None            -> "number"
  bool | None             -> "boolean"
  str | None              -> "string"
  Literal["a", "b", ...]  -> "string" + enum
  list[T] | None          -> "array"  + items
  dict[...] | None        -> "object"
  str | bool | None       -> ["string", "boolean"]   (genuine union, kept)
  str | list[str] | None  -> ["string", "array"]     (genuine union, kept)

**Limitations**:

- ``required[]`` is intentionally not emitted; downstream validators
  must enforce required-field semantics elsewhere. A field annotated
  ``int | None`` with default ``None`` is rendered without a
  ``"default"`` key (matches the existing schema convention).
- ``Annotated[X, ...]`` is unwrapped to ``X`` only when the primary
  ``typing.get_type_hints(include_extras=False)`` path succeeds; on
  fallback to raw ``__annotations__``, ``Annotated`` is unwrapped one
  level via ``__metadata__`` introspection.
- Heterogeneous tuples (``tuple[int, str]``) are treated as
  homogeneous (``items`` derived from the first element).

Usage::

    # Default: AsyncEngineArgs + FrontendArgs, write to internal/engine path
    python scripts/builder/gen-vllm-schema.py \\
        --output internal/engine/vllm/v0.17.1/schema.json

    # Print to stdout
    python scripts/builder/gen-vllm-schema.py

    # CI drift check
    python scripts/builder/gen-vllm-schema.py \\
        --check internal/engine/vllm/v0.17.1/schema.json

    # Override classes (e.g. EngineArgs only, no FrontendArgs)
    python scripts/builder/gen-vllm-schema.py \\
        --class vllm.engine.arg_utils:EngineArgs --no-merge-defaults
"""
from __future__ import annotations

import argparse
import dataclasses
import importlib
import json
import re
import sys
import types
import typing
from typing import Any, Iterable

DEFAULT_PRIMARY_CLASS = "vllm.engine.arg_utils:AsyncEngineArgs"
DEFAULT_MERGE_CLASSES = ("vllm.entrypoints.openai.cli_args:FrontendArgs",)


def _strip_optional(tp: Any) -> tuple[Any, bool]:
    """Strip a single ``Optional`` / ``... | None`` wrapper.

    Returns ``(inner_type, was_optional)``. Only removes ``None`` from the
    union; if the union still has multiple members afterward, returns the
    reduced union.
    """
    origin = typing.get_origin(tp)
    if origin not in (typing.Union, getattr(types, "UnionType", typing.Union)):
        return tp, False
    args = [a for a in typing.get_args(tp) if a is not type(None)]
    if not args:
        return tp, True
    if len(args) == 1:
        return args[0], True
    return typing.Union[tuple(args)], True  # type: ignore[return-value]


def _python_to_json_type(tp: Any) -> dict[str, Any] | None:
    """Translate a single resolved Python type to a JSON Schema fragment.

    Returns ``None`` when the type cannot be confidently translated (caller
    falls back to ``{"type": "object"}`` and surfaces a warning).
    """
    # Strip Annotated[X, ...] -> X. The primary path uses get_type_hints
    # with include_extras=False which already strips Annotated, but the
    # fallback raw-__annotations__ path preserves it; harden here.
    if hasattr(tp, "__metadata__"):
        meta_args = typing.get_args(tp)
        if meta_args:
            tp = meta_args[0]
    inner, _ = _strip_optional(tp)

    if typing.get_origin(inner) is typing.Literal:
        choices = list(typing.get_args(inner))
        if all(isinstance(c, str) for c in choices):
            return {"type": "string", "enum": choices}
        if all(isinstance(c, int) and not isinstance(c, bool) for c in choices):
            return {"type": "integer", "enum": choices}
        if all(isinstance(c, bool) for c in choices):
            return {"type": "boolean", "enum": choices}
        return {"enum": choices}

    origin = typing.get_origin(inner)
    if origin in (typing.Union, getattr(types, "UnionType", typing.Union)):
        union_args = list(typing.get_args(inner))
        json_types: list[str] = []
        items_schema: dict[str, Any] | None = None
        for arg in union_args:
            sub = _python_to_json_type(arg)
            if sub is None or "type" not in sub:
                continue
            t = sub["type"]
            if isinstance(t, list):
                for inner_t in t:
                    if inner_t not in json_types:
                        json_types.append(inner_t)
            elif t not in json_types:
                json_types.append(t)
            if t == "array" and "items" in sub and items_schema is None:
                items_schema = sub["items"]
        if not json_types:
            return None
        if len(json_types) == 1:
            out: dict[str, Any] = {"type": json_types[0]}
            if items_schema is not None and json_types[0] == "array":
                out["items"] = items_schema
            return out
        out = {"type": json_types}
        if items_schema is not None and "array" in json_types:
            out["items"] = items_schema
        return out

    if inner is bool:
        return {"type": "boolean"}
    if inner is int:
        return {"type": "integer"}
    if inner is float:
        return {"type": "number"}
    if inner is str:
        return {"type": "string"}

    if origin in (list, tuple, set, frozenset, typing.List, typing.Tuple, typing.Set):
        item_args = typing.get_args(inner)
        items_schema: dict[str, Any] = {"type": "string"}
        if item_args:
            sub = _python_to_json_type(item_args[0])
            if sub and "type" in sub:
                items_schema = sub
        return {"type": "array", "items": items_schema}
    if origin in (dict, typing.Dict):
        return {"type": "object"}

    if inner is list:
        return {"type": "array", "items": {"type": "string"}}
    if inner is dict:
        return {"type": "object"}

    return None


def _resolve_default(field: dataclasses.Field) -> tuple[Any, bool]:
    """Return ``(default_value, has_default)``.

    Honors both ``default`` and ``default_factory``. ``None`` defaults are
    treated as "no default" so the schema stays compact and matches the
    convention of the existing hand-written schemas.
    """
    if field.default is not dataclasses.MISSING:
        return field.default, field.default is not None
    if field.default_factory is not dataclasses.MISSING:  # type: ignore[misc]
        try:
            v = field.default_factory()  # type: ignore[misc]
        except Exception:
            return None, False
        return v, v is not None
    return None, False


def _help_texts_via_argparse(args_classes: Iterable[type]) -> dict[str, str]:
    """Best-effort extraction of CLI help text per dataclass field.

    Calls ``add_cli_args(parser)`` on each class against a stub argparse
    parser and indexes the resulting ``help=`` strings by argument ``dest``.
    """
    import argparse as _ap

    helps: dict[str, str] = {}
    for cls in args_classes:
        adder = getattr(cls, "add_cli_args", None)
        if not callable(adder):
            continue
        parser = _ap.ArgumentParser(add_help=False)
        try:
            adder(parser)
        except Exception as exc:
            sys.stderr.write(
                f"INFO: failed to extract help text from "
                f"{cls.__module__}.{cls.__name__}.add_cli_args(): {exc}; "
                "schema descriptions for that class will be empty\n"
            )
            continue
        for action in parser._actions:  # type: ignore[attr-defined]
            if not action.dest or action.dest == "help":
                continue
            if action.dest in helps:
                continue
            help_str = (action.help or "").strip()
            if help_str:
                help_str = re.sub(r"%\([^)]+\)s", "", help_str).strip()
                helps[action.dest] = help_str
    return helps


def _resolve_hints(args_class: type) -> dict[str, Any]:
    try:
        return typing.get_type_hints(args_class, include_extras=False)
    except Exception:
        return dict(getattr(args_class, "__annotations__", {}))


def _import_class(spec: str) -> type:
    """Import a class given a ``module.path:ClassName`` spec."""
    if ":" not in spec:
        raise SystemExit(
            f"ERROR: --class / --merge expects 'module.path:ClassName', got {spec!r}"
        )
    module_path, class_name = spec.rsplit(":", 1)
    try:
        module = importlib.import_module(module_path)
    except ImportError as exc:
        raise SystemExit(
            f"ERROR: cannot import {module_path}: {exc}\n"
            "Run this script inside an environment where vLLM is installed."
        ) from exc
    try:
        cls = getattr(module, class_name)
    except AttributeError as exc:
        raise SystemExit(
            f"ERROR: {module_path} has no attribute {class_name}: {exc}"
        ) from exc
    if not dataclasses.is_dataclass(cls):
        raise SystemExit(
            f"ERROR: {spec} is not a dataclass; cannot introspect fields."
        )
    return cls


def build_schema(
    *,
    primary_class: type,
    merge_classes: list[type],
    title: str,
    description: str,
) -> dict[str, Any]:
    """Build the JSON Schema by walking primary_class then merge_classes.

    Field names that appear in ``primary_class`` win over later merges; any
    ``merge_classes`` field whose name is already present is dropped to
    preserve the primary class's type/default.
    """
    args_classes: list[type] = [primary_class] + list(merge_classes)
    hints_per_class = {cls: _resolve_hints(cls) for cls in args_classes}
    helps = _help_texts_via_argparse(args_classes)

    properties: dict[str, dict[str, Any]] = {}
    todo_fields: list[str] = []
    field_origin: dict[str, type] = {}
    duplicates_dropped: list[tuple[str, type, type]] = []

    for cls in args_classes:
        hints = hints_per_class[cls]
        if not dataclasses.is_dataclass(cls):
            continue
        for field in dataclasses.fields(cls):
            name = field.name
            if name in properties:
                duplicates_dropped.append((name, field_origin[name], cls))
                continue  # primary class precedence
            tp = hints.get(name, field.type)
            schema_type = _python_to_json_type(tp)
            if schema_type is None:
                schema_type = {"type": "object"}
                todo_fields.append(name)
            entry: dict[str, Any] = dict(schema_type)
            default_value, has_default = _resolve_default(field)
            if has_default:
                # Preserve empty-collection defaults (`{}` / `[]`): the
                # existing hand-written schemas emit them for fields like
                # `hf_overrides`, `limit_mm_per_prompt`, and dropping
                # them would silently change the configured default for
                # downstream JSON-Schema validators.
                if isinstance(default_value, (set, frozenset)):
                    default_value = sorted(default_value)
                entry["default"] = default_value
            help_text = helps.get(name)
            if help_text:
                entry["description"] = help_text
            properties[name] = entry
            field_origin[name] = cls

    schema: dict[str, Any] = {
        "$schema": "http://json-schema.org/draft-07/schema#",
        "type": "object",
        "title": title,
        "description": description,
        "properties": properties,
        "additionalProperties": False,
    }
    if todo_fields:
        sys.stderr.write(
            "WARN: opaque types fell back to object for fields: "
            + ", ".join(sorted(todo_fields))
            + "\n"
        )
    if duplicates_dropped:
        sys.stderr.write(
            "INFO: dropped duplicate fields (kept first definition): "
            + ", ".join(
                f"{name} (kept from {kept.__name__}, "
                f"dropped from {dropped.__name__})"
                for name, kept, dropped in duplicates_dropped
            )
            + "\n"
        )
    return schema


def _detect_vllm_version() -> str:
    try:
        import vllm  # type: ignore

        v = getattr(vllm, "__version__", None)
    except Exception:
        return "(unknown)"
    if not v:
        return "(unknown)"
    return v if v.startswith("v") else f"v{v}"


def _format_value(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False)


def _is_primitive(value: Any) -> bool:
    return isinstance(value, (str, int, float, bool)) or value is None


def _emit(value: Any, indent: int) -> str:
    """Custom JSON emitter matching the existing schema convention.

    Top-level objects use 2-space indent. Primitive arrays (``enum`` /
    ``default``) stay on a single line for readability. Object dicts —
    including single-key ``items`` payloads like ``{"type": "string"}``
    — are always rendered across multiple lines, matching the formatting
    of the existing hand-written schemas under
    ``internal/engine/<engine>/<version>/schema.json`` so ``--check``
    does not produce noise from cosmetic diffs.
    """
    pad = " " * indent
    if isinstance(value, dict):
        if not value:
            return "{}"
        lines = ["{"]
        keys = list(value.keys())
        for i, key in enumerate(keys):
            v = value[key]
            sep = "," if i < len(keys) - 1 else ""
            lines.append(f"{pad}  {json.dumps(key)}: {_emit(v, indent + 2)}{sep}")
        lines.append(f"{pad}}}")
        return "\n".join(lines)
    if isinstance(value, list):
        if not value:
            return "[]"
        if all(_is_primitive(v) for v in value):
            return _format_value(value)
        lines = ["["]
        for i, v in enumerate(value):
            sep = "," if i < len(value) - 1 else ""
            lines.append(f"{pad}  {_emit(v, indent + 2)}{sep}")
        lines.append(f"{pad}]")
        return "\n".join(lines)
    return _format_value(value)


def render_schema(schema: dict[str, Any]) -> str:
    return _emit(schema, 0) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="gen-vllm-schema",
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--class",
        dest="primary_class",
        default=DEFAULT_PRIMARY_CLASS,
        help=(
            "Primary dataclass to introspect, as 'module.path:ClassName'. "
            f"Default: {DEFAULT_PRIMARY_CLASS}"
        ),
    )
    parser.add_argument(
        "--merge",
        dest="merge_classes",
        action="append",
        metavar="MODULE:CLASS",
        help=(
            "Additional dataclass to merge after the primary (later "
            "duplicates ignored). Repeat for multiple."
        ),
    )
    parser.add_argument(
        "--no-merge-defaults",
        action="store_true",
        help=(
            "Skip the default merge classes "
            f"({', '.join(DEFAULT_MERGE_CLASSES)})."
        ),
    )
    parser.add_argument(
        "--output",
        "-o",
        help="Output path. Default: stdout.",
    )
    parser.add_argument(
        "--check",
        metavar="PATH",
        help=(
            "Compare generated schema against PATH and exit 1 on diff. "
            "Useful as a CI guard against drift between upstream vLLM and "
            "the committed schema."
        ),
    )
    parser.add_argument(
        "--title",
        help="Schema title. Default: derived from detected vLLM version.",
    )
    parser.add_argument(
        "--description",
        help=(
            "Schema description. Default: derived from detected vLLM "
            "version."
        ),
    )
    args = parser.parse_args(argv)

    primary_class = _import_class(args.primary_class)

    merge_specs: list[str] = []
    if not args.no_merge_defaults:
        merge_specs.extend(DEFAULT_MERGE_CLASSES)
    if args.merge_classes:
        merge_specs.extend(args.merge_classes)

    merge_classes: list[type] = []
    for spec in merge_specs:
        try:
            merge_classes.append(_import_class(spec))
        except SystemExit:
            # Default merge classes (e.g. FrontendArgs on older vLLM) may
            # legitimately be absent. Skip silently for defaults; surface
            # the error for explicit --merge specs.
            if spec in DEFAULT_MERGE_CLASSES:
                sys.stderr.write(
                    f"INFO: optional merge class {spec} not present in this "
                    "vLLM build, skipping.\n"
                )
                continue
            raise

    detected = _detect_vllm_version()
    title = args.title or f"vLLM {detected} Engine Configuration"
    description = (
        args.description
        or f"Configuration schema for vLLM {detected} engine parameters"
    )

    schema = build_schema(
        primary_class=primary_class,
        merge_classes=merge_classes,
        title=title,
        description=description,
    )
    rendered = render_schema(schema)

    if args.check:
        try:
            with open(args.check, "r", encoding="utf-8") as f:
                disk = f.read()
        except OSError as exc:
            sys.stderr.write(f"ERROR: cannot read {args.check}: {exc}\n")
            return 2
        if disk == rendered:
            sys.stderr.write("OK: schema matches disk\n")
            return 0
        sys.stderr.write(
            f"DIFF: generated schema differs from {args.check}. "
            "Re-run without --check to refresh.\n"
        )
        return 1

    if args.output:
        with open(args.output, "w", encoding="utf-8") as f:
            f.write(rendered)
        sys.stderr.write(f"wrote {args.output}\n")
    else:
        sys.stdout.write(rendered)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
