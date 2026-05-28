import unittest

from router.routing import (
    ConsistentHashRouter,
    EndpointInfo,
    EndpointHashScorer,
    EndpointLoadScorer,
    PDSameHostRouter,
    RequestStats,
    RoundRobinRouter,
    WeightedEndpointScorer,
    WeightedScoringRouter,
)


class RouterRoutingTests(unittest.TestCase):
    def test_round_robin_cycles_over_sorted_endpoint_urls(self):
        router = RoundRobinRouter()
        endpoints = [
            EndpointInfo(url="http://pod-b:8000", model_names=["m"]),
            EndpointInfo(url="http://pod-a:8000", model_names=["m"]),
        ]

        got = [router.route(endpoints, {}, {}, {"model": "m"}).url for _ in range(4)]

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

        self.assertEqual(first.url, second.url)

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

        self.assertEqual(got.url, "http://pod-b:8000")

    def test_weighted_scoring_router_combines_multiple_plugin_scores(self):
        router = WeightedScoringRouter(
            [
                WeightedEndpointScorer(EndpointHashScorer(), 1.0),
                WeightedEndpointScorer(EndpointLoadScorer(), 2.0),
            ]
        )
        endpoints = [
            EndpointInfo(url="http://pod-a:8000", model_names=["m"], workspace="w", endpoint="e"),
            EndpointInfo(url="http://pod-b:8000", model_names=["m"], workspace="w", endpoint="e"),
        ]
        stats = {
            "http://pod-a:8000": RequestStats(active_requests=50),
            "http://pod-b:8000": RequestStats(active_requests=0),
        }

        got = router.route(endpoints, {}, stats, {"model": "m", "prompt": "same"})

        self.assertEqual(got.url, "http://pod-b:8000")

    def test_pd_router_selects_decode_endpoint_then_local_prefill_endpoint(self):
        router = PDSameHostRouter()
        prefill_a = EndpointInfo(
            url="pd://group-a/prefill/0?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            pd_role_group_id="group-a",
            pd_role="prefill",
            pd_index=0,
        )
        decode_a = EndpointInfo(
            url="pd://group-a/decode/0?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            pd_role_group_id="group-a",
            pd_role="decode",
            pd_index=0,
        )
        prefill_b = EndpointInfo(
            url="pd://group-b/prefill/0?sidecar=http://10.0.0.2:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.2:8000",
            pd_role_group_id="group-b",
            pd_role="prefill",
            pd_index=0,
        )
        decode_b = EndpointInfo(
            url="pd://group-b/decode/0?sidecar=http://10.0.0.2:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.2:8000",
            pd_role_group_id="group-b",
            pd_role="decode",
            pd_index=0,
        )
        stats = {
            decode_a.route_key: RequestStats(active_requests=20),
            decode_b.route_key: RequestStats(active_requests=0),
        }

        decision = router.route(
            [prefill_a, decode_a, prefill_b, decode_b],
            {},
            stats,
            {"model": "m", "messages": [{"role": "user", "content": "hello"}]},
        )

        self.assertEqual(decision.endpoint, decode_b)
        self.assertEqual(decision.decode, decode_b)
        self.assertEqual(decision.prefill, prefill_b)
        self.assertEqual(decision.url, "http://10.0.0.2:8000")
        self.assertEqual(decision.prefill_index, 0)
        self.assertEqual(decision.decode_index, 0)

    def test_pd_router_falls_back_to_load_only_when_cache_key_is_missing(self):
        router = PDSameHostRouter()
        endpoints = [
            EndpointInfo(
                url="pd://group-a/prefill/0?sidecar=http://10.0.0.1:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.1:8000",
                pd_role_group_id="group-a",
                pd_role="prefill",
                pd_index=0,
            ),
            EndpointInfo(
                url="pd://group-a/decode/0?sidecar=http://10.0.0.1:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.1:8000",
                pd_role_group_id="group-a",
                pd_role="decode",
                pd_index=0,
            ),
            EndpointInfo(
                url="pd://group-b/prefill/0?sidecar=http://10.0.0.2:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.2:8000",
                pd_role_group_id="group-b",
                pd_role="prefill",
                pd_index=0,
            ),
            EndpointInfo(
                url="pd://group-b/decode/0?sidecar=http://10.0.0.2:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.2:8000",
                pd_role_group_id="group-b",
                pd_role="decode",
                pd_index=0,
            ),
        ]
        stats = {
            "group-a:decode:0": RequestStats(active_requests=3),
            "group-b:decode:0": RequestStats(active_requests=0),
        }

        decision = router.route(endpoints, {}, stats, {"model": "m"})

        self.assertEqual(decision.endpoint.route_key, "group-b:decode:0")
        self.assertEqual(decision.prefill.route_key, "group-b:prefill:0")

    def test_pd_router_fails_closed_without_local_prefill(self):
        router = PDSameHostRouter()
        endpoint = EndpointInfo(
            url="pd://group-a/decode/0?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            pd_role_group_id="group-a",
            pd_role="decode",
            pd_index=0,
        )

        with self.assertRaisesRegex(ValueError, "no ready prefill endpoint"):
            router.route([endpoint], {}, {}, {"model": "m"})


if __name__ == "__main__":
    unittest.main()
