#!/usr/bin/env python3
"""Generate JSON Schema for an SGLang engine version from upstream dataclasses.

Walks ``sglang.srt.server_args.ServerArgs`` and emits the JSON Schema that
lands at ``internal/engine/sglang/<version>/schema.json``.

This script must run inside an environment where the target SGLang version
is already installed (typically the ``cluster-image-builder`` SGLang image),
since it relies on Python type annotations rather than CLI help text.

The script intentionally carries **zero engine-specific filtering** — no
curated field list, no free-form/preserved-enum tables, no variant
extras. Every dataclass field is emitted as-is, with types translated by
the rules below. Pruning, if any, is the caller's responsibility (e.g. a
manual pass after generation).

**Why dataclass field names matter (vs. CLI long names):**

SGLang's ``ServerArgs`` uses fields like ``tp_size`` / ``dp_size`` while
the CLI exposes ``--tensor-parallel-size`` etc. Neutree's SSH/Ray path
calls ``Engine(**kwargs)`` directly, so kwargs must match the dataclass
field name; otherwise ``filter_engine_args`` silently drops them and the
parallelism setting is lost. Walking the dataclass directly guarantees we
never use the CLI long names.

**Type rules** (Python annotation → JSON Schema):

  int | None              -> "integer"          (NOT ["integer", "string"])
  float | None            -> "number"
  bool | None             -> "boolean"
  str | None              -> "string"
  Literal["a", "b", ...]  -> "string" + enum
  list[T] | None          -> "array"  + items
  dict[...] | None        -> "object"

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

    python scripts/builder/gen-sglang-schema.py \\
        --output internal/engine/sglang/v0.5.10/schema.json

    python scripts/builder/gen-sglang-schema.py \\
        --check internal/engine/sglang/v0.5.10/schema.json

    # Override class (e.g. for a future PortArgs-style split)
    python scripts/builder/gen-sglang-schema.py \\
        --class sglang.srt.server_args:ServerArgs
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

DEFAULT_PRIMARY_CLASS = "sglang.srt.server_args:ServerArgs"


def _strip_optional(tp: Any) -> tuple[Any, bool]:
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

    Honors both ``default`` and ``default_factory``. ``None`` and
    non-JSON-serializable defaults are treated as "no default" so the
    field still appears in the schema with its translated type instead
    of crashing the json.dumps step downstream.
    """
    candidate: Any
    if field.default is not dataclasses.MISSING:
        candidate = field.default
    elif field.default_factory is not dataclasses.MISSING:  # type: ignore[misc]
        try:
            candidate = field.default_factory()  # type: ignore[misc]
        except Exception:
            return None, False
    else:
        return None, False
    if candidate is None:
        return None, False
    try:
        json.dumps(candidate)
    except (TypeError, ValueError):
        return None, False
    return candidate, True


def _help_texts_via_argparse(args_classes: Iterable[type]) -> dict[str, str]:
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
            "Run this script inside an environment where SGLang is installed."
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
    exclude_patterns: list[re.Pattern[str]] | None = None,
) -> dict[str, Any]:
    """``exclude_patterns`` are compiled regex objects; any field whose name
    matches any pattern (via ``re.fullmatch``) is dropped from the output
    schema. Generic escape hatch for caller-driven pruning without
    hard-coding the list in the script.
    """
    args_classes: list[type] = [primary_class] + list(merge_classes)
    hints_per_class = {cls: _resolve_hints(cls) for cls in args_classes}
    helps = _help_texts_via_argparse(args_classes)

    properties: dict[str, dict[str, Any]] = {}
    excluded_fields: list[tuple[str, str]] = []
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
            if exclude_patterns:
                matched = next(
                    (p for p in exclude_patterns if p.fullmatch(name)),
                    None,
                )
                if matched is not None:
                    excluded_fields.append((name, matched.pattern))
                    continue
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
    if excluded_fields:
        by_pattern: dict[str, list[str]] = {}
        for name, pattern in excluded_fields:
            by_pattern.setdefault(pattern, []).append(name)
        for pattern, names in sorted(by_pattern.items()):
            sys.stderr.write(
                f"INFO: --exclude-regex {pattern!r} dropped "
                f"{len(names)} field(s): {', '.join(sorted(names))}\n"
            )
    return schema


def _detect_sglang_version() -> str:
    try:
        import sglang  # type: ignore

        v = getattr(sglang, "__version__", None)
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

    Object dicts always render across multiple lines (including
    single-key ``items`` payloads); primitive arrays (``enum`` /
    ``default``) stay on a single line. Mirrors the format of
    hand-written schemas under
    ``internal/engine/<engine>/<version>/schema.json``.
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
        prog="gen-sglang-schema",
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
        "--exclude-regex",
        dest="exclude_patterns",
        action="append",
        metavar="REGEX",
        help=(
            "Exclude fields whose name fully matches this regex. Repeat "
            "for multiple patterns. Useful for pruning HTTP-server / "
            "gateway-only fields when running under Ray Serve."
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
            "Useful as a CI guard against drift."
        ),
    )
    parser.add_argument(
        "--title",
        help="Schema title. Default: derived from detected SGLang version.",
    )
    parser.add_argument(
        "--description",
        help=(
            "Schema description. Default: derived from detected SGLang "
            "version."
        ),
    )
    args = parser.parse_args(argv)

    primary_class = _import_class(args.primary_class)

    merge_classes: list[type] = []
    for spec in args.merge_classes or []:
        merge_classes.append(_import_class(spec))

    detected = _detect_sglang_version()
    title = args.title or f"SGLang {detected} Engine Configuration"
    description = (
        args.description
        or f"Configuration schema for SGLang {detected} engine parameters"
    )

    exclude_patterns: list[re.Pattern[str]] = []
    for raw in args.exclude_patterns or []:
        try:
            exclude_patterns.append(re.compile(raw))
        except re.error as exc:
            sys.stderr.write(f"ERROR: invalid --exclude-regex {raw!r}: {exc}\n")
            return 2

    schema = build_schema(
        primary_class=primary_class,
        merge_classes=merge_classes,
        title=title,
        description=description,
        exclude_patterns=exclude_patterns or None,
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
