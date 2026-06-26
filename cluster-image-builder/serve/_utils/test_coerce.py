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

    def test_pep604_optional_list_plain_string_wrapped(self):
        args = {"allowed_media_domains": "a.com"}
        coerce_args(args, FakeEngineV017)
        assert args["allowed_media_domains"] == ["a.com"]

    def test_pep604_optional_list_json_object_string_wrapped(self):
        raw = '{"domain":"a.com"}'
        args = {"allowed_media_domains": raw}
        coerce_args(args, FakeEngineV017)
        assert args["allowed_media_domains"] == [raw]

    def test_pep604_list_elements_validated(self):
        args = {"plain_list": '["1", "2", "3"]'}
        coerce_args(args, SampleModel)
        assert args["plain_list"] == [1, 2, 3]

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

    def test_typeadapter_constructor_raise_keeps_string(self, monkeypatch, caplog):
        """If TypeAdapter(target) schema generation itself raises (e.g.
        PydanticUserError on an unresolved forward-ref in a nested field,
        NameError, etc.), coerce_args must keep the original string and not
        crash replica startup."""
        from serve._utils import coerce

        class _BoomError(Exception):
            pass

        def fake_typeadapter(target):
            raise _BoomError("schema generation blew up")

        monkeypatch.setattr(coerce, "TypeAdapter", fake_typeadapter)
        args = {"attn_required": '{"backend":"FLASH"}'}
        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            coerce_args(args, FakeEngineV017)
        assert args["attn_required"] == '{"backend":"FLASH"}'
        assert "TypeAdapter construction or unexpected error" in caplog.text

    def test_string_annotation_passthrough(self):
        """Under NEU-433 last-resort recovery, _get_field_annotations returns
        raw annotation strings from dataclasses.fields() instead of resolved
        type objects. _is_dataclass_like / _wants_dict_or_list must short-
        circuit on str — annotation is not a type, no hydration attempted,
        original string preserved."""
        from serve._utils.coerce import _is_dataclass_like, _wants_dict, _wants_list

        # Both predicates safely return False on raw annotation strings.
        assert _is_dataclass_like("FakeAttentionConfig") is False
        assert _wants_dict("dict[str, Any] | None") is False
        assert _wants_list("list[str] | None") is False


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


@dataclass
class _AllStringAnnotationsArgs:
    """Closer simulation of the production vLLM v0.20.0 case: every annotation
    is stored as a string (as ``from __future__ import annotations`` would
    produce module-wide), and one references an undefined name so a naive
    ``typing.get_type_hints()`` call raises. The Option B recovery path injects
    ``Any`` for the missing name and re-runs, so callers see real type objects
    for every annotation and ``coerce_args`` keeps full JSON-string coercion.
    """

    name: "str" = ""
    config: "Optional[Dict[str, Any]]" = None
    items: "Optional[List[str]]" = None
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
        # Cache is shared across tests; force a fresh evaluation here.
        _get_field_annotations.cache_clear()

        args = {"model": "llama", "max_model_len": 4096, "bogus": 42}
        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            filter_engine_args(args, _BrokenTypeHintsArgs)
        assert args == {"model": "llama", "max_model_len": 4096}
        # Safety net must still fire — not the "could not introspect" bail-out.
        assert "could not introspect" not in caplog.text
        assert "1 unknown engine parameter(s) ignored" in caplog.text
        assert "bogus" in caplog.text


# ---------------------------------------------------------------------------
# Tests for _get_field_annotations recovery (NEU-433, Option B)
# ---------------------------------------------------------------------------


class TestGetFieldAnnotationsRecovery:
    def test_recovers_real_type_objects_via_localns_injection(self, caplog):
        """When ``typing.get_type_hints`` raises ``NameError`` on a
        TYPE_CHECKING-only alias, the recovery loop injects ``Any`` for the
        missing name and re-runs. Healthy fields keep their real type objects
        so downstream consumers (``coerce_args``) keep full functionality.
        """
        # Sanity: the fixture really triggers the failure mode we care about.
        with pytest.raises(NameError):
            typing.get_type_hints(_BrokenTypeHintsArgs)
        _get_field_annotations.cache_clear()

        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            result = _get_field_annotations(_BrokenTypeHintsArgs)

        assert set(result.keys()) == {"model", "max_model_len", "quantization_config"}
        # Healthy fields recover real type objects, not raw strings.
        assert result["model"] is str
        assert result["max_model_len"] is int
        # Recovery succeeded → no fallback warning emitted.
        assert "falling back to __dataclass_fields__" not in caplog.text

    def test_coerce_args_recovers_dict_and_list_coercion(self):
        """Under Option B every healthy annotation resolves to a real type
        object, so dict/list JSON-string coercion still works on the
        SSH/Ray path even when one field's annotation referenced a missing
        TYPE_CHECKING alias.
        """
        _get_field_annotations.cache_clear()
        args = {
            "config": '{"temperature": 0.5}',
            "items": '["a", "b"]',
        }
        coerce_args(args, _AllStringAnnotationsArgs)
        # Healthy fields decoded — Option B preserves their real types.
        assert args["config"] == {"temperature": 0.5}
        assert args["items"] == ["a", "b"]

    def test_filter_engine_args_strips_unknowns_under_recovery(self):
        """``filter_engine_args`` still gets the full field-name keyset
        after recovery, so unknown keys are stripped as designed.
        """
        _get_field_annotations.cache_clear()
        args = {
            "name": "model-x",
            "config": '{"temperature": 0.5}',
            "bogus": 42,
        }
        filter_engine_args(args, _AllStringAnnotationsArgs)
        # filter only strips unknowns; it doesn't coerce — values stay as-is.
        assert args == {"name": "model-x", "config": '{"temperature": 0.5}'}

    def test_falls_back_to_fields_when_recovery_cannot_extract_name(
        self, monkeypatch, caplog
    ):
        """If the NameError message format is unrecognisable (theoretical
        future Python change, or another exception type altogether) the
        function still degrades gracefully: ``dataclasses.fields()`` is
        called so ``filter_engine_args`` keeps the keyset, while
        ``coerce_args`` falls back to a pass-through. This pins the last-
        resort safety behaviour against future Python releases.
        """
        _get_field_annotations.cache_clear()

        original = typing.get_type_hints

        def fake_get_type_hints(*args, **kwargs):
            # Raise NameError with a non-standard message so the regex misses.
            raise NameError("unrecognised wording about a missing name")

        monkeypatch.setattr(typing, "get_type_hints", fake_get_type_hints)

        with caplog.at_level(logging.WARNING, logger="ray.serve"):
            result = _get_field_annotations(_BrokenTypeHintsArgs)

        # Keyset preserved through dataclasses.fields() last-resort fallback.
        assert set(result.keys()) == {"model", "max_model_len", "quantization_config"}
        # Recovery failed warning emitted so operators know coerce degraded.
        assert "falling back to __dataclass_fields__" in caplog.text

        # Restore for any subsequent tests.
        monkeypatch.setattr(typing, "get_type_hints", original)
