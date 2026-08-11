from __future__ import annotations

from serve.native_engine.vllm_v0_24_0_compat import adapt_vllm_v0_24_0_args


def test_adapter_translates_v1_1_1_application_args_without_mutation(
    monkeypatch,
) -> None:
    monkeypatch.setenv("ENGINE_NAME", "vllm")
    monkeypatch.setenv("ENGINE_VERSION", "v0.24.0-native")
    original_container = {
        "image": "neutree/engine-vllm:v0.24.0-native-ray2.53.0",
        "run_options": ["--gpus all", "--rm"],
    }
    legacy_args = {
        "model": {"path": "/models/demo", "serve_name": "demo"},
        "engine_args": {"tensor_parallel_size": 2},
        "deployment_options": {"backend": {"num_gpus": 1}},
        "backend_container": original_container,
    }

    adapted = adapt_vllm_v0_24_0_args(legacy_args)

    assert adapted["native_runtime"] == {
        "command": ["vllm", "serve"],
        "health_path": "/health",
        "metrics_path": "/metrics",
    }
    assert adapted["engine_identity"] == {
        "name": "vllm",
        "version": "v0.24.0-native",
    }
    assert adapted["backend_container"] == {
        "image": "neutree/engine-vllm:v0.24.0-native-ray2.53.0",
        "run_options": [
            "--gpus all",
            "--rm",
            "--ipc=host",
            "-v /tmp/neutree/ports:/var/run/neutree/ports",
        ],
    }
    assert legacy_args["backend_container"] is original_container
    assert original_container["run_options"] == ["--gpus all", "--rm"]


def test_adapter_does_not_duplicate_the_port_state_mount() -> None:
    adapted = adapt_vllm_v0_24_0_args(
        {
            "backend_container": {
                "image": "neutree/engine-vllm:v0.24.0-native-ray2.53.0",
                "run_options": [
                    "--ipc=host",
                    "-v /tmp/neutree/ports:/var/run/neutree/ports",
                ],
            }
        }
    )

    assert adapted["backend_container"]["run_options"] == [
        "--ipc=host",
        "-v /tmp/neutree/ports:/var/run/neutree/ports"
    ]
