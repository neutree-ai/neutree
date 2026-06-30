"""Helpers for vLLM OpenAI model alias registration."""

from typing import List, Sequence, Type, TypeVar, Union


T = TypeVar("T")
ServedModelName = Union[str, Sequence[str]]


def served_model_names(served_model_name: ServedModelName) -> List[str]:
    if isinstance(served_model_name, str):
        return [served_model_name]
    return list(served_model_name)


def build_base_model_paths(
    base_model_path_cls: Type[T],
    served_model_name: ServedModelName,
    model_path: str,
) -> List[T]:
    return [
        base_model_path_cls(name=name, model_path=model_path)
        for name in served_model_names(served_model_name)
    ]
