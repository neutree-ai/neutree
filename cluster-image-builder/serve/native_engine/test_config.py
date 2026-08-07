from __future__ import annotations

import pytest

from serve.native_engine.config import build_engine_args


def test_build_engine_args_keeps_native_image_entrypoint_and_owns_network_flags() -> None:
    args = build_engine_args(
        runtime={"command": ["vllm", "serve"]},
        model={
            "path": "/mnt/neutree/models/default/Qwen/Qwen2.5-0.5B-Instruct/main",
            "serve_name": "chat-model",
        },
        engine_args={
            "tensor_parallel_size": 2,
            "host": "0.0.0.0",
            "port": 8000,
            "disable_log_stats": True,
            "guided_decoding_backend": "outlines",
        },
        port=18000,
    )

    assert args == [
        "vllm",
        "serve",
        "--model",
        "/mnt/neutree/models/default/Qwen/Qwen2.5-0.5B-Instruct/main",
        "--served-model-name",
        "chat-model",
        "--host",
        "127.0.0.1",
        "--port",
        "18000",
        "--disable-log-stats",
        "--guided-decoding-backend",
        "outlines",
        "--tensor-parallel-size",
        "2",
    ]


def test_build_engine_args_requires_a_direct_server_command() -> None:
    with pytest.raises(ValueError, match="runtime.command"):
        build_engine_args(
        runtime={"command": None},
        model={"path": "/models/demo", "serve_name": "demo"},
        engine_args={},
        port=18000,
        )


def test_build_engine_args_coerces_boolean_strings_from_ssh_variables() -> None:
    args = build_engine_args(
        runtime={"command": ["vllm", "serve"]},
        model={"path": "/models/demo"},
        engine_args={"enforce_eager": "true", "enable_prefix_caching": "false"},
        port=18000,
    )

    assert "--enforce-eager" in args
    assert "--enable-prefix-caching" not in args
    assert "false" not in args
