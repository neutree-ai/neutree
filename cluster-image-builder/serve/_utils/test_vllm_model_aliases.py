"""Tests for vLLM OpenAI model alias registration helpers."""

from dataclasses import dataclass

from serve._utils.vllm_model_aliases import build_base_model_paths


@dataclass
class FakeBaseModelPath:
    name: str
    model_path: str


class TestBuildBaseModelPaths:
    def test_expands_served_model_name_list_to_individual_base_paths(self):
        paths = build_base_model_paths(
            FakeBaseModelPath,
            ["primary-model", "neu-vllm-list-alias"],
            "/models/qwen",
        )

        assert paths == [
            FakeBaseModelPath(name="primary-model", model_path="/models/qwen"),
            FakeBaseModelPath(name="neu-vllm-list-alias", model_path="/models/qwen"),
        ]

    def test_preserves_single_string_served_model_name(self):
        paths = build_base_model_paths(
            FakeBaseModelPath,
            "primary-model",
            "/models/qwen",
        )

        assert paths == [
            FakeBaseModelPath(name="primary-model", model_path="/models/qwen"),
        ]

    def test_accepts_tuple_aliases(self):
        paths = build_base_model_paths(
            FakeBaseModelPath,
            ("primary-model", "tuple-alias"),
            "/models/qwen",
        )

        assert paths == [
            FakeBaseModelPath(name="primary-model", model_path="/models/qwen"),
            FakeBaseModelPath(name="tuple-alias", model_path="/models/qwen"),
        ]
