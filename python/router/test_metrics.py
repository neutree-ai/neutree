import unittest

from router.metrics import MetricsRegistry, render_prometheus
from router.scheduling import EndpointInfo, RequestStats


class RouterMetricsTests(unittest.TestCase):
    def test_render_prometheus_exports_neutree_router_metrics(self):
        registry = MetricsRegistry()
        registry.record_incoming("ws", "ep")
        endpoints = [
            EndpointInfo(
                url="http://10.0.0.1:8000",
                model_names=["m"],
                workspace="ws",
                endpoint="ep",
            )
        ]
        stats = {"http://10.0.0.1:8000": RequestStats(active_requests=2, qps=3.5, avg_latency=0.25)}

        text = render_prometheus(registry, endpoints, stats, cpu_percent=12.5)

        self.assertIn("router_cpu_usage_percent 12.5", text)
        self.assertIn(
            'router:healthy_pods_total{workspace="ws",endpoint="ep",server="http://10.0.0.1:8000"} 1',
            text,
        )
        self.assertIn(
            'router:current_qps{workspace="ws",endpoint="ep",server="http://10.0.0.1:8000"} 3.5',
            text,
        )
        self.assertIn(
            'router:num_requests_running{workspace="ws",endpoint="ep",server="http://10.0.0.1:8000"} 2',
            text,
        )
        self.assertIn('router:num_incoming_requests_total{workspace="ws",endpoint="ep"} 1', text)


if __name__ == "__main__":
    unittest.main()
