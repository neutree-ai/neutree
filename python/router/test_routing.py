import unittest

from router.routing import (
    ConsistentHashRouter,
    EndpointInfo,
    PDRouteUnit,
    PDSameHostRouter,
    RequestStats,
    RoundRobinRouter,
)


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

    def test_pd_router_selects_prefill_from_decode_local_role_group(self):
        router = PDSameHostRouter(load_factor=1.0)
        endpoint_a = EndpointInfo(
            url="http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            pd_route_units=[
                PDRouteUnit("group-a", "prefill", 0, True, "http://10.0.0.1:8000"),
                PDRouteUnit("group-a", "decode", 0, True, "http://10.0.0.1:8000"),
            ],
        )
        endpoint_b = EndpointInfo(
            url="http://10.0.0.2:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            pd_route_units=[
                PDRouteUnit("group-b", "prefill", 0, True, "http://10.0.0.2:8000"),
                PDRouteUnit("group-b", "decode", 0, True, "http://10.0.0.2:8000"),
            ],
        )
        stats = {
            "group-a:decode:0": RequestStats(active_requests=20),
            "group-b:decode:0": RequestStats(active_requests=0),
            "group-b:prefill:0": RequestStats(active_requests=0),
        }

        decision = router.route(
            [endpoint_a, endpoint_b],
            {},
            stats,
            {"model": "m", "messages": [{"role": "user", "content": "hello"}]},
        )

        self.assertEqual(decision.decode.role_group_id, "group-b")
        self.assertEqual(decision.prefill.role_group_id, "group-b")
        self.assertEqual(decision.sidecar_url, "http://10.0.0.2:8000")
        self.assertEqual(decision.prefill_index, 0)
        self.assertEqual(decision.decode_index, 0)

    def test_pd_router_falls_back_to_load_only_when_cache_key_is_missing(self):
        router = PDSameHostRouter()
        endpoints = [
            EndpointInfo(
                url="http://10.0.0.1:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                pd_route_units=[
                    PDRouteUnit("group-a", "prefill", 0, True, "http://10.0.0.1:8000"),
                    PDRouteUnit("group-a", "decode", 0, True, "http://10.0.0.1:8000"),
                ],
            ),
            EndpointInfo(
                url="http://10.0.0.2:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                pd_route_units=[
                    PDRouteUnit("group-b", "prefill", 0, True, "http://10.0.0.2:8000"),
                    PDRouteUnit("group-b", "decode", 0, True, "http://10.0.0.2:8000"),
                ],
            ),
        ]
        stats = {
            "group-a:decode:0": RequestStats(active_requests=3),
            "group-b:decode:0": RequestStats(active_requests=0),
        }

        decision = router.route(endpoints, {}, stats, {"model": "m"})

        self.assertEqual(decision.decode.role_group_id, "group-b")
        self.assertEqual(decision.prefill.role_group_id, "group-b")

    def test_pd_router_fails_closed_without_local_prefill(self):
        router = PDSameHostRouter()
        endpoint = EndpointInfo(
            url="http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            pd_route_units=[
                PDRouteUnit("group-a", "decode", 0, True, "http://10.0.0.1:8000"),
            ],
        )

        with self.assertRaisesRegex(ValueError, "no ready prefill"):
            router.route([endpoint], {}, {}, {"model": "m"})


if __name__ == "__main__":
    unittest.main()
