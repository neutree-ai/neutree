"""Translate Neutree's model_task to vLLM's --runner/--convert engine kwargs.

vLLM removed --task in v0.17.0 and replaced it with --runner + --convert (both
default "auto"). Auto-detect can fall back to a generate runner for multimodal
embedding architectures (e.g. Qwen3-VL-Embedding), so Neutree must translate the
registry-level task explicitly. This table is shared across vllm version-specific
Ray Serve apps so adding a new vllm version is one import line.
"""

_TASK_KWARGS: dict[str, dict[str, str]] = {
    "text-embedding": {"runner": "pooling", "convert": "embed"},
    "text-rerank":    {"runner": "pooling", "convert": "classify"},
    # text-generation: leave both defaults at "auto"; vLLM picks generate.
}


def task_kwargs(model_task: str | None) -> dict[str, str]:
    """Return a fresh dict of runner/convert kwargs for a Neutree model_task.

    Empty dict for unknown / generation tasks so callers can do
    ``engine_kwargs.setdefault(k, v)`` without special-casing.
    """
    return dict(_TASK_KWARGS.get(model_task, {}))
