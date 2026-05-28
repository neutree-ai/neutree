from __future__ import annotations

from collections import defaultdict
from typing import Dict, Iterable, Tuple

from router.scheduling import EndpointInfo, RequestStats


class MetricsRegistry:
    def __init__(self):
        self._incoming: Dict[Tuple[str, str], int] = defaultdict(int)

    def record_incoming(self, workspace: str, endpoint: str) -> None:
        self._incoming[(workspace, endpoint)] += 1

    def incoming_items(self) -> Iterable[Tuple[Tuple[str, str], int]]:
        return self._incoming.items()


def render_prometheus(
    registry: MetricsRegistry,
    endpoints: Iterable[EndpointInfo],
    request_stats: Dict[str, RequestStats],
    cpu_percent: float = 0.0,
) -> str:
    lines = [
        "# TYPE router_cpu_usage_percent gauge",
        f"router_cpu_usage_percent {_format_float(cpu_percent)}",
        "# TYPE router:healthy_pods_total gauge",
        "# TYPE router:current_qps gauge",
        "# TYPE router:num_requests_running gauge",
        "# TYPE router:avg_latency gauge",
        "# TYPE router:avg_decoding_length gauge",
        "# TYPE router:avg_itl gauge",
        "# TYPE router:num_requests_swapped gauge",
        "# TYPE router:num_incoming_requests_total counter",
    ]
    for endpoint in sorted(endpoints, key=lambda item: item.url):
        labels = _labels(endpoint)
        stats = request_stats.get(endpoint.url, RequestStats())
        lines.extend(
            [
                f"router:healthy_pods_total{labels} 1",
                f"router:current_qps{labels} {_format_float(stats.qps)}",
                f"router:num_requests_running{labels} {stats.active_requests}",
                f"router:avg_latency{labels} {_format_float(stats.avg_latency)}",
                f"router:avg_decoding_length{labels} {_format_float(stats.avg_decoding_length)}",
                f"router:avg_itl{labels} {_format_float(stats.avg_itl)}",
                f"router:num_requests_swapped{labels} {stats.num_swapped_requests}",
            ]
        )
    for (workspace, endpoint), value in sorted(registry.incoming_items()):
        lines.append(
            "router:num_incoming_requests_total"
            f'{{workspace="{_escape(workspace)}",endpoint="{_escape(endpoint)}"}} {value}'
        )
    return "\n".join(lines) + "\n"


def _labels(endpoint: EndpointInfo) -> str:
    return (
        "{"
        f'workspace="{_escape(endpoint.workspace or "")}",'
        f'endpoint="{_escape(endpoint.endpoint or "")}",'
        f'server="{_escape(endpoint.url)}"'
        "}"
    )


def _escape(value: str) -> str:
    return value.replace("\\", "\\\\").replace("\n", "\\n").replace('"', '\\"')


def _format_float(value: float) -> str:
    return f"{value:g}"
