"""Tests for serve._utils.coerce_pydantic_args."""

from typing import Dict, List, Optional, Any

from pydantic import BaseModel
from pydantic.dataclasses import dataclass as pydantic_dataclass

from serve._utils import coerce_pydantic_args


class SampleModel(BaseModel):
    name: str = ""
    count: int = 0
    config: Optional[Dict[str, Any]] = None
    items: Optional[List[str]] = None
    tags: Dict[str, str] = {}
    plain_list: List[int] = []


@pydantic_dataclass
class SampleDataclass:
    name: str = ""
    config: Optional[Dict[str, Any]] = None


class TestCoercePydanticArgs:
    def test_string_to_dict(self):
        args = {"config": '{"temperature": 0.5}'}
        coerce_pydantic_args(args, SampleModel)
        assert args["config"] == {"temperature": 0.5}

    def test_string_to_list(self):
        args = {"items": '["a", "b", "c"]'}
        coerce_pydantic_args(args, SampleModel)
        assert args["items"] == ["a", "b", "c"]

    def test_string_field_untouched(self):
        args = {"name": '{"looks": "like json"}'}
        coerce_pydantic_args(args, SampleModel)
        assert args["name"] == '{"looks": "like json"}'

    def test_int_field_untouched(self):
        args = {"count": "42"}
        coerce_pydantic_args(args, SampleModel)
        assert args["count"] == "42"  # not coerced — only dict/list fields

    def test_non_string_value_skipped(self):
        args = {"config": {"already": "a dict"}}
        coerce_pydantic_args(args, SampleModel)
        assert args["config"] == {"already": "a dict"}

    def test_invalid_json_left_as_is(self):
        args = {"config": "not valid json"}
        coerce_pydantic_args(args, SampleModel)
        assert args["config"] == "not valid json"

    def test_dict_field_without_optional(self):
        args = {"tags": '{"env": "prod"}'}
        coerce_pydantic_args(args, SampleModel)
        assert args["tags"] == {"env": "prod"}

    def test_list_field_without_optional(self):
        args = {"plain_list": "[1, 2, 3]"}
        coerce_pydantic_args(args, SampleModel)
        assert args["plain_list"] == [1, 2, 3]

    def test_unknown_field_ignored(self):
        args = {"unknown": '{"a": 1}'}
        coerce_pydantic_args(args, SampleModel)
        assert args["unknown"] == '{"a": 1}'

    def test_works_with_pydantic_dataclass(self):
        args = {"config": '{"key": "value"}'}
        coerce_pydantic_args(args, SampleDataclass)
        assert args["config"] == {"key": "value"}

    def test_non_pydantic_class_is_noop(self):
        args = {"config": '{"key": "value"}'}
        coerce_pydantic_args(args, dict)
        assert args["config"] == '{"key": "value"}'

    def test_mixed_args(self):
        args = {
            "name": "my-model",
            "config": '{"temperature": 0.5}',
            "items": '["a", "b"]',
            "count": "10",
        }
        coerce_pydantic_args(args, SampleModel)
        assert args["name"] == "my-model"
        assert args["config"] == {"temperature": 0.5}
        assert args["items"] == ["a", "b"]
        assert args["count"] == "10"
