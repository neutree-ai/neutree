import unittest

from router.routing import ConsistentHashRouter, EndpointInfo, RequestStats, RoundRobinRouter


class RouterRoutingTests(unittest.TestCase):
    def test_round_robin_cycles_over_sorted_endpoint_urls(self):
        router = RoundRobinRouter()
        endpoints = [
            EndpointInfo(url="http://pod-b:8000", model_names=["m"]),
            EndpointInfo(url="http://pod-a:8000", model_names=["m"]),
        ]

        got = [router.route(endpoints, {}, {}, {"model": "m"}) for _ in range(4)]

        self.assertEqual(
            got,
            [
                "http://pod-a:8000",
                "http://pod-b:8000",
                "http://pod-a:8000",
                "http://pod-b:8000",
            ],
        )

    def test_consistent_hash_is_stable_for_same_chat_prefix(self):
        router = ConsistentHashRouter()
        endpoints = [
            EndpointInfo(url="http://pod-a:8000", model_names=["m"], workspace="w", endpoint="e"),
            EndpointInfo(url="http://pod-b:8000", model_names=["m"], workspace="w", endpoint="e"),
            EndpointInfo(url="http://pod-c:8000", model_names=["m"], workspace="w", endpoint="e"),
        ]
        payload = {
            "model": "m",
            "messages": [
                {"role": "system", "content": "be concise"},
                {"role": "user", "content": "hello"},
            ],
        }

        first = router.route(endpoints, {}, {}, payload)
        second = router.route(list(reversed(endpoints)), {}, {}, payload)

        self.assertEqual(first, second)

    def test_consistent_hash_skips_overloaded_replica_when_possible(self):
        router = ConsistentHashRouter(load_factor=1.0)
        endpoints = [
            EndpointInfo(url="http://pod-a:8000", model_names=["m"], workspace="w", endpoint="e"),
            EndpointInfo(url="http://pod-b:8000", model_names=["m"], workspace="w", endpoint="e"),
        ]
        stats = {
            "http://pod-a:8000": RequestStats(active_requests=50),
            "http://pod-b:8000": RequestStats(active_requests=0),
        }

        got = router.route(endpoints, {}, stats, {"model": "m", "prompt": "same"})

        self.assertEqual(got, "http://pod-b:8000")


if __name__ == "__main__":
    unittest.main()
