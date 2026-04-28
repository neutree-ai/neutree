"""Tests for serve._utils.coerce_args and filter_engine_args."""

import logging
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Any

from pydantic import BaseModel
from pydantic.dataclasses import dataclass as pydantic_dataclass

from serve._utils import coerce_args, filter_engine_args


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


# --- Fixtures for PEP 604 / dataclass-hydration coverage (NEU-425) ---


@dataclass
class FakeAttentionConfig:
    backend: str = ""


class FakePydanticConfig(BaseModel):
    x: int = 0


@dataclass
class FakeInner:
    v: int = 0


@dataclass
class FakeOuter:
    inner: FakeInner = field(default_factory=FakeInner)


@dataclass
class FakeEngineV017:
    """Minimal stub mirroring vLLM v0.17.1+ EngineArgs annotation shapes."""
    speculative_config: dict[str, Any] | None = None       # PEP 604 dict
    allowed_media_domains: list[str] | None = None          # PEP 604 list
    bare_dict: dict[str, str] = field(default_factory=dict) # PEP 604 bare
    attn_required: FakeAttentionConfig = field(default_factory=FakeAttentionConfig)
    attn_optional: FakeAttentionConfig | None = None        # PEP 604 + custom
    cfg_optional: FakePydanticConfig | None = None
    outer: FakeOuter = field(default_factory=FakeOuter)
    name: str = ""
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
# PEP 604 + dataclass-hydration tests (NEU-425)
# ---------------------------------------------------------------------------


class TestCoerceArgsPEP604:
    def test_pep604_optional_dict_coerced(self):
        args = {"speculative_config": '{"method":"mtp","num":2}'}
        coerce_args(args, FakeEngineV017)
        assert args["speculative_config"] == {"method": "mtp", "num": 2}

    def test_pep604_optional_list_coerced(self):
        args = {"allowed_media_domains": '["a.com","b.com"]'}
        coerce_args(args, FakeEngineV017)
        assert args["allowed_media_domains"] == ["a.com", "b.com"]

    def test_pep604_bare_dict_coerced(self):
        args = {"bare_dict": '{"k":"v"}'}
        coerce_args(args, FakeEngineV017)
        assert args["bare_dict"] == {"k": "v"}


class TestCoerceArgsDataclassHydration:
    def test_dataclass_field_hydrated_to_instance(self):
        args = {"attn_required": '{"backend":"FLASH"}'}
        coerce_args(args, FakeEngineV017)
        assert isinstance(args["attn_required"], FakeAttentionConfig)
        assert args["attn_required"].backend == "FLASH"

    def test_pep604_optional_dataclass_hydrated(self):
        args = {"attn_optional": '{"backend":"FLASH_ATTN"}'}
        coerce_args(args, FakeEngineV017)
        assert isinstance(args["attn_optional"], FakeAttentionConfig)
        assert args["attn_optional"].backend == "FLASH_ATTN"

    def test_pydantic_model_hydrated(self):
        args = {"cfg_optional": '{"x":42}'}
        coerce_args(args, FakeEngineV017)
        assert isinstance(args["cfg_optional"], FakePydanticConfig)
        assert args["cfg_optional"].x == 42

    def test_nested_dataclass_hydrated_recursively(self):
        args = {"outer": '{"inner":{"v":42}}'}
        coerce_args(args, FakeEngineV017)
        assert isinstance(args["outer"], FakeOuter)
        assert isinstance(args["outer"].inner, FakeInner)
        assert args["outer"].inner.v == 42

    def test_validation_error_keeps_string_and_warns(self, caplog):
        # backend expects str, give a list — Pydantic should reject
        args = {"attn_required": '{"backend": [1, 2, 3]}'}
        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            coerce_args(args, FakeEngineV017)
        assert args["attn_required"] == '{"backend": [1, 2, 3]}'
        assert "TypeAdapter" in caplog.text

    def test_invalid_json_for_dataclass_keeps_string(self):
        args = {"attn_required": "not valid json"}
        coerce_args(args, FakeEngineV017)
        assert args["attn_required"] == "not valid json"

    def test_native_instance_passthrough(self):
        existing = FakeAttentionConfig(backend="ALREADY_SET")
        args = {"attn_required": existing}
        coerce_args(args, FakeEngineV017)
        assert args["attn_required"] is existing


# ---------------------------------------------------------------------------
# Fixture for filter_engine_args tests
# ---------------------------------------------------------------------------

@dataclass
class _FakeEngineArgs:
    model: str = ""
    max_model_len: int = 0
    tensor_parallel_size: int = 1
    reasoning_parser: Optional[str] = None


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
