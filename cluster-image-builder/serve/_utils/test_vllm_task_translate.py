"""Tests for serve._utils.vllm_task_translate."""

from serve._utils.vllm_task_translate import task_kwargs


class TestTaskKwargs:
    def test_text_embedding_maps_to_pooling_embed(self):
        assert task_kwargs("text-embedding") == {"runner": "pooling", "convert": "embed"}

    def test_text_rerank_maps_to_pooling_classify(self):
        assert task_kwargs("text-rerank") == {"runner": "pooling", "convert": "classify"}

    def test_text_generation_returns_empty(self):
        # text-generation relies on vLLM's auto runner (generate); no flags injected.
        assert task_kwargs("text-generation") == {}

    def test_unknown_task_returns_empty(self):
        assert task_kwargs("not-a-real-task") == {}

    def test_empty_string_returns_empty(self):
        assert task_kwargs("") == {}

    def test_returns_fresh_dict_each_call(self):
        # Mutating the returned dict must not poison subsequent calls.
        d1 = task_kwargs("text-embedding")
        d1["runner"] = "mutated"
        d1["extra"] = "junk"
        d2 = task_kwargs("text-embedding")
        assert d2 == {"runner": "pooling", "convert": "embed"}
