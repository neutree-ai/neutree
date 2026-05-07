"""Tests for serve._utils.coerce_args and filter_engine_args."""

import logging
import typing
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Any

import pytest
from pydantic import BaseModel
from pydantic.dataclasses import dataclass as pydantic_dataclass

from serve._utils import coerce_args, filter_engine_args
from serve._utils.coerce import _get_field_annotations


class SampleModel(BaseModel):
    name: str = ""
    count: int = 0
    config: Optional[Dict[str, Any]] = None
    items: Optional[List[str]] = None
    tags: Dict[str, str] = {}
    plain_list: List[int] = []


@pydantic_dataclass
class SamplePydanticDataclass:
    name: str = ""
    config: Optional[Dict[str, Any]] = None


@dataclass
class SampleStdDataclass:
    name: str = ""
    config: Optional[Dict[str, Any]] = None
    items: Optional[List[str]] = None
    count: int = 0


class TestCoerceArgs:
    def test_string_to_dict(self):
        args = {"config": '{"temperature": 0.5}'}
        coerce_args(args, SampleModel)
        assert args["config"] == {"temperature": 0.5}

    def test_string_to_list(self):
        args = {"items": '["a", "b", "c"]'}
        coerce_args(args, SampleModel)
        assert args["items"] == ["a", "b", "c"]

    def test_string_field_untouched(self):
        args = {"name": '{"looks": "like json"}'}
        coerce_args(args, SampleModel)
        assert args["name"] == '{"looks": "like json"}'

    def test_int_field_untouched(self):
        args = {"count": "42"}
        coerce_args(args, SampleModel)
        assert args["count"] == "42"  # not coerced — only dict/list fields

    def test_non_string_value_skipped(self):
        args = {"config": {"already": "a dict"}}
        coerce_args(args, SampleModel)
        assert args["config"] == {"already": "a dict"}

    def test_invalid_json_left_as_is(self):
        args = {"config": "not valid json"}
        coerce_args(args, SampleModel)
        assert args["config"] == "not valid json"

    def test_dict_field_without_optional(self):
        args = {"tags": '{"env": "prod"}'}
        coerce_args(args, SampleModel)
        assert args["tags"] == {"env": "prod"}

    def test_list_field_without_optional(self):
        args = {"plain_list": "[1, 2, 3]"}
        coerce_args(args, SampleModel)
        assert args["plain_list"] == [1, 2, 3]

    def test_unknown_field_ignored(self):
        args = {"unknown": '{"a": 1}'}
        coerce_args(args, SampleModel)
        assert args["unknown"] == '{"a": 1}'

    def test_works_with_pydantic_dataclass(self):
        args = {"config": '{"key": "value"}'}
        coerce_args(args, SamplePydanticDataclass)
        assert args["config"] == {"key": "value"}

    def test_non_pydantic_class_is_noop(self):
        args = {"config": '{"key": "value"}'}
        coerce_args(args, dict)
        assert args["config"] == '{"key": "value"}'

    def test_mixed_args(self):
        args = {
            "name": "my-model",
            "config": '{"temperature": 0.5}',
            "items": '["a", "b"]',
            "count": "10",
        }
        coerce_args(args, SampleModel)
        assert args["name"] == "my-model"
        assert args["config"] == {"temperature": 0.5}
        assert args["items"] == ["a", "b"]
        assert args["count"] == "10"

    # --- Standard dataclass tests ---

    def test_std_dataclass_string_to_dict(self):
        args = {"config": '{"temperature": 0.5}'}
        coerce_args(args, SampleStdDataclass)
        assert args["config"] == {"temperature": 0.5}

    def test_std_dataclass_string_to_list(self):
        args = {"items": '["a", "b"]'}
        coerce_args(args, SampleStdDataclass)
        assert args["items"] == ["a", "b"]

    def test_std_dataclass_string_field_untouched(self):
        args = {"name": '{"looks": "like json"}'}
        coerce_args(args, SampleStdDataclass)
        assert args["name"] == '{"looks": "like json"}'

    def test_std_dataclass_int_field_untouched(self):
        args = {"count": "42"}
        coerce_args(args, SampleStdDataclass)
        assert args["count"] == "42"



# ---------------------------------------------------------------------------
# Fixture for filter_engine_args tests
# ---------------------------------------------------------------------------

@dataclass
class _FakeEngineArgs:
    model: str = ""
    max_model_len: int = 0
    tensor_parallel_size: int = 1
    reasoning_parser: Optional[str] = None


@dataclass
class _BrokenTypeHintsArgs:
    """Mirrors vLLM v0.20.0 AsyncEngineArgs: one field annotation is a string
    forward-ref to a name that does not exist at runtime, so
    ``typing.get_type_hints()`` raises ``NameError``. ``get_type_hints`` is
    all-or-nothing — one bad field invalidates the whole result.
    """

    model: str = ""
    max_model_len: int = 0
    # Forward-ref string referencing an undefined symbol — eval at runtime fails.
    quantization_config: "_NonExistentRuntimeAlias | None" = None  # type: ignore[name-defined]


# ---------------------------------------------------------------------------
# Tests for filter_engine_args
# ---------------------------------------------------------------------------

class TestFilterEngineArgs:
    def test_known_fields_kept(self):
        args = {"model": "llama", "max_model_len": 4096}
        filter_engine_args(args, _FakeEngineArgs)
        assert args == {"model": "llama", "max_model_len": 4096}

    def test_unknown_fields_removed(self, caplog):
        args = {"model": "llama", "response_role": "user", "bogus": 42}
        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            filter_engine_args(args, _FakeEngineArgs)
        assert args == {"model": "llama"}
        assert "2 unknown engine parameter(s) ignored" in caplog.text
        assert "bogus" in caplog.text
        assert "response_role" in caplog.text

    def test_empty_args(self):
        args = {}
        filter_engine_args(args, _FakeEngineArgs)
        assert args == {}

    def test_all_unknown(self, caplog):
        args = {"tool_call_parser": "hermes", "chat_template": "custom"}
        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            filter_engine_args(args, _FakeEngineArgs)
        assert args == {}
        assert "2 unknown engine parameter(s) ignored" in caplog.text

    def test_non_dataclass_is_noop(self, caplog):
        args = {"anything": "goes"}
        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            filter_engine_args(args, dict)
        assert args == {"anything": "goes"}
        assert "could not introspect" in caplog.text

    def test_filter_works_when_get_type_hints_raises(self, caplog):
        """Regression for NEU-433: when ``typing.get_type_hints`` raises
        (e.g., vLLM v0.20.0 AsyncEngineArgs missing a TYPE_CHECKING alias),
        ``filter_engine_args`` must still strip unknown keys via the
        ``__dataclass_fields__`` fallback, not silently no-op.
        """
        # Sanity: the fixture really triggers the failure mode we care about.
        with pytest.raises(NameError):
            typing.get_type_hints(_BrokenTypeHintsArgs)

        args = {"model": "llama", "max_model_len": 4096, "bogus": 42}
        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            filter_engine_args(args, _BrokenTypeHintsArgs)
        assert args == {"model": "llama", "max_model_len": 4096}
        # Safety net must still fire — not the "could not introspect" bail-out.
        assert "could not introspect" not in caplog.text
        assert "1 unknown engine parameter(s) ignored" in caplog.text
        assert "bogus" in caplog.text


# ---------------------------------------------------------------------------
# Tests for _get_field_annotations fallback (NEU-433)
# ---------------------------------------------------------------------------


class TestGetFieldAnnotationsFallback:
    def test_returns_field_names_when_get_type_hints_raises(self):
        """When ``typing.get_type_hints`` raises on a dataclass, fall back
        to ``dataclasses.fields()`` so the field-name keyset is preserved.
        """
        with pytest.raises(NameError):
            typing.get_type_hints(_BrokenTypeHintsArgs)

        result = _get_field_annotations(_BrokenTypeHintsArgs)
        assert set(result.keys()) == {"model", "max_model_len", "quantization_config"}

    def test_coerce_args_degrades_silently_under_fallback(self):
        """Under the fallback path ``f.type`` is a raw string rather than a
        real type object, so dict/list coercion silently degrades to a
        pass-through. Acceptable trade-off documented in NEU-433: better
        than the prior behaviour where the entire safety net no-opped.
        """
        args = {"quantization_config": '{"key": "value"}'}
        coerce_args(args, _BrokenTypeHintsArgs)
        # No exception, no coercion — value passes through untouched.
        assert args["quantization_config"] == '{"key": "value"}'
