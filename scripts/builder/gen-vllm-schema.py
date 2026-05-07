#!/usr/bin/env python3
"""Generate JSON Schema for a vLLM engine version.

Reads ``AsyncEngineArgs`` (and ``FrontendArgs`` when present) from the
installed vLLM package and emits a JSON Schema describing the configuration
parameters Neutree exposes for the engine.

This script must run inside an environment where the target vLLM version is
already installed (typically the ``cluster-image-builder`` vLLM image), since
it relies on Python type annotations rather than CLI help text.

Type rules follow ``~/.claude/skills/generate-engine-version`` Step 1:

  int | None              -> "integer"          (NOT ["integer", "string"])
  float | None            -> "number"
  bool | None             -> "boolean"
  str | None              -> "string"
  Literal["a", "b", ...]  -> "string" + enum
  list[T] | None          -> "array"  + items
  dict[...] | None        -> "object"
  str | bool | None       -> ["string", "boolean"]   (genuine union, kept)
  str | list[str] | None  -> ["string", "array"]     (genuine union, kept)

Pure HTTP-server fields (``host``/``port``/``ssl_*``/``cors_*``/``uvicorn_*``)
are excluded since they have no meaning under Ray Serve.

Usage::

    # Print to stdout
    python scripts/builder/gen-vllm-schema.py

    # Write to canonical location
    python scripts/builder/gen-vllm-schema.py \\
        --output internal/engine/vllm/v0.17.1/schema.json

    # CI drift check (exit 1 if disk schema differs from generated)
    python scripts/builder/gen-vllm-schema.py \\
        --check internal/engine/vllm/v0.17.1/schema.json
"""
from __future__ import annotations

import argparse
import dataclasses
import json
import re
import sys
import types
import typing
from typing import Any, Iterable

# Field name patterns for HTTP-server-only knobs that have no effect under
# Ray Serve and should not appear in the schema. Ray Serve owns the ingress.
_HTTP_SERVER_PATTERNS = (
    re.compile(r"^host$"),
    re.compile(r"^port$"),
    re.compile(r"^uds$"),
    re.compile(r"^root_path$"),
    re.compile(r"^api_key$"),
    re.compile(r"^ssl_"),
    re.compile(r"^cors_"),
    re.compile(r"^uvicorn_"),
    re.compile(r"^allow_credentials$"),
    re.compile(r"^allowed_origins$"),
    re.compile(r"^allowed_methods$"),
    re.compile(r"^allowed_headers$"),
    re.compile(r"^enable_request_id_headers$"),
    re.compile(r"^disable_uvicorn_access_log$"),
    re.compile(r"^middleware$"),
    re.compile(r"^enable_ssl_refresh$"),
)


def _is_http_server_field(name: str) -> bool:
    return any(p.match(name) for p in _HTTP_SERVER_PATTERNS)


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
    # Reconstruct union without None
    return typing.Union[tuple(args)], True  # type: ignore[return-value]


def _python_to_json_type(tp: Any) -> dict[str, Any] | None:
    """Translate a single resolved Python type to a JSON Schema fragment.

    Returns None when the type cannot be confidently translated (caller will
    emit a ``"type": "object"`` fallback or skip the field).
    """
    inner, _ = _strip_optional(tp)

    # Literal[...] -> string + enum
    if typing.get_origin(inner) is typing.Literal:
        choices = list(typing.get_args(inner))
        if all(isinstance(c, str) for c in choices):
            return {"type": "string", "enum": choices}
        if all(isinstance(c, int) and not isinstance(c, bool) for c in choices):
            return {"type": "integer", "enum": choices}
        if all(isinstance(c, bool) for c in choices):
            return {"type": "boolean", "enum": choices}
        return {"enum": choices}

    # Genuine union (str | bool, str | list[str], etc.) — preserve multi-type
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

    # Concrete primitives
    if inner is bool:
        return {"type": "boolean"}
    if inner is int:
        return {"type": "integer"}
    if inner is float:
        return {"type": "number"}
    if inner is str:
        return {"type": "string"}

    # Container generics
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

    # Bare typing aliases without origin (rare)
    if inner is list:
        return {"type": "array", "items": {"type": "string"}}
    if inner is dict:
        return {"type": "object"}

    return None


def _resolve_default(field: dataclasses.Field) -> tuple[Any, bool]:
    """Return ``(default_value, has_default)`` for a dataclass field.

    Honors both ``default`` and ``default_factory``. Treats sentinel
    ``MISSING`` as no default. Skips ``None`` defaults entirely (keeps
    schemas compact and matches the existing hand-written convention).
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
    Returns an empty dict if the call fails — descriptions are then left
    empty and a human reviewer must fill them in.
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
        except Exception:
            continue
        for action in parser._actions:  # type: ignore[attr-defined]
            if not action.dest or action.dest == "help":
                continue
            if action.dest in helps:
                continue
            help_str = (action.help or "").strip()
            if help_str:
                # Strip any %(default)s / %(type)s placeholders to match the
                # pattern of existing hand-written descriptions.
                help_str = re.sub(r"%\([^)]+\)s", "", help_str).strip()
                helps[action.dest] = help_str
    return helps


def _iter_fields(args_class: type) -> Iterable[dataclasses.Field]:
    if not dataclasses.is_dataclass(args_class):
        return ()
    return dataclasses.fields(args_class)


def _resolve_hints(args_class: type) -> dict[str, Any]:
    try:
        return typing.get_type_hints(args_class, include_extras=False)
    except Exception:
        # Fallback: raw annotations (may contain unresolved forward refs).
        return dict(getattr(args_class, "__annotations__", {}))


def build_schema(
    *,
    include_frontend: bool,
    title: str,
    description: str,
) -> dict[str, Any]:
    """Build the JSON Schema dict by introspecting installed vLLM."""
    try:
        from vllm.engine.arg_utils import AsyncEngineArgs  # type: ignore
    except Exception as exc:  # pragma: no cover
        raise SystemExit(
            f"ERROR: cannot import vllm.engine.arg_utils.AsyncEngineArgs: {exc}\n"
            "Run this script inside an environment where vLLM is installed."
        ) from exc

    args_classes: list[type] = [AsyncEngineArgs]
    if include_frontend:
        try:
            from vllm.entrypoints.openai.cli_args import FrontendArgs  # type: ignore

            args_classes.append(FrontendArgs)
        except Exception:
            # Older vLLM (< v0.10) had no FrontendArgs. Silently skip.
            pass

    hints_per_class = {cls: _resolve_hints(cls) for cls in args_classes}
    helps = _help_texts_via_argparse(args_classes)

    properties: dict[str, dict[str, Any]] = {}
    todo_fields: list[str] = []

    for cls in args_classes:
        hints = hints_per_class[cls]
        for field in _iter_fields(cls):
            name = field.name
            if name in properties:
                continue  # AsyncEngineArgs takes precedence over FrontendArgs
            if _is_http_server_field(name):
                continue
            tp = hints.get(name, field.type)
            schema_type = _python_to_json_type(tp)
            if schema_type is None:
                # Unsupported / opaque type — emit object fallback, flag for
                # human review.
                schema_type = {"type": "object"}
                todo_fields.append(name)
            entry: dict[str, Any] = dict(schema_type)
            default_value, has_default = _resolve_default(field)
            if has_default and not (
                isinstance(default_value, (dict, list, set, tuple))
                and not default_value
            ):
                # Skip empty containers — they appear as the dataclass's
                # neutral default (e.g. ``[]``, ``{}``) and would be noise in
                # the user-facing schema.
                if isinstance(default_value, frozenset):
                    default_value = sorted(default_value)
                if isinstance(default_value, set):
                    default_value = sorted(default_value)
                entry["default"] = default_value
            help_text = helps.get(name)
            if help_text:
                entry["description"] = help_text
            properties[name] = entry

    schema: dict[str, Any] = {
        "$schema": "http://json-schema.org/draft-07/schema#",
        "type": "object",
        "title": title,
        "description": description,
        "properties": properties,
        "additionalProperties": False,
    }
    if todo_fields:
        # Surface the list to stderr so human reviewers know which fields
        # need a manual type pass before the schema is committed.
        sys.stderr.write(
            "WARN: opaque types fell back to object for fields: "
            + ", ".join(sorted(todo_fields))
            + "\n"
        )
    return schema


def _detect_vllm_version() -> str:
    try:
        import vllm  # type: ignore

        v = getattr(vllm, "__version__", "unknown")
    except Exception:
        return "unknown"
    return v if v.startswith("v") else f"v{v}"


def _format_value(value: Any) -> str:
    """Encode a primitive (or array/object) for our custom emitter."""
    return json.dumps(value, ensure_ascii=False)


def _is_primitive(value: Any) -> bool:
    return isinstance(value, (str, int, float, bool)) or value is None


def _is_simple_object(value: Any) -> bool:
    """``items`` payloads with a single ``{"type": "..."}`` key get inlined."""
    return (
        isinstance(value, dict)
        and len(value) == 1
        and "type" in value
        and isinstance(value["type"], str)
    )


def _emit(value: Any, indent: int) -> str:
    """Custom JSON emitter that inlines primitive arrays and trivial items.

    Matches the formatting convention of the hand-written schemas under
    ``internal/engine/<engine>/<version>/schema.json``: top-level objects
    use 2-space indent, but ``enum`` / ``default`` / simple ``items``
    arrays/objects stay on a single line for readability.
    """
    pad = " " * indent
    if isinstance(value, dict):
        if not value:
            return "{}"
        # Inline single-key {"type": "..."} for items refs.
        if _is_simple_object(value):
            return _format_value(value)
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
        if all(_is_simple_object(v) for v in value):
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
        "--no-frontend-args",
        action="store_true",
        help="Skip FrontendArgs introspection (useful for older vLLM).",
    )
    parser.add_argument(
        "--title",
        help="Schema title. Default: derived from detected vLLM version.",
    )
    parser.add_argument(
        "--description",
        help=(
            "Schema description. Default: derived from detected vLLM version."
        ),
    )
    args = parser.parse_args(argv)

    detected = _detect_vllm_version()
    title = args.title or f"vLLM {detected} Engine Configuration"
    description = (
        args.description
        or f"Configuration schema for vLLM {detected} engine parameters"
    )

    schema = build_schema(
        include_frontend=not args.no_frontend_args,
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
