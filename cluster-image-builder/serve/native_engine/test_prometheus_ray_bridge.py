from __future__ import annotations

from dataclasses import dataclass, field

from serve.native_engine.prometheus_ray_bridge import HttpPrometheusToRayBridge


@dataclass
class FakeMetric:
    name: str
    description: str
    tag_keys: tuple[str, ...]
    increments: list[tuple[float, dict[str, str]]] = field(default_factory=list)
    values: list[tuple[float, dict[str, str]]] = field(default_factory=list)

    def inc(self, value: float, tags: dict[str, str]) -> None:
        self.increments.append((value, tags))

    def set(self, value: float, tags: dict[str, str]) -> None:
        self.values.append((value, tags))


class FakeCounter(FakeMetric):
    created: list[FakeMetric] = []

    def __init__(self, name: str, description: str, tag_keys: tuple[str, ...]) -> None:
        super().__init__(name, description, tag_keys)
        self.created.append(self)


class FakeGauge(FakeMetric):
    created: list[FakeMetric] = []

    def __init__(self, name: str, description: str, tag_keys: tuple[str, ...]) -> None:
        super().__init__(name, description, tag_keys)
        self.created.append(self)


def new_bridge() -> HttpPrometheusToRayBridge:
    FakeCounter.created = []
    FakeGauge.created = []
    return HttpPrometheusToRayBridge(
        actor_tags={
            "application": "default_chat",
            "deployment": "NativeEngineRunner",
            "replica": "NativeEngineRunner#abc",
            "engine": "vllm",
            "engine_version": "v0.24.0",
        },
        counter_cls=FakeCounter,
        gauge_cls=FakeGauge,
    )


def test_counter_is_emitted_as_delta_and_rebaselined_after_reset() -> None:
    bridge = new_bridge()

    bridge.emit_text("""# TYPE vllm:request_success counter
vllm:request_success_total{model_name=\"demo\",request_type=\"chat\"} 10
""")
    bridge.emit_text("""# TYPE vllm:request_success counter
vllm:request_success_total{model_name=\"demo\",request_type=\"chat\"} 13
""")
    bridge.emit_text("""# TYPE vllm:request_success counter
vllm:request_success_total{model_name=\"demo\",request_type=\"chat\"} 2
""")

    assert len(FakeCounter.created) == 1
    assert [increment[0] for increment in FakeCounter.created[0].increments] == [10, 3, 2]
    assert FakeCounter.created[0].name == "vllm_request_success"
    assert FakeCounter.created[0].increments[-1][1] == {
        "application": "default_chat",
        "deployment": "NativeEngineRunner",
        "engine": "vllm",
        "engine_version": "v0.24.0",
        "model_name": "demo",
        "replica": "NativeEngineRunner#abc",
        "request_type": "chat",
    }


def test_histogram_samples_keep_le_and_all_native_labels() -> None:
    bridge = new_bridge()

    bridge.emit_text("""# TYPE vllm:e2e_request_latency_seconds histogram
vllm:e2e_request_latency_seconds_bucket{le=\"0.1\",model_name=\"demo\"} 3
vllm:e2e_request_latency_seconds_sum{model_name=\"demo\"} 0.2
vllm:e2e_request_latency_seconds_count{model_name=\"demo\"} 3
""")

    bucket = next(metric for metric in FakeGauge.created if metric.name.endswith("_bucket"))
    assert bucket.name == "vllm_e2e_request_latency_seconds_bucket"
    assert bucket.values == [
        (
            3,
            {
                "application": "default_chat",
                "deployment": "NativeEngineRunner",
                "engine": "vllm",
                "engine_version": "v0.24.0",
                "le": "0.1",
                "model_name": "demo",
                "replica": "NativeEngineRunner#abc",
            },
        )
    ]
