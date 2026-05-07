#!/usr/bin/env python3
"""Generate JSON Schema for an SGLang engine version.

Reads ``ServerArgs`` from the installed SGLang package and emits a JSON
Schema describing the configuration parameters Neutree exposes.

Like ``gen-vllm-schema.py``, this script must run inside an environment
where the target SGLang version is already installed (typically the
``cluster-image-builder`` SGLang image).

**Why dataclass field names matter (vs. CLI long names):**

SGLang's ``ServerArgs`` uses fields like ``tp_size`` / ``dp_size`` while the
CLI exposes ``--tensor-parallel-size`` etc. Neutree's SSH/Ray path calls
``Engine(**kwargs)`` directly, so kwargs must match the dataclass field
name; otherwise ``filter_engine_args`` silently drops them and the
parallelism setting is lost. This script always uses dataclass names.

Type rules follow ``~/.claude/skills/generate-engine-version-sglang``:

  int | None              -> "integer"          (NOT ["integer", "string"])
  float | None            -> "number"
  bool | None             -> "boolean"
  str | None              -> "string"
  Literal["a", "b", ...]  -> "string" + enum    (only for preserved-enum
                                                  fields; free-form backends
                                                  drop the enum)
  list[T] | None          -> "array"  + items
  dict[...] | None        -> "object"

**Free-form fields** (``attention_backend``, ``sampling_backend``,
``grammar_backend``, ``quantization``, ``load_format``, ``moe_a2a_backend``,
``moe_runner_backend``, ``tool_call_parser``, ``reasoning_parser``) drop
their ``enum`` even if upstream declares Literal/choices, because SGLang
backends evolve fast and locking the enum forces a schema bump per release.

**Curated mode (default):** emits ~55 fields covering identity / loading /
memory / parallelism / scheduling / backends / tool / speculative / LoRA /
logging / performance plus optional variant-specific fields. Pass
``--all`` to emit every dataclass field.

Usage::

    python scripts/builder/gen-sglang-schema.py \\
        --output internal/engine/sglang/v0.5.10/schema.json

    python scripts/builder/gen-sglang-schema.py \\
        --variant deepseek-v4-hopper \\
        --output internal/engine/sglang/deepseek-v4-hopper/schema.json
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

# Backends that evolve too fast for a stable enum. Even when ServerArgs
# declares Literal[...] or argparse choices, drop them: the schema would
# otherwise reject valid new values added in subsequent SGLang releases.
_FREEFORM_FIELDS = frozenset(
    {
        "attention_backend",
        "sampling_backend",
        "grammar_backend",
        "quantization",
        "load_format",
        "moe_a2a_backend",
        "moe_runner_backend",
        "tool_call_parser",
        "reasoning_parser",
    }
)

# Fields whose enum we deliberately preserve when upstream declares one.
# These are user-facing modes with stable, well-known value sets.
_PRESERVED_ENUM_FIELDS = frozenset(
    {
        "dtype",
        "kv_cache_dtype",
        "tokenizer_mode",
        "schedule_policy",
        "speculative_algorithm",
        "disaggregation_mode",
    }
)

# Curated field set per generate-engine-version-sglang Step 1. Covers
# Identity / Loading / Memory / Parallelism / Scheduling / Backends /
# Tool / Speculative / LoRA / Logging / Performance.
_CURATED_BASE = [
    # Identity
    "trust_remote_code",
    "tokenizer_path",
    "tokenizer_mode",
    "revision",
    "context_length",
    "chat_template",
    "skip_tokenizer_init",
    "is_embedding",
    # Loading
    "load_format",
    "dtype",
    "kv_cache_dtype",
    "quantization",
    # Memory / Batching
    "mem_fraction_static",
    "max_running_requests",
    "max_total_tokens",
    "chunked_prefill_size",
    "max_prefill_tokens",
    "page_size",
    # Parallel — schema MUST use dataclass names, not CLI long names.
    "tp_size",
    "dp_size",
    "pp_size",
    "ep_size",
    "enable_dp_attention",
    # Scheduling
    "schedule_policy",
    "schedule_conservativeness",
    "decode_log_interval",
    "watchdog_timeout",
    "random_seed",
    "stream_interval",
    # Backends (free-form per _FREEFORM_FIELDS)
    "attention_backend",
    "sampling_backend",
    "grammar_backend",
    # Tool / Reasoning
    "tool_call_parser",
    "reasoning_parser",
    # Speculative
    "speculative_algorithm",
    "speculative_num_steps",
    "speculative_eagle_topk",
    "speculative_num_draft_tokens",
    "speculative_token_map",
    "speculative_draft_model_path",
    # LoRA
    "lora_paths",
    "max_loras_per_batch",
    # Logging
    "log_level",
    "log_level_http",
    "log_requests",
    "show_time_cost",
    "enable_metrics",
    "enable_cache_report",
    # Performance toggles
    "disable_radix_cache",
    "disable_cuda_graph",
    "disable_cuda_graph_padding",
    "enable_torch_compile",
    "enable_p2p_check",
    "enable_mixed_chunk",
    "enable_two_batch_overlap",
]

# Variant-specific extras. Names match ServerArgs field names — verify
# against the variant's actual ServerArgs definition before adding new
# variants here.
_VARIANT_EXTRAS: dict[str, list[str]] = {
    "deepseek-v4-hopper": [
        # Disaggregation
        "disaggregation_mode",
        "disaggregation_decode_dp",
        "disaggregation_decode_tp",
        "disaggregation_prefill_pp",
        "disaggregation_decode_enable_fake_auto",
        # MoE
        "moe_a2a_backend",
        "moe_runner_backend",
        # Piecewise CUDA graph
        "enable_piecewise_cuda_graph",
        # NGRAM speculative
        "speculative_ngram_branch_length",
        "speculative_ngram_max_match_window_size",
        "speculative_ngram_min_match_window_size",
        # Multimodal
        "mm_max_concurrent_calls",
        "mm_per_request_timeout",
        # Indexer
        "enable_return_indexer_topk",
        "hierarchical_sparse_attention_extra_config",
        # Misc
        "stream_output",
        "disable_hicache_numa_detect",
    ],
}


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
    if field.default is not dataclasses.MISSING:
        return field.default, field.default is not None
    if field.default_factory is not dataclasses.MISSING:  # type: ignore[misc]
        try:
            v = field.default_factory()  # type: ignore[misc]
        except Exception:
            return None, False
        return v, v is not None
    return None, False


def _help_texts_via_argparse(args_class: type) -> dict[str, str]:
    import argparse as _ap

    helps: dict[str, str] = {}
    adder = getattr(args_class, "add_cli_args", None)
    if not callable(adder):
        return helps
    parser = _ap.ArgumentParser(add_help=False)
    try:
        adder(parser)
    except Exception:
        return helps
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


def build_schema(
    *,
    field_filter: list[str] | None,
    title: str,
    description: str,
) -> dict[str, Any]:
    try:
        from sglang.srt.server_args import ServerArgs  # type: ignore
    except Exception as exc:  # pragma: no cover
        raise SystemExit(
            "ERROR: cannot import sglang.srt.server_args.ServerArgs: "
            f"{exc}\nRun this script inside an environment where SGLang is "
            "installed."
        ) from exc

    if not dataclasses.is_dataclass(ServerArgs):
        raise SystemExit(
            "ERROR: sglang.srt.server_args.ServerArgs is not a dataclass; "
            "this generator assumes the dataclass shape used since v0.4."
        )

    hints = _resolve_hints(ServerArgs)
    helps = _help_texts_via_argparse(ServerArgs)
    available = {f.name: f for f in dataclasses.fields(ServerArgs)}

    if field_filter is not None:
        # Curated mode. Walk the curated list in declared order; warn about
        # any name that no longer exists in the upstream dataclass so the
        # human reviewer notices upstream renames before committing.
        ordered_names: list[str] = []
        missing: list[str] = []
        for name in field_filter:
            if name in available:
                ordered_names.append(name)
            else:
                missing.append(name)
        if missing:
            sys.stderr.write(
                "WARN: curated fields not found in upstream ServerArgs "
                "(removed or renamed?): " + ", ".join(missing) + "\n"
            )
    else:
        # Full mode: every dataclass field, in declaration order.
        ordered_names = list(available.keys())

    properties: dict[str, dict[str, Any]] = {}
    todo_fields: list[str] = []

    for name in ordered_names:
        field = available[name]
        tp = hints.get(name, field.type)
        schema_type = _python_to_json_type(tp)
        if schema_type is None:
            schema_type = {"type": "object"}
            todo_fields.append(name)
        entry: dict[str, Any] = dict(schema_type)

        # Free-form fields drop the enum even if Literal forced one in.
        if name in _FREEFORM_FIELDS and "enum" in entry:
            entry.pop("enum", None)
        # Preserved-enum fields keep enum if present (default behavior); no
        # action needed beyond not stripping. Listed for documentation.
        _ = _PRESERVED_ENUM_FIELDS

        default_value, has_default = _resolve_default(field)
        if has_default and not (
            isinstance(default_value, (dict, list, set, tuple))
            and not default_value
        ):
            if isinstance(default_value, (set, frozenset)):
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
        sys.stderr.write(
            "WARN: opaque types fell back to object for fields: "
            + ", ".join(sorted(todo_fields))
            + "\n"
        )
    return schema


def _detect_sglang_version() -> str:
    try:
        import sglang  # type: ignore

        v = getattr(sglang, "__version__", "unknown")
    except Exception:
        return "unknown"
    return v if v.startswith("v") else f"v{v}"


def _format_value(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False)


def _is_primitive(value: Any) -> bool:
    return isinstance(value, (str, int, float, bool)) or value is None


def _is_simple_object(value: Any) -> bool:
    return (
        isinstance(value, dict)
        and len(value) == 1
        and "type" in value
        and isinstance(value["type"], str)
    )


def _emit(value: Any, indent: int) -> str:
    pad = " " * indent
    if isinstance(value, dict):
        if not value:
            return "{}"
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
        prog="gen-sglang-schema",
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
            "Useful as a CI guard against drift."
        ),
    )
    parser.add_argument(
        "--all",
        action="store_true",
        help=(
            "Emit every ServerArgs field (default: curated subset of ~55)."
        ),
    )
    parser.add_argument(
        "--variant",
        help=(
            "Variant name (e.g. ``deepseek-v4-hopper``) to add variant-"
            "specific fields on top of the curated base. Ignored with --all."
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

    if args.all and args.variant:
        sys.stderr.write(
            "WARN: --variant has no effect with --all (full set already "
            "includes variant-specific fields).\n"
        )

    detected = _detect_sglang_version()
    title = args.title or f"SGLang {detected} Engine Configuration"
    description = (
        args.description
        or f"Configuration schema for SGLang {detected} ServerArgs"
        " (subset). Field names match SGLang's Python ServerArgs dataclass —"
        " Neutree calls Engine(**kwargs) directly. The K8s deploy template"
        " converts snake_case keys to kebab-case CLI flags automatically."
    )

    if args.all:
        field_filter: list[str] | None = None
    else:
        field_filter = list(_CURATED_BASE)
        if args.variant:
            extras = _VARIANT_EXTRAS.get(args.variant)
            if extras is None:
                sys.stderr.write(
                    f"WARN: unknown variant '{args.variant}'; emitting "
                    "curated base only. Add the variant's extras to "
                    "_VARIANT_EXTRAS in this script when needed.\n"
                )
            else:
                field_filter += extras

    schema = build_schema(
        field_filter=field_filter,
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
