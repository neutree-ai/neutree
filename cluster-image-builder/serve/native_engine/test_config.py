from __future__ import annotations

import pytest

from serve.native_engine.config import build_engine_args, startup_timeout_s


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


def test_build_engine_args_expands_vllm_multi_value_flags_and_model_aliases() -> None:
    args = build_engine_args(
        runtime={"command": ["vllm", "serve"]},
        model={"path": "/models/demo", "serve_name": "chat-model"},
        engine_args={
            "allowed_media_domains": ["media.example", "assets.example"],
            "logits_processors": ["pkg.First", "pkg.Second"],
            "override_generation_config": {"temperature": 0.2},
            "served_model_name": ["chat-model", "chat-alias"],
            "cudagraph_capture_sizes": [],
        },
        port=30000,
    )

    assert args == [
        "vllm",
        "serve",
        "--model",
        "/models/demo",
        "--served-model-name",
        "chat-model",
        "chat-alias",
        "--host",
        "127.0.0.1",
        "--port",
        "30000",
        "--allowed-media-domains",
        "media.example",
        "assets.example",
        "--logits-processors",
        "pkg.First",
        "pkg.Second",
        "--override-generation-config",
        '{"temperature":0.2}',
    ]


def test_startup_timeout_defaults_without_a_control_plane_field() -> None:
    assert startup_timeout_s({}) == 1200.0
    assert startup_timeout_s({"startup_timeout_seconds": "90"}) == 90.0
